package core

import (
	"errors"
	"math"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
)

// Heat-pump split (ADR-015 amendment): the household forecast as the profile of the
// household energy without the heat pump plus a heat-pump term that follows the forecast
// outdoor temperature. "off" does nothing, "shadow" archives the forecast as variant
// hp-split without using it, "on" plans with it while its gates are open and falls back
// to the heating-degree forecast otherwise.
const (
	heatPumpOff    = "off"
	heatPumpShadow = "shadow"
	heatPumpOn     = "on"

	heatPumpVariant = "hp-split"
	profileVariant  = "profile" // the uncorrected profile, the baseline hp-split is scored against
)

// heatPumpModelMode returns the effective mode, shadow for settings saved before the setting
// existed.
func heatPumpModelMode(setting string) string {
	if setting == "" {
		return heatPumpShadow
	}
	return setting
}

const (
	heatPumpMinDays      = 14  // valid days in the fit
	heatPumpMinHDDRange  = 3.0 // K of daily heating-degree spread in the fit
	heatPumpMinR         = 0.6 // Pearson r of daily heat-pump energy and heating-degree
	heatPumpMinSlope     = 0.3 // kWh per K and day
	heatPumpMaxSlope     = 3.0
	heatPumpRecentDays   = 90 // the fit uses only the last heatPumpRecentDays ...
	heatPumpRecentMin    = 60 // ... once this many valid days lie in them
	heatPumpShapeDays    = 30
	heatPumpShapeMinDays = 7
	heatPumpSigmaK       = 1.5 // temperature error the heat-pump band covers each side
	heatPumpMaxMeterAge  = time.Hour
	heatPumpMaxDropped   = 0.1 // share of paired profile slots with a negative residual
	heatPumpConstantSlot = 4   // forecast slots needed to call a flat 24 h forecast stale
	heatPumpRetry        = time.Hour

	// a forecast colder than anything the fit saw is not extrapolated further than this
	heatPumpExtrapolationK = 3.0 // K beyond the fit's largest daily heating-degree
	heatPumpDailyCapShare  = 1.5 // of the fit's largest daily energy
)

// gate reasons, reported in the status
const (
	heatPumpNoMeter          = "no_meter"
	heatPumpFewDays          = "few_days"
	heatPumpLowHDDRange      = "low_hdd_range"
	heatPumpFitPoor          = "fit_poor"
	heatPumpSlopeOutOfBounds = "slope_out_of_bounds"
	heatPumpTemperatureStale = "temperature_stale"
	heatPumpMeterIncomplete  = "hp_meter_incomplete"
	heatPumpNegativeResidual = "negative_residual"
	heatPumpProfile          = "profile_unavailable"
	heatPumpError            = "error"
	heatPumpPending          = "not_run_yet" // status only: the daily fit is open, no planning run has checked the rest
)

// heatPumpDay is one complete local date of the fit: the heat pump's energy (kWh) and the
// mean of the slots' heating-degree (K), the same definition the forecast uses.
type heatPumpDay struct {
	Date   time.Time
	Energy float64
	HDD    float64
}

type heatPumpFit struct {
	A, B, R  float64 // kWh per day, kWh per K and day, Pearson r
	Days     int     // days in the fit
	HDDRange float64
	Valid    int     // valid days available, before the recent-window choice
	MaxHDD   float64 // largest daily heating-degree in the fit
	MaxDaily float64 // largest daily energy in the fit (kWh)
}

// hddClamp is the heating-degree the forecast may extrapolate to: the fit's largest plus
// heatPumpExtrapolationK. dailyCap bounds a forecast day's energy at heatPumpDailyCapShare
// times the fit's largest day.
func (f heatPumpFit) hddClamp() float64 { return f.MaxHDD + heatPumpExtrapolationK }
func (f heatPumpFit) dailyCap() float64 { return heatPumpDailyCapShare * f.MaxDaily }

// heatPumpDays builds the daily series: a date is valid when it ends before today, at least
// heatingDegreeDayFill of its slots have a clean heat-pump reading (real slots, else the
// backfilled ones) and a temperature, and the temperature is not constant. Flagged slots are
// unknown, never zero. Energy is scaled to the date's length like heatingDegreeDays.
func heatPumpDays(rows []metrics.HeatPumpRow, backfill []metrics.SlotValue, temps map[int64]float64, today time.Time, loc *time.Location) []heatPumpDay {
	type sum struct {
		n, length int
		sum       float64
	}
	type temp struct {
		n        int
		hdd      float64
		min, max float64
	}
	date := func(ts int64) time.Time {
		t := time.Unix(ts, 0).In(loc)
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}
	add := func(m map[time.Time]*sum, ts int64, v float64) {
		d := date(ts)
		if m[d] == nil {
			m[d] = &sum{length: int(d.AddDate(0, 0, 1).Sub(d) / tariff.SlotDuration)}
		}
		m[d].n++
		m[d].sum += v
	}

	measured, filled := map[time.Time]*sum{}, map[time.Time]*sum{}
	for _, r := range rows {
		if r.Clean() {
			add(measured, r.Ts, r.Energy)
		}
	}
	for _, b := range backfill {
		if !math.IsNaN(b.Value) && !math.IsInf(b.Value, 0) && b.Value >= 0 {
			add(filled, b.Ts, b.Value)
		}
	}
	temperature := map[time.Time]*temp{}
	stamps := make([]int64, 0, len(temps))
	for ts := range temps {
		stamps = append(stamps, ts)
	}
	slices.Sort(stamps) // a fixed summation order
	for _, ts := range stamps {
		v := temps[ts]
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		d := date(ts)
		t := temperature[d]
		if t == nil {
			t = &temp{min: v, max: v}
			temperature[d] = t
		}
		t.n++
		t.hdd += heatingDegree(v)
		t.min, t.max = min(t.min, v), max(t.max, v)
	}

	complete := func(s *sum) bool {
		return s != nil && float64(s.n) >= heatingDegreeDayFill*float64(s.length)
	}
	var res []heatPumpDay
	for d, t := range temperature {
		if !d.Before(today) || t.min == t.max {
			continue
		}
		s := measured[d]
		if !complete(s) {
			s = filled[d]
		}
		if !complete(s) || float64(t.n) < heatingDegreeDayFill*float64(s.length) {
			continue
		}
		res = append(res, heatPumpDay{Date: d, Energy: s.sum * float64(s.length) / float64(s.n), HDD: t.hdd / float64(t.n)})
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Date.Before(res[j].Date) })
	return res
}

// fitHeatPump fits daily energy on daily heating-degree by ordinary least squares over all
// valid days, or only the last heatPumpRecentDays once heatPumpRecentMin of them qualify.
// reason is the first shut gate; the fit is filled as far as it could be computed.
func fitHeatPump(days []heatPumpDay, today time.Time) (heatPumpFit, string) {
	fit := heatPumpFit{Valid: len(days)}
	use := days
	var recent []heatPumpDay
	for _, d := range days {
		if !d.Date.Before(today.AddDate(0, 0, -heatPumpRecentDays)) {
			recent = append(recent, d)
		}
	}
	if len(recent) >= heatPumpRecentMin && hddRange(recent) >= heatPumpMinHDDRange {
		use = recent
	}
	fit.Days = len(use)
	if len(use) == 0 {
		return fit, heatPumpFewDays
	}
	fit.HDDRange = hddRange(use)
	for _, d := range use {
		fit.MaxHDD, fit.MaxDaily = max(fit.MaxHDD, d.HDD), max(fit.MaxDaily, d.Energy)
	}

	x, y := make([]float64, len(use)), make([]float64, len(use))
	var mx, my float64
	for i, d := range use {
		x[i], y[i] = d.HDD, d.Energy
		mx += d.HDD
		my += d.Energy
	}
	mx /= float64(len(use))
	my /= float64(len(use))
	var sxy, sxx float64
	for i := range x {
		sxy += (x[i] - mx) * (y[i] - my)
		sxx += (x[i] - mx) * (x[i] - mx)
	}
	if sxx > 0 {
		fit.B = sxy / sxx
		fit.A = my - fit.B*mx
	}
	if r, ok := pearson(x, y); ok {
		fit.R = r
	}

	switch {
	case fit.Days < heatPumpMinDays:
		return fit, heatPumpFewDays
	case fit.HDDRange < heatPumpMinHDDRange:
		return fit, heatPumpLowHDDRange
	case fit.R < heatPumpMinR:
		return fit, heatPumpFitPoor
	case fit.B < heatPumpMinSlope || fit.B > heatPumpMaxSlope:
		return fit, heatPumpSlopeOutOfBounds
	}
	return fit, ""
}

func hddRange(days []heatPumpDay) float64 {
	if len(days) == 0 {
		return 0
	}
	lo, hi := days[0].HDD, days[0].HDD
	for _, d := range days {
		lo, hi = min(lo, d.HDD), max(hi, d.HDD)
	}
	return hi - lo
}

// slotIndex is the local time-of-day bucket of a slot, as the profile indexes it.
func slotIndex(t time.Time) int { return t.Hour()*4 + t.Minute()/15 }

// heatPumpShape learns the heat pump's share per time-of-day slot from the real slots of the
// complete days of the last heatPumpShapeDays before today, normalised to 1. Backfilled
// hours never enter it. Without heatPumpShapeMinDays such days, or a slot of the day nobody
// observed, it is uniform. Daylight-saving days contribute the slots they have.
func heatPumpShape(rows []metrics.HeatPumpRow, today time.Time, loc *time.Location) (shape [96]float64, days int, learned bool) {
	type day struct {
		n, length int
		slots     []metrics.HeatPumpRow
	}
	byDate := map[time.Time]*day{}
	from := today.AddDate(0, 0, -heatPumpShapeDays)
	for _, r := range rows {
		t := time.Unix(r.Ts, 0).In(loc)
		d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		if !r.Clean() || d.Before(from) || !d.Before(today) {
			continue
		}
		if byDate[d] == nil {
			byDate[d] = &day{length: int(d.AddDate(0, 0, 1).Sub(d) / tariff.SlotDuration)}
		}
		byDate[d].n++
		byDate[d].slots = append(byDate[d].slots, r)
	}

	var sum [96]float64
	var count [96]int
	for _, d := range byDate {
		if float64(d.n) < heatingDegreeDayFill*float64(d.length) {
			continue
		}
		days++
		for _, r := range d.slots {
			i := slotIndex(time.Unix(r.Ts, 0).In(loc))
			sum[i] += r.Energy
			count[i]++
		}
	}

	uniform := func() {
		for i := range shape {
			shape[i] = 1.0 / 96
		}
	}
	if days < heatPumpShapeMinDays || slices.Contains(count[:], 0) {
		uniform()
		return shape, days, false
	}
	var total float64
	for i := range shape {
		shape[i] = sum[i] / float64(count[i])
		total += shape[i]
	}
	if total <= 0 {
		uniform()
		return shape, days, false
	}
	for i := range shape {
		shape[i] /= total
	}
	return shape, days, true
}

// heatPumpAnalysis is the daily part of the model: the fit and the shape, valid for the local
// day it was computed on.
type heatPumpAnalysis struct {
	Meter        string
	Entity       int
	Day          time.Time
	Computed     time.Time
	Fit          heatPumpFit
	Shape        [96]float64
	ShapeDays    int
	ShapeLearned bool
	Open         bool
	Reason       string
}

func analyzeHeatPump(rows []metrics.HeatPumpRow, backfill []metrics.SlotValue, temps map[int64]float64, today time.Time, loc *time.Location) heatPumpAnalysis {
	res := heatPumpAnalysis{Day: today}
	res.Fit, res.Reason = fitHeatPump(heatPumpDays(rows, backfill, temps, today, loc), today)
	res.Shape, res.ShapeDays, res.ShapeLearned = heatPumpShape(rows, today, loc)
	res.Open = res.Reason == ""
	return res
}

// heatPumpDailyCache holds the analysis of the current local day.
type heatPumpDailyCache struct {
	mu  sync.Mutex
	day time.Time
	ref string
	res *heatPumpAnalysis
	err error
	at  time.Time
}

// heatPumpDailyAnalysis returns the analysis of ref for the local day of now, computed at the
// first call of the day, retried after heatPumpRetry on failure and for a meter that does not
// exist (yet).
func (site *Site) heatPumpDailyAnalysis(ref string, now time.Time) (*heatPumpAnalysis, error) {
	c := &site.heatPumpDailyState
	c.mu.Lock()
	defer c.mu.Unlock()

	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	retry := c.err != nil || (c.res != nil && c.res.Reason == heatPumpNoMeter)
	if c.res != nil && c.day.Equal(day) && c.ref == ref && (!retry || time.Since(c.at) < heatPumpRetry) {
		return c.res, c.err
	}

	c.day, c.ref, c.at, c.err = day, ref, time.Now(), nil
	c.res = &heatPumpAnalysis{Meter: ref, Day: day, Computed: c.at, Reason: heatPumpError}
	if db.Instance == nil {
		c.err = errors.New("database offline")
		return c.res, c.err
	}
	id, ok, err := metrics.HeatPumpEntity(ref)
	if err != nil {
		c.err = err
		return c.res, c.err
	}
	if !ok {
		c.res = &heatPumpAnalysis{Meter: ref, Day: day, Computed: c.at, Reason: heatPumpNoMeter}
		return c.res, nil
	}
	epoch := time.Unix(0, 0)
	rows, err := metrics.QueryHeatPumpRows(id, epoch)
	if err == nil {
		var backfill []metrics.SlotValue
		if backfill, err = metrics.QueryHeatPumpBackfill(id, epoch); err == nil {
			var temps map[int64]float64
			if temps, err = metrics.QueryTemperatureHistory(epoch); err == nil {
				*c.res = analyzeHeatPump(rows, backfill, temps, day, time.Local)
				c.res.Meter, c.res.Entity, c.res.Computed = ref, id, c.at
				return c.res, nil
			}
		}
	}
	site.log.ERROR.Printf("heat pump daily fit: %v", err)
	c.err = err
	return c.res, c.err
}

// heatPumpPlanDay is one local date of the latest forecast (kWh).
type heatPumpPlanDay struct {
	Date     string `json:"date"`
	Slots    int    `json:"slots"`
	Modelled bool   `json:"modelled"`         // false: the date kept the active forecast
	Reason   string `json:"reason,omitempty"` // why it did
	// the forecast was colder than the fit supports: heating-degree clamped, or energy capped
	Extrapolated bool    `json:"extrapolated"`
	HDD          float64 `json:"hdd"`
	HeatPump     float64 `json:"heatPumpKWh"`
	NonHP        float64 `json:"nonHeatPumpKWh"`
	Total        float64 `json:"totalKWh"`
}

// heatPumpForecastStale reports whether the temperature forecast cannot be trusted at now:
// missing, ending in the past or flat over the next 24 h.
func heatPumpForecastStale(rates api.Rates, now time.Time) bool {
	if len(rates) == 0 || !rates[len(rates)-1].End.After(now) {
		return true
	}
	start := now.Truncate(tariff.SlotDuration)
	var n int
	var lo, hi float64
	for t := start; t.Before(start.Add(24 * time.Hour)); t = t.Add(tariff.SlotDuration) {
		r, err := rates.At(t)
		if err != nil {
			continue
		}
		if n == 0 {
			lo, hi = r.Value, r.Value
		}
		lo, hi = min(lo, r.Value), max(hi, r.Value)
		n++
	}
	return n >= heatPumpConstantSlot && lo == hi
}

// heatPumpInput is what composeHeatPump forecasts from: the slots from Start with their
// non-heat-pump profile, and for dates it cannot forecast the active forecast (kWh per
// slot) and the household profile's band.
type heatPumpInput struct {
	Start       time.Time
	NonHP       []metrics.HomeForecastSlot
	ProfileBand []metrics.HomeForecastSlot
	Active      []float64
	Rates       api.Rates
	Now         time.Time
	History     map[int64]float64 // recorded temperature per slot, fills the elapsed part of today
	Loc         *time.Location
}

// composeHeatPump forecasts the slots of in: the non-heat-pump profile plus the heat
// pump's daily energy a + b x HDD spread by the learned shape. Dates whose forecast covers
// less than heatingDegreeDayFill of the slots asked for keep in.Active (kWh per slot) and
// in.ProfileBand. series is kWh per slot, band Wh per slot with the heat-pump part widened by
// the energy of heatPumpSigmaK each side. Calibrating the band of the sum against its own
// archived residuals (as calibrateHomeRanges does for the household forecast) is a later step.
func composeHeatPump(a *heatPumpAnalysis, in heatPumpInput) (series []float64, band []metrics.HomeForecastSlot, days []heatPumpPlanDay) {
	start, nonHP, profileBand, active, rates, loc := in.Start, in.NonHP, in.ProfileBand, in.Active, in.Rates, in.Loc
	temperature := func(t time.Time) (float64, bool) {
		if r, err := rates.At(t); err == nil {
			return r.Value, true
		}
		if t.Add(tariff.SlotDuration).After(in.Now) {
			return 0, false
		}
		v, ok := in.History[t.Unix()]
		return v, ok
	}
	n := len(nonHP)
	series, band = make([]float64, n), make([]metrics.HomeForecastSlot, n)
	type group struct {
		date  time.Time
		slots []int
	}
	var groups []*group
	for i := range n {
		t := start.Add(time.Duration(i) * tariff.SlotDuration).In(loc)
		date := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		if len(groups) == 0 || !groups[len(groups)-1].date.Equal(date) {
			groups = append(groups, &group{date: date})
		}
		g := groups[len(groups)-1]
		g.slots = append(g.slots, i)
	}

	for _, g := range groups {
		day := heatPumpPlanDay{Date: g.date.Format(time.DateOnly), Slots: len(g.slots)}

		// the date's heating-degree over its whole length: the forecast, and for slots already
		// elapsed that it lacks the recorded temperature; at least heatingDegreeDayFill of the
		// date must be known and every slot asked for must be forecast
		hdd, shapeSum, covered, length := heatPumpDateHDD(a, g.date, temperature)
		var have int
		for _, i := range g.slots {
			if _, err := rates.At(start.Add(time.Duration(i) * tariff.SlotDuration)); err == nil {
				have++
			}
		}
		day.Modelled = shapeSum > 0 && float64(covered) >= heatingDegreeDayFill*float64(length) && float64(have) >= heatingDegreeDayFill*float64(len(g.slots))
		if !day.Modelled {
			day.Reason = heatPumpTemperatureStale
		}

		if !day.Modelled {
			for _, i := range g.slots {
				series[i], band[i] = active[i], profileBand[i]
				day.Total += active[i]
			}
			days = append(days, day)
			continue
		}

		day.HDD = hdd / float64(covered)
		effective := min(day.HDD, a.Fit.hddClamp())
		daily := max(0, a.Fit.A+a.Fit.B*effective)
		day.Extrapolated = effective < day.HDD
		if capped := a.Fit.dailyCap(); daily > capped {
			daily, day.Extrapolated = capped, true
		}
		for _, i := range g.slots {
			t := start.Add(time.Duration(i) * tariff.SlotDuration).In(loc)
			share := a.Shape[slotIndex(t)] / shapeSum
			hp, half := daily*share, a.Fit.B*heatPumpSigmaK*share
			p := nonHP[i]
			band[i] = metrics.HomeForecastSlot{
				Start: p.Start,
				Low:   p.Low + max(0, hp-half)*1e3,
				Base:  p.Base + hp*1e3,
				High:  p.High + (hp+half)*1e3,
			}
			series[i] = band[i].Base / 1e3
			day.HeatPump += hp
			day.NonHP += p.Base / 1e3
			day.Total += series[i]
		}
		days = append(days, day)
	}
	return series, band, days
}

// heatPumpDateHDD sums the heating-degree of the slots of date that temperature knows, with
// the shape weight of the whole date and the counts of known slots and slots of the date.
func heatPumpDateHDD(a *heatPumpAnalysis, date time.Time, temperature func(time.Time) (float64, bool)) (hdd, shapeSum float64, covered, length int) {
	for t := date; t.Before(date.AddDate(0, 0, 1)); t = t.Add(tariff.SlotDuration) {
		length++
		shapeSum += a.Shape[slotIndex(t)]
		if v, ok := temperature(t); ok {
			hdd += heatingDegree(v)
			covered++
		}
	}
	return hdd, shapeSum, covered, length
}

// heatPumpLastState is the outcome of the latest run.
type heatPumpLastState struct {
	mu      sync.Mutex
	updated time.Time
	open    bool
	reason  string
	days    []heatPumpPlanDay
	stats   metrics.NonHeatPumpStats
}

func (site *Site) heatPumpRecord(open bool, reason string, stats metrics.NonHeatPumpStats, days []heatPumpPlanDay) {
	l := &site.heatPumpLast
	l.mu.Lock()
	defer l.mu.Unlock()
	l.updated, l.open, l.reason, l.stats, l.days = time.Now(), open, reason, stats, days
}

// heatPumpSplit computes the heat-pump forecast of the n request slots from start for
// ref: kWh per slot and the Wh band per slot. active is the forecast in use (kWh per
// slot) and profile the household profile, both only for the dates the temperature forecast
// does not cover. The result is nil unless every gate is open, and the reason is recorded. In
// mode off it does nothing at all.
func (site *Site) heatPumpSplit(mode, ref string, now time.Time, profile *metrics.HomeForecastResult, active []float64) ([]float64, []metrics.HomeForecastSlot) {
	if mode == heatPumpOff {
		return nil, nil
	}
	closed := func(stats metrics.NonHeatPumpStats, reason string) ([]float64, []metrics.HomeForecastSlot) {
		site.heatPumpRecord(false, reason, stats, nil)
		return nil, nil
	}
	var stats metrics.NonHeatPumpStats
	if ref == "" {
		return closed(stats, heatPumpNoMeter)
	}
	if db.Instance == nil || profile == nil || len(profile.Rates) != len(active) || len(active) == 0 {
		return closed(stats, heatPumpError)
	}

	a, err := site.heatPumpDailyAnalysis(ref, now)
	if err != nil || a == nil {
		return closed(stats, heatPumpError)
	}
	if !a.Open {
		return closed(stats, a.Reason)
	}

	var rates api.Rates
	if tempTariff := site.GetTariff(api.TariffUsageTemperature); tempTariff != nil {
		var err error
		if rates, err = tempTariff.Rates(); err != nil {
			rates = nil
		}
	}
	if heatPumpForecastStale(rates, now) {
		return closed(stats, heatPumpTemperatureStale)
	}

	id := a.Entity
	incomplete, newest, seen, err := metrics.HeatPumpFreshness(id, now.Add(-24*time.Hour))
	if err != nil {
		site.log.ERROR.Printf("heat pump freshness: %v", err)
		return closed(stats, heatPumpError)
	}
	if incomplete > 0 || !seen || now.Sub(newest.Add(tariff.SlotDuration)) > heatPumpMaxMeterAge {
		return closed(stats, heatPumpMeterIncomplete)
	}

	history, err := metrics.QueryTemperatureHistory(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local))
	if err != nil {
		site.log.ERROR.Printf("heat pump temperature history: %v", err)
		return closed(stats, heatPumpError)
	}

	from := profile.Rates[0].Start
	to := profile.Rates[len(profile.Rates)-1].Start.Add(tariff.SlotDuration)
	nonHP, stats, err := metrics.HomeForecastNonHeatPump(from, to, id)
	if err != nil {
		if !errors.Is(err, metrics.ErrIncomplete) {
			site.log.ERROR.Printf("heat pump profile: %v", err)
			return closed(stats, heatPumpError)
		}
		return closed(stats, heatPumpProfile)
	}
	if stats.Pairs == 0 || float64(stats.Dropped) > heatPumpMaxDropped*float64(stats.Pairs) {
		return closed(stats, heatPumpNegativeResidual)
	}
	if len(nonHP.Rates) != len(active) {
		return closed(stats, heatPumpError)
	}

	series, band, days := composeHeatPump(a, heatPumpInput{Start: from, NonHP: nonHP.Rates, ProfileBand: profile.Rates, Active: active, Rates: rates, Now: now, History: history, Loc: time.Local})
	modelled := false
	for _, d := range days {
		modelled = modelled || d.Modelled
	}
	for i := range series {
		if math.IsNaN(series[i]) || math.IsInf(series[i], 0) || series[i] < 0 {
			return closed(stats, heatPumpError)
		}
	}
	if !modelled {
		return closed(stats, heatPumpTemperatureStale)
	}
	site.heatPumpRecord(true, "", stats, days)
	return series, band
}
