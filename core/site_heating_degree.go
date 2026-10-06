package core

import (
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"github.com/jinzhu/now"
)

// Heating-degree setting (ADR-015, demand temperature backtest). "auto" is the
// correction as it always was (slot-level r >= 0.3, applyHeatingDegree). "on"
// gates the same slot-level fit on the correlation of daily totals instead and
// caps each day's summed correction; it was chosen as T3dg in the backtest.
// "off" never corrects. Whatever the setting, the variants not in use are
// archived as shadow forecasts so they can be scored live.
const (
	heatingDegreeAuto = "auto"
	heatingDegreeOn   = "on"
	heatingDegreeOff  = "off"
)

// heatingDegreeMode returns the effective mode, auto for settings saved before
// the setting existed.
func heatingDegreeMode(setting string) string {
	if setting == "" {
		return heatingDegreeAuto
	}
	return setting
}

const (
	// heatingDegreeMinDailyCorrelation is the "on" gate: Pearson r between daily house
	// energy and daily mean heating-degree. Daily sums remove the daily load cycle that
	// dilutes the slot-level r (0.14-0.28 in heating season against 0.62-0.72 daily).
	heatingDegreeMinDailyCorrelation = 0.3
	// heatingDegreeMinDays is the number of complete days the daily r needs.
	heatingDegreeMinDays = 20
	// heatingDegreeDayFill is the share of a day's slots that makes it complete.
	heatingDegreeDayFill = 0.9
	// heatingDegreeCapFloor (kWh per 96 slots) and heatingDegreeCapShare (of the day's
	// uncorrected total) bound the summed correction per local date in "on" mode.
	heatingDegreeCapFloor = 2.0
	heatingDegreeCapShare = 0.25
	// heatingDegreeRetry bounds how often a failed daily analysis is retried.
	heatingDegreeRetry = time.Hour
)

// gate reasons, reported in the status
const (
	heatingDegreeNoTariff   = "no_tariff"
	heatingDegreeFewDays    = "few_days"
	heatingDegreeFewSamples = "few_samples"
	heatingDegreeDailyRLow  = "daily_r_low"
	heatingDegreeSlopeSign  = "slope_not_positive"
	heatingDegreeError      = "error"
)

// heatingDegreeAnalysis is the "on" gate's view of the fit window.
type heatingDegreeAnalysis struct {
	From, Computed time.Time
	Samples        int
	CompleteDays   int
	DailyR         *float64
	SlotR          *float64
	Fit            heatingDegreeFit
	HasFit         bool // the slot-level regression exists (both sides vary)
	Open           bool
	Reason         string // why the gate is shut
}

// heatingDegreeDayPoint is one local date's aggregate of the fit window.
type heatingDegreeDayPoint struct {
	Date     time.Time
	Slots    int // slots with energy and temperature
	Length   int // slots of the local date: 92, 96 or 100
	Energy   float64
	HDD      float64
	Complete bool
}

// heatingDegreeDays aggregates samples by local date in loc. A date is complete
// when at least heatingDegreeDayFill of its slots are present; its energy is
// the sum over present slots scaled by length/present, its heating-degree the
// mean over present slots.
func heatingDegreeDays(samples []metrics.TemperatureSample, loc *time.Location) []heatingDegreeDayPoint {
	byDate := make(map[time.Time]*heatingDegreeDayPoint)
	for _, s := range samples {
		if math.IsNaN(s.Energy) || s.Energy < 0 || math.IsNaN(s.Temperature) {
			continue
		}
		t := time.Unix(s.Ts, 0).In(loc)
		date := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		p := byDate[date]
		if p == nil {
			p = &heatingDegreeDayPoint{Date: date, Length: int(date.AddDate(0, 0, 1).Sub(date) / tariff.SlotDuration)}
			byDate[date] = p
		}
		p.Slots++
		p.Energy += s.Energy
		p.HDD += heatingDegree(s.Temperature)
	}

	res := make([]heatingDegreeDayPoint, 0, len(byDate))
	for _, p := range byDate {
		p.Complete = float64(p.Slots) >= heatingDegreeDayFill*float64(p.Length)
		p.Energy *= float64(p.Length) / float64(p.Slots)
		p.HDD /= float64(p.Slots)
		res = append(res, *p)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Date.Before(res[j].Date) })
	return res
}

// pearson returns r of x and y, false without variance on either side.
func pearson(x, y []float64) (float64, bool) {
	if len(x) < 2 || len(x) != len(y) {
		return 0, false
	}
	var mx, my float64
	for i := range x {
		mx += x[i]
		my += y[i]
	}
	mx /= float64(len(x))
	my /= float64(len(y))
	var sxy, sxx, syy float64
	for i := range x {
		dx, dy := x[i]-mx, y[i]-my
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	if sxx == 0 || syy == 0 {
		return 0, false
	}
	return sxy / math.Sqrt(sxx*syy), true
}

// analyzeHeatingDegree evaluates the "on" gate on the fit window's samples: at
// least heatingDegreeMinDays complete days, heatingDegreeMinSamples samples, a
// daily r of at least heatingDegreeMinDailyCorrelation and a positive slot-level
// slope (more energy when colder). The correction it opens is the unchanged
// slot-level fit.
func analyzeHeatingDegree(samples []metrics.TemperatureSample, loc *time.Location) heatingDegreeAnalysis {
	res := heatingDegreeAnalysis{Samples: len(samples)}

	var energy, hdd []float64
	for _, d := range heatingDegreeDays(samples, loc) {
		if d.Complete {
			energy = append(energy, d.Energy)
			hdd = append(hdd, d.HDD)
		}
	}
	res.CompleteDays = len(energy)
	if r, ok := pearson(energy, hdd); ok {
		res.DailyR = &r
	}

	if len(samples) > 0 {
		if fit, r, ok := regressHeatingDegree(samples); ok {
			res.Fit, res.HasFit, res.SlotR = fit, true, &r
		}
	}

	switch {
	case res.CompleteDays < heatingDegreeMinDays:
		res.Reason = heatingDegreeFewDays
	case res.Samples < heatingDegreeMinSamples:
		res.Reason = heatingDegreeFewSamples
	case res.DailyR == nil || *res.DailyR < heatingDegreeMinDailyCorrelation:
		res.Reason = heatingDegreeDailyRLow
	case !res.HasFit || res.Fit.slope <= 0:
		res.Reason = heatingDegreeSlopeSign
	default:
		res.Open = true
	}
	return res
}

// heatingDegreeDailyCache holds the "on" analysis of the current local day.
type heatingDegreeDailyCache struct {
	mu  sync.Mutex
	day time.Time
	res *heatingDegreeAnalysis
	err error
}

// heatingDegreeDailyAnalysis returns the "on" analysis over the 30 days before
// the current local midnight, computed at the first call after midnight (and
// retried after heatingDegreeRetry on failure), so the window is anchored to
// the calendar rather than to the process start.
func (site *Site) heatingDegreeDailyAnalysis() (*heatingDegreeAnalysis, error) {
	c := &site.heatingDegreeDailyState
	c.mu.Lock()
	defer c.mu.Unlock()

	day := now.BeginningOfDay()
	if c.day.Equal(day) && (c.err == nil || time.Since(c.res.Computed) < heatingDegreeRetry) {
		return c.res, c.err
	}

	from := day.AddDate(0, 0, -30)
	res := &heatingDegreeAnalysis{From: from, Computed: time.Now(), Reason: heatingDegreeError}
	c.day, c.res, c.err = day, res, nil
	if db.Instance == nil {
		c.err = errors.New("database offline")
		return c.res, c.err
	}
	samples, err := metrics.QueryHomeTemperatureSamples(from)
	if err != nil {
		site.log.ERROR.Printf("heating degree daily gate: %v", err)
		c.err = err
		return c.res, c.err
	}
	*res = analyzeHeatingDegree(samples, time.Local)
	res.From, res.Computed = from, time.Now()
	return c.res, nil
}

// heatingDegreeDay is one local date of a corrected forecast (kWh).
type heatingDegreeDay struct {
	Date        string  `json:"date"`
	Slots       int     `json:"slots"`
	Uncorrected float64 `json:"uncorrectedKWh"`
	Uncapped    float64 `json:"uncappedKWh"`   // the "on" correction before the cap
	Cap         float64 `json:"capKWh"`        // the cap's bound on |correction|
	Correction  float64 `json:"correctionKWh"` // the "on" correction after the cap
	Capped      bool    `json:"capped"`        // the cap bound
	Auto        float64 `json:"autoKWh"`       // the "auto" correction, for comparison
	Applied     bool    `json:"applied"`       // the "on" correction is open on this forecast
	AutoApplied bool    `json:"autoApplied"`   // the "auto" correction is open on this forecast
}

// capHeatingDegree bounds the summed correction per local date in loc to
// max(heatingDegreeCapFloor x n/96, heatingDegreeCapShare x the date's
// uncorrected total) over its n forecast slots, scaling that date's
// corrections proportionally. corrections are modified in place; NaN entries
// (no temperature forecast) count as zero and stay NaN.
func capHeatingDegree(profile, corrections []float64, start time.Time, loc *time.Location) []heatingDegreeDay {
	var res []heatingDegreeDay
	var idx [][]int
	for i := range profile {
		t := start.Add(time.Duration(i) * tariff.SlotDuration).In(loc)
		date := t.Format(time.DateOnly)
		if len(res) == 0 || res[len(res)-1].Date != date {
			res = append(res, heatingDegreeDay{Date: date})
			idx = append(idx, nil)
		}
		d := &res[len(res)-1]
		d.Slots++
		d.Uncorrected += profile[i]
		if !math.IsNaN(corrections[i]) {
			d.Uncapped += corrections[i]
		}
		idx[len(idx)-1] = append(idx[len(idx)-1], i)
	}

	for k := range res {
		d := &res[k]
		d.Cap = max(heatingDegreeCapFloor*float64(d.Slots)/96, heatingDegreeCapShare*d.Uncorrected)
		d.Correction = d.Uncapped
		if total := math.Abs(d.Uncapped); total > d.Cap && d.Cap > 0 {
			scale := d.Cap / total
			d.Correction = 0
			for _, i := range idx[k] {
				if !math.IsNaN(corrections[i]) {
					corrections[i] *= scale
					d.Correction += corrections[i]
				}
			}
			d.Capped = true
		}
	}
	return res
}

// applyHeatingDegreeOn is the "on" correction of profile (kWh per slot from the
// current slot): the slot-level fit with its per-slot clamp, opened by the
// daily gate and capped per date. The per-date breakdown is nil when nothing
// is corrected.
func (site *Site) applyHeatingDegreeOn(profile []float64) ([]float64, []heatingDegreeDay) {
	tempTariff := site.GetTariff(api.TariffUsageTemperature)
	if tempTariff == nil || site.heatingDegreeDaily == nil {
		return profile, nil
	}

	a, err := site.heatingDegreeDaily()
	if err != nil || !a.Open { // errors are logged where the analysis is computed
		return profile, nil
	}

	rates, err := tempTariff.Rates()
	if err != nil || len(rates) == 0 {
		return profile, nil
	}

	start := time.Now().Truncate(tariff.SlotDuration)
	return correctHeatingDegreeOn(profile, start, rates, a.Fit, time.Local)
}

// correctHeatingDegreeOn applies fit to profile from start with the per-date cap.
func correctHeatingDegreeOn(profile []float64, start time.Time, rates api.Rates, fit heatingDegreeFit, loc *time.Location) ([]float64, []heatingDegreeDay) {
	corrections := heatingDegreeCorrections(profile, start, rates, fit)
	days := capHeatingDegree(profile, corrections, start, loc)
	for i := range days {
		days[i].Applied = true
	}
	return addHeatingDegree(profile, corrections), days
}

// heatingDegreeProfile returns profile corrected according to mode, and for
// "on" the per-date breakdown.
func (site *Site) heatingDegreeProfile(mode string, profile []float64) ([]float64, []heatingDegreeDay) {
	switch heatingDegreeMode(mode) {
	case heatingDegreeOff:
		return profile, nil
	case heatingDegreeOn:
		return site.applyHeatingDegreeOn(profile)
	default:
		return site.applyHeatingDegree(profile), nil
	}
}

// heatingDegreeShadowVariants are the variants archived beside mode: the one
// not in use, or both while the correction is off.
func heatingDegreeShadowVariants(mode string) []string {
	switch heatingDegreeMode(mode) {
	case heatingDegreeOn:
		return []string{heatingDegreeAuto}
	case heatingDegreeOff:
		return []string{heatingDegreeAuto, heatingDegreeOn}
	default:
		return []string{heatingDegreeOn}
	}
}

// heatingDegreeLastState is the per-date breakdown of the latest forecast.
type heatingDegreeLastState struct {
	mu      sync.Mutex
	updated time.Time
	days    []heatingDegreeDay
}

// heatingDegreeVariants corrects profile (kWh per slot from the current slot)
// with mode and computes the shadow variants beside it. It records the per-date
// breakdown of both corrections for the status.
func (site *Site) heatingDegreeVariants(mode string, profile []float64) ([]float64, map[string][]float64) {
	variants := map[string][]float64{heatingDegreeOff: profile}
	var days []heatingDegreeDay
	for _, v := range append([]string{heatingDegreeMode(mode)}, heatingDegreeShadowVariants(mode)...) {
		corrected, d := site.heatingDegreeProfile(v, profile)
		variants[v] = corrected
		if v == heatingDegreeOn {
			days = d
		}
	}

	start := time.Now().Truncate(tariff.SlotDuration)
	if days == nil {
		days = capHeatingDegree(profile, make([]float64, len(profile)), start, time.Local)
	}
	auto := variants[heatingDegreeAuto]
	for i := range profile {
		date := start.Add(time.Duration(i) * tariff.SlotDuration).In(time.Local).Format(time.DateOnly)
		for k := range days {
			if days[k].Date == date {
				days[k].Auto += auto[i] - profile[i]
				days[k].AutoApplied = days[k].AutoApplied || auto[i] != profile[i]
			}
		}
	}

	site.heatingDegreeLast.mu.Lock()
	site.heatingDegreeLast.updated, site.heatingDegreeLast.days = time.Now(), days
	site.heatingDegreeLast.mu.Unlock()

	shadow := make(map[string][]float64)
	for _, v := range heatingDegreeShadowVariants(mode) {
		shadow[v] = variants[v]
	}
	return variants[heatingDegreeMode(mode)], shadow
}

// heatingDegreeSnapshotPeriod returns the period [from, until) containing
// issued in which a horizon snapshot is taken, and the window [start, end) that
// snapshot is scored on: the local day for the first hour after midnight, the
// night 17:00-07:00 for 12:00-17:00.
func heatingDegreeSnapshotPeriod(issued time.Time) (from, until, start, end time.Time, ok bool) {
	t := issued.In(time.Local)
	at := func(d, h int) time.Time { return time.Date(t.Year(), t.Month(), t.Day()+d, h, 0, 0, 0, time.Local) }
	switch {
	case t.Before(at(0, 1)):
		return at(0, 0), at(0, 1), at(0, 0), at(1, 0), true
	case !t.Before(at(0, 12)) && t.Before(at(0, 17)):
		return at(0, 12), at(0, 17), at(0, 17), at(1, 7), true
	}
	return time.Time{}, time.Time{}, time.Time{}, time.Time{}, false
}

// heatingDegreeSnapshotWindow is heatingDegreeSnapshotPeriod's window, for scoring.
func heatingDegreeSnapshotWindow(issued time.Time) (time.Time, time.Time, bool) {
	_, _, start, end, ok := heatingDegreeSnapshotPeriod(issued)
	return start, end, ok
}

// heatingDegreeSnapshotDue reports whether a run issued at issued with a
// forecast horizon until horizonEnd takes its period's horizon snapshot: the
// first run in the period whose horizon covers the window. For the 12:00
// period that is, with day-ahead prices, the first run after their
// publication (the horizon ends at midnight before).
func (site *Site) heatingDegreeSnapshotDue(issued, horizonEnd time.Time) bool {
	from, until, _, end, ok := heatingDegreeSnapshotPeriod(issued)
	if !ok || horizonEnd.Before(end) || site.heatingDegreeSnapshot.Equal(from) {
		return false
	}
	exists, err := metrics.HomeForecastShadowSnapshotExists(from, until)
	if err != nil {
		site.log.ERROR.Printf("heating degree shadow: %v", err)
		return false
	}
	if exists { // taken before a restart
		site.heatingDegreeSnapshot = from
		return false
	}
	return true
}

// shadow score: the live ADR-015 energy-total comparison of "on" against "auto"
const (
	heatingDegreeScoreMinWindows = 14
	heatingDegreeBootstrapBlock  = 7
	heatingDegreeBootstrapRuns   = 10000
)

// heatingDegreeShadowScore compares the variants' energy-total errors over one
// window type. Delta is the mean of |on error| - |auto error| (negative: "on"
// was better) with its 95 % moving-block bootstrap interval.
type heatingDegreeShadowScore struct {
	Window     string   `json:"window"`     // day (issued 00:00) or overnight (17:00-07:00, issued from 12:00)
	MaxMissing int      `json:"maxMissing"` // slots a window may lack and still count
	Windows    int      `json:"windows"`    // complete windows scored
	Required   int      `json:"required"`
	Ready      bool     `json:"ready"`
	MAEOn      *float64 `json:"maeOnKWh,omitempty"`
	MAEAuto    *float64 `json:"maeAutoKWh,omitempty"`
	Delta      *float64 `json:"deltaKWh,omitempty"`
	CILow      *float64 `json:"ciLowKWh,omitempty"`
	CIHigh     *float64 `json:"ciHighKWh,omitempty"`
}

// blockBootstrapMean returns the 95 % interval of the mean of d (in time order)
// from a moving-block bootstrap: blocks of block consecutive values, drawn with
// a fixed seed, concatenated and trimmed to len(d); linear percentiles.
func blockBootstrapMean(d []float64, block, runs int) (float64, float64) {
	n := len(d)
	b := max(1, min(block, n))
	blocks := (n + b - 1) / b
	rng := rand.New(rand.NewPCG(0, 0))
	means := make([]float64, runs)
	for r := range means {
		var sum float64
		k := 0
		for range blocks {
			start := rng.IntN(n - b + 1)
			for j := 0; j < b && k < n; j++ {
				sum += d[start+j]
				k++
			}
		}
		means[r] = sum / float64(n)
	}
	slices.Sort(means)
	return percentileLinear(means, 0.025), percentileLinear(means, 0.975)
}

// percentileLinear is the linearly interpolated percentile of sorted values.
func percentileLinear(sorted []float64, p float64) float64 {
	pos := p * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := min(lo+1, len(sorted)-1)
	return sorted[lo] + (pos-float64(lo))*(sorted[hi]-sorted[lo])
}

// scoreHeatingDegreeShadow scores the complete windows of one type.
func scoreHeatingDegreeShadow(window string, maxMissing int, windows []metrics.HomeForecastShadowWindow) heatingDegreeShadowScore {
	res := heatingDegreeShadowScore{Window: window, MaxMissing: maxMissing, Required: heatingDegreeScoreMinWindows}
	var on, auto, diff []float64
	for _, w := range windows {
		if w.Missing > maxMissing {
			continue
		}
		eOn := math.Abs(w.Variants[heatingDegreeOn] - w.Actual)
		eAuto := math.Abs(w.Variants[heatingDegreeAuto] - w.Actual)
		on, auto, diff = append(on, eOn), append(auto, eAuto), append(diff, eOn-eAuto)
	}
	res.Windows = len(diff)
	if res.Windows < heatingDegreeScoreMinWindows {
		return res
	}
	mean := func(v []float64) *float64 {
		var s float64
		for _, x := range v {
			s += x
		}
		s /= float64(len(v))
		return &s
	}
	low, high := blockBootstrapMean(diff, heatingDegreeBootstrapBlock, heatingDegreeBootstrapRuns)
	res.Ready, res.MAEOn, res.MAEAuto, res.Delta, res.CILow, res.CIHigh = true, mean(on), mean(auto), mean(diff), &low, &high
	return res
}

// HeatingDegreeStatus is the read-only state of the household demand
// temperature correction for Plan > Details.
type HeatingDegreeStatus struct {
	Mode                string                     `json:"mode"`                // auto, on or off
	TemperatureForecast bool                       `json:"temperatureForecast"` // a temperature tariff is configured
	Applied             string                     `json:"applied"`             // the correction in the current plan: auto, on or none
	Open                bool                       `json:"open"`                // the "on" gate
	Reason              string                     `json:"reason,omitempty"`    // why the "on" gate is shut
	Error               string                     `json:"error,omitempty"`
	From                *time.Time                 `json:"from,omitempty"` // fit window, to the computation
	Computed            *time.Time                 `json:"computed,omitempty"`
	Samples             int                        `json:"samples"`
	CompleteDays        int                        `json:"completeDays"`
	DailyR              *float64                   `json:"dailyR,omitempty"`
	SlotR               *float64                   `json:"slotR,omitempty"`
	SlopeWhPerSlotK     *float64                   `json:"slopeWhPerSlotK,omitempty"`
	SlopeKWhPerDayK     *float64                   `json:"slopeKWhPerDayK,omitempty"`
	MeanHDD             *float64                   `json:"meanHDD,omitempty"`
	Thresholds          HeatingDegreeThresholds    `json:"thresholds"`
	Updated             *time.Time                 `json:"updated,omitempty"` // of the per-date breakdown
	Days                []heatingDegreeDay         `json:"days"`
	Shadow              []heatingDegreeShadowScore `json:"shadow"`
	ShadowError         string                     `json:"shadowError,omitempty"`
}

// HeatingDegreeThresholds are the gate and cap parameters in effect.
type HeatingDegreeThresholds struct {
	MinDays          int     `json:"minDays"`
	MinSamples       int     `json:"minSamples"`
	MinDailyR        float64 `json:"minDailyR"`
	DayFill          float64 `json:"dayFill"`
	BaseTemp         float64 `json:"baseTemp"`
	CapFloorKWh      float64 `json:"capFloorKWh"`
	CapShare         float64 `json:"capShare"`
	AutoMinSlotR     float64 `json:"autoMinSlotR"`
	ShadowMinWindows int     `json:"shadowMinWindows"`
}

// HeatingDegree reports the correction's state. The "on" gate is evaluated
// whatever the setting, so it can be reviewed before switching it on.
func (site *Site) HeatingDegree() HeatingDegreeStatus {
	res := HeatingDegreeStatus{
		Mode:                heatingDegreeMode(site.GetEnergyIntelligenceSettings().HeatingDegree),
		TemperatureForecast: site.tariffs != nil && site.GetTariff(api.TariffUsageTemperature) != nil,
		Applied:             "none",
		Thresholds: HeatingDegreeThresholds{
			MinDays: heatingDegreeMinDays, MinSamples: heatingDegreeMinSamples, MinDailyR: heatingDegreeMinDailyCorrelation,
			DayFill: heatingDegreeDayFill, BaseTemp: heatingDegreeBaseTemp, CapFloorKWh: heatingDegreeCapFloor, CapShare: heatingDegreeCapShare,
			AutoMinSlotR: heatingDegreeMinCorrelation, ShadowMinWindows: heatingDegreeScoreMinWindows,
		},
		Days:   []heatingDegreeDay{},
		Shadow: []heatingDegreeShadowScore{},
	}

	if site.heatingDegreeDaily != nil {
		a, err := site.heatingDegreeDaily()
		if err != nil {
			res.Error = err.Error()
		}
		if a != nil {
			res.Open, res.Reason = a.Open, a.Reason
			res.From, res.Computed = &a.From, &a.Computed
			res.Samples, res.CompleteDays, res.DailyR, res.SlotR = a.Samples, a.CompleteDays, a.DailyR, a.SlotR
			if a.HasFit {
				slope, perDay, mean := a.Fit.slope, a.Fit.slope*96/1e3, a.Fit.meanHDD
				res.SlopeWhPerSlotK, res.SlopeKWhPerDayK, res.MeanHDD = &slope, &perDay, &mean
			}
		}
	}
	if !res.TemperatureForecast {
		res.Open, res.Reason = false, heatingDegreeNoTariff
	}

	site.heatingDegreeLast.mu.Lock()
	if !site.heatingDegreeLast.updated.IsZero() {
		updated := site.heatingDegreeLast.updated
		res.Updated, res.Days = &updated, slices.Clone(site.heatingDegreeLast.days)
	}
	site.heatingDegreeLast.mu.Unlock()
	for _, d := range res.Days {
		switch {
		case res.Mode == heatingDegreeOn && d.Applied:
			res.Applied = heatingDegreeOn
		case res.Mode == heatingDegreeAuto && d.AutoApplied:
			res.Applied = heatingDegreeAuto
		}
	}

	if db.Instance == nil {
		res.ShadowError = "database offline"
		return res
	}
	windows, err := metrics.HomeForecastShadowWindows(heatingDegreeSnapshotWindow, []string{heatingDegreeAuto, heatingDegreeOn}, time.Now())
	if err != nil {
		res.ShadowError = err.Error()
		return res
	}
	byType := map[string][]metrics.HomeForecastShadowWindow{}
	for _, w := range windows {
		kind := "overnight"
		if w.Start.In(time.Local).Hour() == 0 {
			kind = "day"
		}
		byType[kind] = append(byType[kind], w)
	}
	res.Shadow = []heatingDegreeShadowScore{
		scoreHeatingDegreeShadow("day", 4, byType["day"]),
		scoreHeatingDegreeShadow("overnight", 2, byType["overnight"]),
	}
	return res
}
