package core

import (
	"encoding/json"
	"math"
	"slices"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/jinzhu/now"
	"github.com/samber/lo"
)

// forecastSeries and solarDetails implement BytesMarshaler so MQTT publishes one
// json message per forecast key instead of decomposing every slot into its own
// topic (several thousand messages per update).
type forecastSeries [][]float64

var _ api.BytesMarshaler = (*forecastSeries)(nil)

func (s forecastSeries) MarshalBytes() ([]byte, error) {
	return json.Marshal(s)
}

type solarDetails struct {
	Scale            float64      `json:"scale"`                // trailing percentile solar scale factor, 1 if unscaled
	Today            dailyDetails `json:"today"`                // tomorrow
	Tomorrow         dailyDetails `json:"tomorrow"`             // tomorrow
	DayAfterTomorrow dailyDetails `json:"dayAfterTomorrow"`     // day after tomorrow
	Timeseries       timeseries   `json:"timeseries,omitempty"` // timeseries of forecasted energy
}

var _ api.BytesMarshaler = (*solarDetails)(nil)

func (d solarDetails) MarshalBytes() ([]byte, error) {
	return json.Marshal(d)
}

type dailyDetails struct {
	Yield    float64 `json:"energy"`
	Complete bool    `json:"complete"`
}

// forecastRates publishes rates as [start, end, value] with the timestamps in
// unix seconds. The forecast is the largest payload evcc sends and RFC3339
// timestamps are two thirds of it.
func forecastRates(rr api.Rates) forecastSeries {
	// keep nil for empty rates: shards are published without omitempty
	if len(rr) == 0 {
		return nil
	}

	return lo.Map(rr, func(r api.Rate, _ int) []float64 {
		return []float64{float64(r.Start.Unix()), float64(r.End.Unix()), r.Value}
	})
}

// greenShare returns
//   - the current green share, calculated for the part of the consumption between powerFrom and powerTo
//     the consumption below powerFrom will get the available green power first
func (site *Site) greenShare(powerFrom float64, powerTo float64) float64 {
	state := site.state()

	greenPower := math.Max(0, state.pvPower) + math.Max(0, state.battery.Power)
	greenPowerAvailable := math.Max(0, greenPower-powerFrom)

	power := powerTo - powerFrom
	share := math.Min(greenPowerAvailable, power) / power

	if math.IsNaN(share) {
		if greenPowerAvailable > 0 {
			share = 1
		} else {
			share = 0
		}
	}

	return share
}

// effectivePrice calculates the real energy price based on self-produced and grid-imported energy.
func (site *Site) effectivePrice(greenShare float64) *float64 {
	if grid, err := tariff.Now(site.GetTariff(api.TariffUsageGrid)); err == nil {
		feedin, err := tariff.Now(site.GetTariff(api.TariffUsageFeedIn))
		if err != nil {
			feedin = 0
		}
		effPrice := grid*(1-greenShare) + feedin*greenShare
		return &effPrice
	}
	return nil
}

// effectiveCo2 calculates the amount of emitted co2 based on self-produced and grid-imported energy.
func (site *Site) effectiveCo2(greenShare float64) *float64 {
	if co2, err := tariff.Now(site.GetTariff(api.TariffUsageCo2)); err == nil {
		effCo2 := co2 * (1 - greenShare)
		return &effCo2
	}
	return nil
}

func (site *Site) publishTariffs(greenShareHome float64, greenShareLoadpoints float64) {
	site.publish(keys.GreenShareHome, greenShareHome)
	site.publish(keys.GreenShareLoadpoints, greenShareLoadpoints)

	if v, err := tariff.Now(site.GetTariff(api.TariffUsageGrid)); err == nil {
		site.publish(keys.TariffGrid, v)
	}
	if v, err := tariff.Now(site.GetTariff(api.TariffUsageFeedIn)); err == nil {
		site.publish(keys.TariffFeedIn, v)
	}
	if v, err := tariff.Now(site.GetTariff(api.TariffUsageCo2)); err == nil {
		site.publish(keys.TariffCo2, v)
	}
	if v, err := tariff.Now(site.GetTariff(api.TariffUsageSolar)); err == nil {
		site.publish(keys.TariffSolar, v)
	}
	if v, err := tariff.Now(site.GetTariff(api.TariffUsageTemperature)); err == nil {
		site.publish(keys.TariffTemperature, v)
	}
	if v := site.effectivePrice(greenShareHome); v != nil {
		site.publish(keys.TariffPriceHome, v)
	}
	if v := site.effectiveCo2(greenShareHome); v != nil {
		site.publish(keys.TariffCo2Home, v)
	}
	if v := site.effectivePrice(greenShareLoadpoints); v != nil {
		site.publish(keys.TariffPriceLoadpoints, v)
	}
	if v := site.effectiveCo2(greenShareLoadpoints); v != nil {
		site.publish(keys.TariffCo2Loadpoints, v)
	}

	fc := struct {
		Co2         forecastSeries `json:"co2,omitempty"`
		FeedIn      forecastSeries `json:"feedin,omitempty"`
		Grid        forecastSeries `json:"grid,omitempty"`
		Planner     forecastSeries `json:"planner,omitempty"`
		Solar       *solarDetails  `json:"solar,omitempty"`
		Temperature forecastSeries `json:"temperature,omitempty"`
	}{
		Co2:         forecastRates(tariff.Rates(site.GetTariff(api.TariffUsageCo2))),
		FeedIn:      forecastRates(tariff.Rates(site.GetTariff(api.TariffUsageFeedIn))),
		Planner:     forecastRates(tariff.Rates(site.GetTariff(api.TariffUsagePlanner))),
		Grid:        forecastRates(tariff.Rates(site.GetTariff(api.TariffUsageGrid))),
		Temperature: forecastRates(tariff.Rates(site.GetTariff(api.TariffUsageTemperature))),
	}

	// calculate adjusted solar rates
	if solar := tariff.Rates(site.GetTariff(api.TariffUsageSolar)); len(solar) > 0 {
		fc.Solar = new(site.solarDetails(solar))
	}

	site.publish(keys.Forecast, util.NewSharder(keys.Forecast, fc))

	site.persistTariffs()
}

// persistTariffs stores tariff values once per 15min boundary. Like the meter
// collectors it is driven by the update loop, skipping the partial boot slot.
//
// It also re-attempts the immediately preceding slot. A usage whose tariff was
// unavailable at the moment that slot was written (a tariff device being recreated by
// a UI config edit, a provider request that failed) left a NULL behind that nothing
// ever came back for - metrics.PersistTariffs only ever fills NULLs and never
// overwrites a recorded value, so the retry cannot damage an observed reading, and it
// closes the hole while the rate is still readable for that slot. tariff.At returns an
// error for a slot outside the tariff's rate window, which leaves that usage nil
// rather than substituting a later price.
//
// It is not, however, free of risk. A static tariff's rates are regenerated from the
// CURRENT configuration, so tariff.At answers for a past instant with today's number:
// if the device was mid-recreation at the previous tick AND the edit that landed
// between the two ticks changed the rate, this writes the new rate into a slot that
// was billed at the old one, permanently and indistinguishably from an observed
// reading. A static tariff's past rates are unverifiable by construction; this writes
// exactly one slot's worth of that assumption, and only when the update loop is
// running on schedule - the retry is bounded to slot-1 and skipped entirely when the
// last persisted slot is older than that, so a stalled loop cannot widen the window
// it asserts over.
func (site *Site) persistTariffs() {
	slot := time.Now().Truncate(tariff.SlotDuration)

	last := site.tariffSlot
	site.tariffSlot = slot

	// skip repeat ticks within the slot and the partial boot slot
	if last.IsZero() || !slot.After(last) {
		return
	}

	persist := func(ts time.Time) {
		value := func(u api.TariffUsage) *float64 {
			if r, err := tariff.At(site.GetTariff(u), ts); err == nil {
				return &r.Value
			}
			return nil
		}

		if err := metrics.PersistTariffs(ts,
			value(api.TariffUsageGrid),
			value(api.TariffUsageFeedIn),
			value(api.TariffUsageCo2),
			value(api.TariffUsageTemperature),
		); err != nil {
			site.log.ERROR.Printf("persist tariffs: %v", err)
		}
	}

	if prev := slot.Add(-tariff.SlotDuration); last.Equal(prev) {
		persist(prev)
	}
	persist(slot)
}

// forecastSlotEnergy is the energy expected in the slot covering now, integrated
// the same way as the published forecast so the persisted history matches the
// curve the UI draws. Beyond the forecast horizon it is zero.
func forecastSlotEnergy(solar api.Rates, now time.Time) float64 {
	slot := now.Truncate(tariff.SlotDuration)
	return solarEnergy(solar, slot, slot.Add(tariff.SlotDuration)) / 1e3
}

// archiveForecastSlot persists the solar forecast's predicted energy at a small
// set of lead times ahead of now, once per 15min boundary - same cadence and
// partial-boot-slot skip as persistTariffs. Unlike the Forecast collector fed by
// forecastSlotEnergy above, which only ever records the near-zero-lead nowcast
// for the slot containing "now", this captures what the forecast said about a
// slot well before it arrived, so forecast bias can later be measured per lead
// time instead of being conflated into one number.
func (site *Site) archiveForecastSlot(solar api.Rates) {
	slot := time.Now().Truncate(tariff.SlotDuration)

	last := site.forecastArchiveSlot
	site.forecastArchiveSlot = slot

	// skip repeat ticks within the slot and the partial boot slot
	if last.IsZero() || !slot.After(last) {
		return
	}

	if err := metrics.ArchiveForecastSample(slot, func(from, to time.Time) (float64, bool) {
		// target slot outside the forecast's own horizon: solarEnergy would return 0 for
		// "no data" indistinguishably from a real zero, so report not-ok instead
		if len(solar) == 0 || from.Before(solar[0].Start) || to.After(solar[len(solar)-1].End) {
			return 0, false
		}
		return solarEnergy(solar, from, to), true
	}); err != nil {
		site.log.ERROR.Printf("archive solar forecast: %v", err)
	}
}

func (site *Site) solarDetails(solar api.Rates) solarDetails {
	res := solarDetails{
		Timeseries: solarTimeseries(solar),
	}

	last := solar[len(solar)-1].Start

	bod := now.BeginningOfDay()
	eod := bod.AddDate(0, 0, 1)
	eot := eod.AddDate(0, 0, 1)

	remainingToday := solarEnergy(solar, time.Now(), eod)
	tomorrow := solarEnergy(solar, eod, eot)
	dayAfterTomorrow := solarEnergy(solar, eot, eot.AddDate(0, 0, 1))

	res.Today = dailyDetails{
		Yield:    remainingToday,
		Complete: !last.Before(eod),
	}
	res.Tomorrow = dailyDetails{
		Yield:    tomorrow,
		Complete: !last.Before(eot),
	}
	res.DayAfterTomorrow = dailyDetails{
		Yield:    dayAfterTomorrow,
		Complete: !last.Before(eot.AddDate(0, 0, 1)),
	}

	if err := site.collectors[metrics.Forecast].SetEnergy(forecastSlotEnergy(solar, time.Now())); err != nil {
		site.log.ERROR.Printf("solar forecast collector: %v", err)
	}

	site.archiveForecastSlot(solar)

	if r, err := tariff.At(site.GetTariff(api.TariffUsageTemperature), time.Now()); err == nil {
		if err := site.collectors[metrics.Temperature].SetSocTemp(r.Value, true); err != nil {
			site.log.ERROR.Printf("temperature collector soc_temp: %v", err)
		}
	}

	res.Scale = site.solarScale()

	return res
}

// effectiveSolarScale returns the solar forecast scale used to adjust the
// optimizer's solar input if forecast adjustment is enabled, 1 otherwise.
func (site *Site) effectiveSolarScale() float64 {
	if !site.GetSolarAdjusted() {
		return 1
	}
	return site.solarScale()
}

// effectiveSolarScaleAt returns a function giving the solar forecast scale to
// apply to a slot a given duration ahead of now, honoring GetSolarAdjusted():
// disabled entirely returns a flat scale of 1 for every lead, matching
// effectiveSolarScale. nowcast is the already-computed effectiveSolarScale()
// result, reused as the lead=0 anchor instead of recomputing it.
func (site *Site) effectiveSolarScaleAt(nowcast float64) func(lead time.Duration) float64 {
	if !site.GetSolarAdjusted() {
		return func(time.Duration) float64 { return 1 }
	}
	return site.solarScaleAt(nowcast)
}

const (
	solarScaleWindow     = 30  // trailing window of days to consider
	solarScaleMinSamples = 14  // minimum daily ratios before applying a scale
	solarScalePercentile = 0.5 // percentile of the daily ratio distribution to use
	solarScaleMinEnergy  = 0.5 // kWh, skip days where either side is too small for a meaningful ratio
)

// solarScale computes a scale factor for the solar forecast by sorting the daily
// produced/forecasted solar ratio over a trailing window of completed days and
// picking the value at a configured percentile (window: solarScaleWindow,
// percentile: solarScalePercentile). This captures the installation's systematic
// bias (soiling, shading, model error) instead of a single day's weather noise.
// The current (partial) day is excluded; returns 1 when there is not enough history.
//
// Depends only on completed days, so it's cached instead of recomputed per run.
//
// The result only depends on completed days, so it cannot change within a day. It is
// cached accordingly instead of being recomputed on every optimizer run.
func (site *Site) solarScale() float64 {
	scale, err := site.solarScaleCached()
	if err != nil {
		return 1
	}
	return scale
}

// querySolarScale does the actual metrics query and percentile calculation
// for solarScale, given the current beginning-of-day boundary.
func (site *Site) querySolarScale(bod time.Time) (float64, error) {
	from := bod.AddDate(0, 0, -solarScaleWindow)
	series, err := metrics.QueryEnergy(from, time.Now(), "day", true)
	if err != nil {
		return 0, err
	}

	pv := make(map[string]float64, solarScaleWindow)
	fcst := make(map[string]float64, solarScaleWindow)
	for _, s := range series {
		var m map[string]float64
		switch s.Group {
		case metrics.PV:
			m = pv
		case metrics.Forecast:
			m = fcst
		default:
			continue
		}
		for _, d := range s.Data {
			m[d.Start.Format("2006-01-02")] = d.Energy
		}
	}

	today := bod.Format("2006-01-02")
	ratios := make([]float64, 0, len(fcst))
	for day, f := range fcst {
		// skip today (partial) and dark days where the ratio is noise. The threshold
		// applies to production as well: a near-zero yield against a healthy forecast
		// is a fault (snow, soiling, inverter or metering outage), not a bias that
		// should be projected onto the next solarScaleWindow days.
		if p := pv[day]; day != today && f > solarScaleMinEnergy && p > solarScaleMinEnergy {
			ratios = append(ratios, p/f)
		}
	}

	scale, ok := percentileOf(ratios, solarScalePercentile, solarScaleMinSamples)
	if !ok {
		return 1, nil
	}
	site.log.DEBUG.Printf("solar scale P%.0f over %d days = %.3f", solarScalePercentile*100, len(ratios), scale)
	return scale, nil
}

// leadTimeScaleWindow is the trailing history window for the per-lead-time solar
// scale. Shorter than solarScaleWindow: forecast_samples accrues roughly one row
// per 15-minute slot per lead time (see metrics.ArchiveForecastSample), so even a
// couple of weeks gives far more samples than the once-a-day nowcast ratio.
//
// leadTimeScaleMinSamples is correspondingly higher than solarScaleMinSamples -
// slot-level noise (passing clouds, a single misread) needs more samples to
// average out than day-level noise does.
const (
	leadTimeScaleWindow     = 14
	leadTimeScaleMinSamples = 50
)

// querySolarScaleByLead computes a solar forecast scale per archived lead time
// (metrics.ForecastLeadTimes): the same percentile-of-ratio approach as
// querySolarScale, but keyed by how far ahead of the slot the forecast was read
// instead of collapsing every lead time into the near-zero-lead nowcast. A lead
// time with too little history is simply absent from the result, so callers can
// fall back to the nowcast scale for that range instead of trusting a percentile
// computed from noise.
func (site *Site) querySolarScaleByLead() (map[int]float64, error) {
	from := time.Now().AddDate(0, 0, -leadTimeScaleWindow)

	rows, err := metrics.QueryLeadTimeSamples(from)
	if err != nil {
		return nil, err
	}

	ratios := make(map[int][]float64)
	for _, r := range rows {
		// same fault-vs-bias reasoning as querySolarScale: a near-zero actual
		// against a healthy forecast (or vice versa) is noise or a metering
		// fault, not a bias to project forward
		if r.Forecast > solarScaleMinEnergy && r.Actual > solarScaleMinEnergy {
			ratios[r.LeadMinutes] = append(ratios[r.LeadMinutes], r.Actual/r.Forecast)
		}
	}

	res := make(map[int]float64, len(ratios))
	for lead, rr := range ratios {
		if v, ok := percentileOf(rr, solarScalePercentile, leadTimeScaleMinSamples); ok {
			res[lead] = v
			site.log.DEBUG.Printf("solar scale P%.0f at lead %dmin over %d samples = %.3f", solarScalePercentile*100, lead, len(rr), v)
		}
	}

	return res, nil
}

// solarScaleAt returns a function giving the solar forecast scale to apply to a
// slot a given duration ahead of now. It is anchored at lead=0 with nowcast (the
// existing querySolarScale result, unaffected by this), plus any
// querySolarScaleByLead buckets that have enough history, linearly interpolated
// between neighboring anchors and clamped to the nearest anchor beyond the ends.
// With no per-lead history at all, lead=0 is the only anchor and every lead
// resolves to nowcast - identical to the flat scale used before this existed.
func (site *Site) solarScaleAt(nowcast float64) func(lead time.Duration) float64 {
	byLead, err := site.solarScaleByLeadCached()
	if err != nil {
		site.log.ERROR.Printf("solar scale by lead time: %v, falling back to nowcast scale", err)
		byLead = nil
	}

	type anchor struct {
		lead  time.Duration
		scale float64
	}

	anchors := []anchor{{0, nowcast}}
	for _, lead := range metrics.ForecastLeadTimes {
		if v, ok := byLead[int(lead.Minutes())]; ok {
			anchors = append(anchors, anchor{lead, v})
		}
	}

	return func(lead time.Duration) float64 {
		if lead <= anchors[0].lead {
			return anchors[0].scale
		}
		for i := 1; i < len(anchors); i++ {
			if lead <= anchors[i].lead {
				prev, next := anchors[i-1], anchors[i]
				w := float64(lead-prev.lead) / float64(next.lead-prev.lead)
				return prev.scale + w*(next.scale-prev.scale)
			}
		}
		return anchors[len(anchors)-1].scale
	}
}

// percentileOf returns the p-th percentile (0..1) of values by nearest-rank on the
// sorted series, or false when fewer than minSamples are present.
func percentileOf(values []float64, p float64, minSamples int) (float64, bool) {
	if len(values) < minSamples {
		return 0, false
	}
	s := slices.Clone(values)
	slices.Sort(s)
	return s[int(p*float64(len(s)-1))], true
}

func (site *Site) isDynamicTariff(usage api.TariffUsage) bool {
	tariff := site.GetTariff(usage)
	return tariff != nil && tariff.Type() != api.TariffTypePriceStatic
}
