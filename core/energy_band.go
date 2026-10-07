package core

import (
	"fmt"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	optimizer "github.com/evcc-io/optimizer/client"
)

// Cheap-charge band (ADR-017). Between a vehicle's minSoc and its band top, energy is
// worth the reference price: the solver buys grid energy only below it, takes solar
// whenever feed-in pays less, and has no deadline. Below minSoc the loadpoint floor
// still fast-charges, unchanged. The loadpoint only follows the solver while the
// optimizer controls it; without a fresh suggestion the band does nothing.

const (
	cheapBandDefaultDays   = 30
	cheapBandDefaultMargin = 10.0 // percent below the median
	// cheapBandMinSamples is one day of 15-minute prices: a median over less history
	// is not a reference.
	cheapBandMinSamples = 96
	// cheapBandChargeMinWh is the planned charge treated as charging when explaining the band
	cheapBandChargeMinWh = 20
)

const (
	bandStatusInactive     = "inactive"     // mode, unknown soc or band top not above the minimum
	bandStatusReached      = "reached"      // soc at or above the band top
	bandStatusNoReference  = "noReference"  // not enough price history and no fixed price
	bandStatusBelowMinimum = "belowMinimum" // the minSoc floor charges first
	bandStatusCharging     = "charging"     // the plan charges in the current slot
	bandStatusWaitingSolar = "waitingSolar" // next planned charge is mostly solar
	bandStatusWaitingCheap = "waitingCheap" // next planned charge is a grid slot below the reference
	bandStatusNothingCheap = "nothingCheap" // nothing in the horizon pays off
)

// cheapBandReference is the price below which grid energy is worth storing in the band.
type cheapBandReference struct {
	Price   *float64 `json:"price,omitempty"`  // currency/kWh, AC side; nil when unavailable
	Median  *float64 `json:"median,omitempty"` // currency/kWh over Days
	Source  string   `json:"source"`           // median or fixed
	Days    int      `json:"days"`
	Margin  float64  `json:"margin"` // percent
	Samples int      `json:"samples"`
}

type cheapBandCache struct {
	key string
	ref cheapBandReference
}

// energyBand describes a vehicle's band for one optimizer run. The unexported fields
// are the solver inputs, set only while the band is active.
type energyBand struct {
	Status        string             `json:"status"`
	TopSoc        int                `json:"topSoc"` // percent, after clamping to the limit soc
	MinRunMinutes int                `json:"minRunMinutes"`
	Reference     cheapBandReference `json:"reference"`
	NextStart     *time.Time         `json:"nextStart,omitempty"`
	NextPrice     *float64           `json:"nextPrice,omitempty"` // currency/kWh

	active  bool
	sBand   float64 // Wh stored
	pBand   float64 // currency per stored Wh, on top of p_a
	minRun  float64 // s, 0 = none
	elapsed float64 // s the charging run in progress has lasted, 0 = none; below minRun it is held
	// home batteries must hold while the car charges in its band; never for the floor
	refuseStorage bool
}

// cheapBandRun is a vehicle's charging run as observed by the optimizer.
type cheapBandRun struct {
	start     time.Time
	fromFloor bool // started below minSoc: the floor's run, never held to the minimum
}

// cheapBandReference returns the reference for the current settings, cached per local
// day and window so the solver inputs do not drift between runs of the same day.
func (site *Site) cheapBandReference(cfg EnergyIntelligenceSettings, now time.Time) cheapBandReference {
	days, margin := cheapBandDefaultDays, cheapBandDefaultMargin
	if cfg.CheapBandDays != nil {
		days = *cfg.CheapBandDays
	}
	if cfg.CheapBandMargin != nil {
		margin = *cfg.CheapBandMargin
	}
	if cfg.CheapBandPrice != nil {
		price := *cfg.CheapBandPrice
		return cheapBandReference{Price: &price, Source: "fixed", Days: days, Margin: margin}
	}

	key := fmt.Sprintf("%s/%d/%g", now.Local().Format(time.DateOnly), days, margin)
	if site.cheapBand.key == key {
		return site.cheapBand.ref
	}

	ref := cheapBandReference{Source: "median", Days: days, Margin: margin}
	if db.Instance == nil {
		return ref
	}
	median, n, err := metrics.GridPriceMedian(now.AddDate(0, 0, -days), now)
	if err != nil {
		site.log.ERROR.Printf("cheap band reference: %v", err)
		return ref
	}
	ref.Samples = n
	if n >= cheapBandMinSamples {
		price := median * (1 - margin/100)
		ref.Median, ref.Price = &median, &price
	}
	site.cheapBand = cheapBandCache{key: key, ref: ref}
	return ref
}

// cheapBandInput is everything the band depends on, so the decision is a pure function.
type cheapBandInput struct {
	capacity         float64 // kWh
	soc              float64 // percent
	minSoc, limitSoc int     // percent
	bandSoc, minRun  int     // percent, minutes
	mode             api.ChargeMode
	fromBattery      bool // energy intelligence setting cheapBandFromBattery
	ref              cheapBandReference
	pa               float32 // the vehicle's terminal value, currency per stored Wh
	etaC             float64
}

// cheapBand decides whether the band applies and converts the reference price into
// solver terms. Grid energy bought at price p (AC) stores etaC Wh worth pa + pBand,
// so it pays off below etaC*(pa+pBand): pBand = reference/etaC - pa. pa already
// values the energy (see terminalStorageValue), so the band adds only the difference.
func cheapBand(in cheapBandInput) *energyBand {
	if in.bandSoc <= 0 {
		return nil
	}
	top := in.bandSoc
	if in.limitSoc > 0 && in.limitSoc < top {
		top = in.limitSoc
	}
	band := &energyBand{Status: bandStatusInactive, TopSoc: top, MinRunMinutes: in.minRun, Reference: in.ref}

	switch {
	case in.mode != api.ModePV && in.mode != api.ModeMinPV, in.capacity <= 0, in.soc <= 0, top <= in.minSoc:
		return band
	case in.soc >= float64(top):
		band.Status = bandStatusReached
		return band
	case in.ref.Price == nil:
		band.Status = bandStatusNoReference
		return band
	}

	pBand := *in.ref.Price/1e3/in.etaC - float64(in.pa)
	if pBand <= 0 {
		// even the reference does not beat what stored energy is already worth
		band.Status = bandStatusNothingCheap
		return band
	}

	band.Status = bandStatusWaitingCheap
	band.active = true
	band.sBand = in.capacity * float64(top) * 10
	band.pBand = pBand
	band.minRun = float64(in.minRun) * 60
	band.refuseStorage = !in.fromBattery && in.soc >= float64(in.minSoc)
	return band
}

// trackCheapBandRun records the charging run of loadpoint id into runs, keeping the start
// of a run already observed, and returns it; nil while the loadpoint is not charging.
func (site *Site) trackCheapBandRun(id int, lp loadpoint.API, runs map[int]cheapBandRun, now time.Time) *cheapBandRun {
	if lp.GetStatus() != api.StatusC {
		return nil
	}
	run, ok := site.cheapBandRuns[id]
	if !ok {
		run = cheapBandRun{start: now, fromFloor: lp.GetSoc() < float64(lp.EffectiveMinSoc())}
	}
	runs[id] = run
	return &run
}

// setCheapBandRun tells the solver how long the vehicle's run in progress has lasted. Below
// minRun the companion makes it continue at least at minimum power, a hard constraint, so
// that is only asked when it fits below the ceiling; otherwise, and for the floor's own run,
// the run counts as complete and may stop at any time.
func setCheapBandRun(band *energyBand, run *cheapBandRun, bat optimizer.BatteryConfig, now time.Time) {
	if band == nil || !band.active || band.minRun <= 0 || run == nil {
		return
	}
	// observed charging this cycle is a run in progress, however short
	elapsed := max(now.Sub(run.start).Seconds(), 1)
	// the companion holds whole slots: the rest can reach one slot further than its seconds
	if remaining := band.minRun - elapsed; remaining > 0 && !run.fromFloor &&
		float64(bat.SMax-bat.SInitial) >= eta*float64(bat.CMin)*(remaining+tariff.SlotDuration.Seconds())/3600+1 {
		band.elapsed = elapsed
		return
	}
	band.elapsed = band.minRun
}

// vehicleCheapBand returns the band of the vehicle at lp, nil if none is configured.
func (site *Site) vehicleCheapBand(lp loadpoint.API, name string, capacity float64, minImportPrice float32) *energyBand {
	if name == "" {
		return nil
	}
	v, err := site.Vehicles().ByName(name)
	if err != nil || v.GetCheapBandSoc() <= 0 {
		return nil
	}
	return cheapBand(cheapBandInput{
		capacity: capacity, soc: lp.GetSoc(),
		minSoc: lp.EffectiveMinSoc(), limitSoc: lp.EffectiveLimitSoc(),
		bandSoc: v.GetCheapBandSoc(), minRun: v.GetCheapBandMinRun(),
		mode: lp.GetMode(), fromBattery: site.energyInsights.Settings.CheapBandFromBattery,
		ref: site.cheapBandReference(site.energyInsights.Settings, time.Now()),
		pa:  terminalStorageValue(minImportPrice), etaC: eta,
	})
}

// applyCheapBand puts an active band on the solver battery.
func applyCheapBand(bat *energyBattery, band *energyBand) {
	if band == nil || !band.active {
		return
	}
	sBand, pBand := band.sBand, band.pBand
	// the companion requires s_band <= capacity, which is s_max when no capacity is sent
	ceiling := bat.SMax
	if bat.SCapacity > 0 {
		ceiling = bat.SCapacity
	}
	sBand = min(sBand, float64(ceiling))
	bat.SBand, bat.PBand = &sBand, &pBand
	// the companion uses the c_min on/off binaries to know when the car charges
	if band.refuseStorage && bat.CMin > 0 {
		feed := false
		bat.BandStorageFeed = &feed
	}
	if band.minRun > 0 && bat.CMin > 0 {
		minRun := band.minRun
		bat.CMinRun = &minRun
		if band.elapsed > 0 {
			elapsed := band.elapsed
			bat.CRunElapsed = &elapsed
		}
	}
}

// explainCheapBand sets the band status from the solved plan of battery i.
func explainCheapBand(band *energyBand, req energyRequest, res optimizer.OptimizationResult, timestamps []time.Time, i int) {
	if band == nil || !band.active {
		return
	}
	// the request's SMin is the vehicle's minSoc, unclamped (see loadpointRequest)
	if bat := req.Batteries[i]; bat.SInitial < bat.SMin {
		band.Status = bandStatusBelowMinimum
		return
	}
	charge := res.Batteries[i].ChargingPower
	for t := range charge {
		if charge[t] <= cheapBandChargeMinWh {
			continue
		}
		if t == 0 {
			band.Status = bandStatusCharging
			return
		}
		price := float64(req.TimeSeries.PN[t]) * 1e3
		band.NextPrice = &price
		if t < len(timestamps) {
			start := timestamps[t]
			band.NextStart = &start
		}
		band.Status = bandStatusWaitingCheap
		if energyGridImport(req.OptimizationInput, res, t) < charge[t]/2 {
			band.Status = bandStatusWaitingSolar
		}
		return
	}
	band.Status = bandStatusNothingCheap
}

// cheapBandFullPower reports whether a band vehicle's partial first-slot charge runs at
// full power under robust dispatch: in a slot priced at or below the reference any
// power is worth it, and the band top ends it on the next control cycle. A run held to
// its minimum keeps its planned (minimum) power instead: the hold is not price-bounded,
// so it must not be widened to full power.
func cheapBandFullPower(band *energyBand, price float32) bool {
	if band == nil || !band.active || band.Reference.Price == nil {
		return false
	}
	return float64(price)*1e3 <= *band.Reference.Price
}

// cheapBandValue is the band's contribution to the solver objective between initial
// and final stored energy (Wh): the solver maximizes b <= min(final, s_band).
func cheapBandValue(bat energyBattery, final float32) float64 {
	if bat.SBand == nil || bat.PBand == nil {
		return 0
	}
	return *bat.PBand * (min(float64(final), *bat.SBand) - min(float64(bat.SInitial), *bat.SBand))
}
