package core

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/shading"
	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"github.com/jinzhu/now"
)

// reasons why no solar shading map is available
const (
	solarShadingNoCoordinates = "coordinates"
	solarShadingNoPV          = "noPv"
	solarShadingNoData        = "insufficientData"
	solarShadingError         = "error"
)

type solarShadingUnavailable struct {
	reason string
	err    error
}

func (e *solarShadingUnavailable) Error() string {
	if e.err != nil {
		return e.reason + ": " + e.err.Error()
	}
	return e.reason
}

func (e *solarShadingUnavailable) Unwrap() error { return e.err }

// solarShadingRetry bounds how often a failed computation is retried.
const solarShadingRetry = time.Hour

type solarShadingCache struct {
	mu       sync.Mutex
	cutoff   time.Time // beginning of the day the map was learned for
	computed time.Time
	m        *shading.Map
	err      error
	windows  []shading.Window
}

// solarShadingMap returns the map learned from the days completed before
// today, computing it at most once per day (and retrying a failure after
// solarShadingRetry).
func (site *Site) solarShadingMap() (*shading.Map, []shading.Window, time.Time, error) {
	c := &site.solarShading
	c.mu.Lock()
	defer c.mu.Unlock()

	cutoff := now.BeginningOfDay()
	if c.cutoff.Equal(cutoff) && (c.err == nil || time.Since(c.computed) < solarShadingRetry) {
		return c.m, c.windows, c.computed, c.err
	}

	c.cutoff, c.computed = cutoff, time.Now()
	c.m, c.windows, c.err = site.learnSolarShading(cutoff)
	if c.err != nil {
		site.log.DEBUG.Printf("solar shading unavailable: %v", c.err)
	} else {
		site.log.DEBUG.Printf("solar shading learned from %d slots on %d days", c.m.Samples, c.m.Days)
	}
	return c.m, c.windows, c.computed, c.err
}

func (site *Site) learnSolarShading(cutoff time.Time) (*shading.Map, []shading.Window, error) {
	cfg := shading.DefaultConfig
	windows := cfg.TrainingWindows(cutoff)

	loc, err := site.tariffs.SolarLocation()
	if err != nil {
		return nil, windows, &solarShadingUnavailable{solarShadingNoCoordinates, err}
	}
	if db.Instance == nil {
		return nil, windows, &solarShadingUnavailable{solarShadingError, errors.New("database offline")}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var samples []shading.Sample
	for _, w := range windows {
		rows, hasPV, err := metrics.QueryShadingSamples(ctx, metrics.ShadingLeadMinutes, w.From, w.To)
		if err != nil {
			return nil, windows, &solarShadingUnavailable{solarShadingError, err}
		}
		if !hasPV {
			return nil, windows, &solarShadingUnavailable{reason: solarShadingNoPV}
		}
		for _, r := range rows {
			samples = append(samples, shading.Sample{Slot: r.Slot, Forecast: r.Forecast, Measured: r.Measured})
		}
	}

	m := shading.Learn(samples, shading.Location{Lat: loc.Lat, Lon: loc.Lon}, cutoff, cfg)
	if !m.Learned() {
		return m, windows, &solarShadingUnavailable{reason: solarShadingNoData}
	}
	return m, windows, nil
}

// activeSolarShading returns the map when the setting is on and a map is
// available, nil otherwise. While it returns a map, the map replaces the daily
// solar scale for the optimizer, the scenarios, the Plan view and the
// published forecast. Off, it does not touch the database.
func (site *Site) activeSolarShading() *shading.Map {
	if !site.GetEnergyIntelligenceSettings().SolarShading {
		return nil
	}
	m, _, _, err := site.solarShadingMap()
	if err != nil {
		return nil
	}
	return m
}

// solarSpread is a slot's solar band relative to its corrected energy:
// low <= 1 <= high. The zero value means no band information.
type solarSpread struct{ low, high float64 }

// shadeAndPruneByLead is scaleAndPruneByLead with the shading map's per-slot
// factor. fallback gives the factor where the map has no evidence (the daily
// scale when solarAdjusted is on, else 1).
func shadeAndPruneByLead(rates api.Rates, now time.Time, m *shading.Map, fallback func(lead time.Duration) float64, maxLen int) ([]float32, []solarSpread) {
	res := make([]float32, 0, maxLen)
	spread := make([]solarSpread, 0, maxLen)

	for _, slot := range rates {
		c := m.Factor(slot.Start, fallback(slot.Start.Sub(now)))
		res = append(res, float32(slot.Value*c.Factor))
		s := solarSpread{1, 1}
		if c.Factor > 0 {
			s = solarSpread{c.Low / c.Factor, c.High / c.Factor}
		}
		spread = append(spread, s)
		if len(res) >= maxLen {
			break
		}
	}

	return res, spread
}

// shadedSolarRates applies the map to forecast power samples. A sample is the
// power at its start instant, so the factor is the one for the slot centred
// on it.
func shadedSolarRates(rates api.Rates, now time.Time, m *shading.Map, fallback func(lead time.Duration) float64) api.Rates {
	res := make(api.Rates, len(rates))
	for i, r := range rates {
		c := m.Factor(r.Start.Add(-shading.SlotDuration/2), fallback(r.Start.Sub(now)))
		res[i] = r
		res[i].Value = r.Value * c.Factor
	}
	return res
}

// SolarShadingStatus is the read-only view of the learned map for the API.
type SolarShadingStatus struct {
	Enabled     bool               `json:"enabled"`
	Active      bool               `json:"active"`
	Available   bool               `json:"available"`
	Reason      string             `json:"reason,omitempty"`
	Error       string             `json:"error,omitempty"`
	Location    *shading.Location  `json:"location,omitempty"`
	LeadMinutes int                `json:"leadMinutes"`
	Windows     []shading.Window   `json:"trainingWindows"`
	Updated     *time.Time         `json:"updated,omitempty"`
	Days        int                `json:"days"`
	Samples     int                `json:"samples"`
	Fallback    string             `json:"fallback"`       // what unlearned bins use: daily or none
	FallbackNow float64            `json:"fallbackFactor"` // its current value
	Parameters  SolarShadingParams `json:"parameters"`
	Bins        []shading.Bin      `json:"bins"`
}

// SolarShadingParams are the learning parameters in effect.
type SolarShadingParams struct {
	AzimuthStep   float64 `json:"azimuthStep"`
	ElevationStep float64 `json:"elevationStep"`
	HalfLifeDays  float64 `json:"halfLifeDays"`
	WindowDays    float64 `json:"windowDays"`
	MinFactor     float64 `json:"minFactor"`
	MaxFactor     float64 `json:"maxFactor"`
	MinSlots      int     `json:"minSlots"`
}

// SolarShading reports the learned map and whether it is in effect. The
// map is learned for display even while the setting is off, so it can be
// reviewed before enabling it.
func (site *Site) SolarShading() SolarShadingStatus {
	cfg := shading.DefaultConfig
	res := SolarShadingStatus{
		Enabled:     site.GetEnergyIntelligenceSettings().SolarShading,
		LeadMinutes: metrics.ShadingLeadMinutes,
		Fallback:    "none",
		FallbackNow: 1,
		Parameters: SolarShadingParams{
			AzimuthStep:   cfg.AzimuthStep,
			ElevationStep: cfg.ElevationStep,
			HalfLifeDays:  cfg.HalfLife.Hours() / 24,
			WindowDays:    cfg.Window.Hours() / 24,
			MinFactor:     cfg.MinFactor,
			MaxFactor:     cfg.MaxFactor,
			MinSlots:      cfg.MinSlots,
		},
		Bins: []shading.Bin{},
	}
	if site.GetSolarAdjusted() {
		res.Fallback, res.FallbackNow = "daily", site.solarScale()
	}

	m, windows, updated, err := site.solarShadingMap()
	res.Windows = windows
	if !updated.IsZero() {
		res.Updated = &updated
	}
	if m != nil {
		res.Location = &m.Location
		res.Days, res.Samples = m.Days, m.Samples
		if bins := m.Bins(); bins != nil {
			res.Bins = bins
		}
	}
	if err != nil {
		res.Reason = solarShadingError
		if u, ok := errors.AsType[*solarShadingUnavailable](err); ok {
			res.Reason = u.reason
			if u.err != nil {
				res.Error = u.err.Error()
			}
		}
		return res
	}
	res.Available = true
	res.Active = res.Enabled
	return res
}

// correctedSolar is the published forecast with the shading map applied, only
// present while the map is active.
type correctedSolar struct {
	Today            dailyDetails `json:"today"`
	Tomorrow         dailyDetails `json:"tomorrow"`
	DayAfterTomorrow dailyDetails `json:"dayAfterTomorrow"`
	Timeseries       timeseries   `json:"timeseries,omitempty"`
}

func (site *Site) correctedSolarDetails(solar api.Rates, raw solarDetails) *correctedSolar {
	m := site.activeSolarShading()
	if m == nil {
		return nil
	}
	ts := time.Now()
	shaded := shadedSolarRates(solar, ts, m, site.effectiveSolarScaleAt(site.effectiveSolarScale()))

	bod := now.BeginningOfDay()
	eod := bod.AddDate(0, 0, 1)
	eot := eod.AddDate(0, 0, 1)
	return &correctedSolar{
		Today:            dailyDetails{Yield: solarEnergy(shaded, ts, eod), Complete: raw.Today.Complete},
		Tomorrow:         dailyDetails{Yield: solarEnergy(shaded, eod, eot), Complete: raw.Tomorrow.Complete},
		DayAfterTomorrow: dailyDetails{Yield: solarEnergy(shaded, eot, eot.AddDate(0, 0, 1)), Complete: raw.DayAfterTomorrow.Complete},
		Timeseries:       solarTimeseries(shaded),
	}
}

// shadedSolarSlots builds the optimizer's solar series with the shading map
// and the band multipliers for each slot. As in the daily-scale path, the
// first slots are blended toward the last completed slot's measurement: the
// ratio of measured PV to the raw forecast corrected by that slot's own
// factor carries the current weather into the next slots, while the map keeps
// supplying the shape.
func (site *Site) shadedSolarSlots(solarEnergy api.Rates, now time.Time, m *shading.Map, fallback func(lead time.Duration) float64, minLen int, firstSlotDuration time.Duration) ([]float32, []solarSpread) {
	ftSlots, spread := shadeAndPruneByLead(solarEnergy, now, m, fallback, minLen)

	if pv, fcstRaw := site.measuredSlotEnergy(site.Meters.PVMetersRef...), site.measuredSlotEnergy(metrics.Forecast); pv > 0 && fcstRaw > 0 {
		prev := now.Truncate(tariff.SlotDuration).Add(-tariff.SlotDuration)
		if f := m.Factor(prev, fallback(prev.Sub(now))).Factor; f > 0 {
			ratio := pv / (fcstRaw * f)
			orig := slices.Clone(ftSlots[:min(optimizerDecaySlots, len(ftSlots))])
			blendScaleByLead(ftSlots, solarEnergy, now, func(time.Duration) float64 { return ratio }, optimizerDecaySlots)
			site.log.DEBUG.Printf("optimizer: shaded pv slots updated with measured %.0fWh vs corrected forecast %.0fWh: %.0f -> %.0f", pv, fcstRaw*f, orig, ftSlots[:len(orig)])
		}
	}

	return prorate(ftSlots, firstSlotDuration), spread
}

// widenSolarBand widens a slot's solar range by the shading map's spread for
// that slot. It never narrows the range it is given.
func widenSolarBand(solar, low, high float64, s solarSpread) (float64, float64) {
	if s.low == 0 && s.high == 0 {
		return low, high
	}
	return max(0, min(low, solar*s.low)), max(high, solar*s.high)
}
