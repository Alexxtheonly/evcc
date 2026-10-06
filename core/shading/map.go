package shading

import (
	"math"
	"slices"
	"time"
)

// Sample is one completed 15-minute slot: what the forecast predicted for it
// and what the PV meters measured. Both in kWh. Callers only pass slots where
// both sides are known; a missing or flagged measurement is never a zero.
type Sample struct {
	Slot     time.Time // slot start
	Forecast float64
	Measured float64
}

// SlotDuration is the length of a sample slot. Sun positions are taken at the
// slot midpoint.
const SlotDuration = 15 * time.Minute

// Config holds the learning parameters. DefaultConfig is what evcc uses; the
// backtest in this package documents why.
type Config struct {
	AzimuthStep     float64       // bin width, degrees
	ElevationStep   float64       // bin height, degrees
	Window          time.Duration // recent training window
	HalfLife        time.Duration // recency half-life of a sample's weight
	MinForecast     float64       // kWh, slots with less forecast energy carry no weight
	MaxRatio        float64       // measured is clipped to MaxRatio x forecast
	MinSlots        int           // evidence threshold: slots per bin
	MinWeightedFcst float64       // evidence threshold: sum of weight x forecast kWh
	MinFactor       float64       // factor clamp
	MaxFactor       float64
	SpreadLow       float64 // quantile of the per-slot ratios for the low band
	SpreadHigh      float64 // quantile of the per-slot ratios for the high band
	SeasonWindow    time.Duration
	SeasonWeight    float64 // weight of a sample one year back, squared for two years, ...
	SeasonYears     int
}

// DefaultConfig is the parameter set chosen by backtest (see backtest_test.go).
var DefaultConfig = Config{
	AzimuthStep:     10,
	ElevationStep:   5,
	Window:          60 * 24 * time.Hour,
	HalfLife:        14 * 24 * time.Hour,
	MinForecast:     0.02,
	MaxRatio:        3,
	MinSlots:        4,
	MinWeightedFcst: 0.05,
	MinFactor:       0.05,
	MaxFactor:       1.5,
	SpreadLow:       0.2,
	SpreadHigh:      0.8,
	SeasonWindow:    21 * 24 * time.Hour,
	SeasonWeight:    0.5,
	SeasonYears:     3,
}

// Source names where a factor came from.
const (
	SourceLearned   = "learned"
	SourceNeighbour = "neighbour"
	SourceFallback  = "fallback" // caller substitutes its daily scale, or 1
	SourceNight     = "night"    // sun below the horizon, factor 1
)

// Window is a training interval [From, To).
type Window struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// TrainingWindows returns the intervals whose samples Learn uses for a map
// computed at cutoff: the recent window plus the same calendar window of
// earlier years. Callers query exactly these.
func (c Config) TrainingWindows(cutoff time.Time) []Window {
	res := []Window{{From: cutoff.Add(-c.Window), To: cutoff}}
	for k := 1; k <= c.SeasonYears; k++ {
		center := cutoff.AddDate(-k, 0, 0)
		res = append(res, Window{From: center.Add(-c.SeasonWindow), To: center.Add(c.SeasonWindow)})
	}
	return res
}

type binKey struct{ az, el int }

type ratioSample struct{ ratio, weight float64 }

type accumulator struct {
	measured, forecast float64 // weighted sums, kWh
	weight             float64 // sum of weights
	slots              int
	last               time.Time
	ratios             []ratioSample
}

func (a *accumulator) add(b *accumulator) {
	a.measured += b.measured
	a.forecast += b.forecast
	a.weight += b.weight
	a.slots += b.slots
	if b.last.After(a.last) {
		a.last = b.last
	}
	a.ratios = append(a.ratios, b.ratios...)
}

// Bin is one learned cell of the map, for display and for the API.
type Bin struct {
	AzimuthFrom   float64   `json:"azimuthFrom"`
	AzimuthTo     float64   `json:"azimuthTo"`
	ElevationFrom float64   `json:"elevationFrom"`
	ElevationTo   float64   `json:"elevationTo"`
	Factor        float64   `json:"factor"`
	Low           float64   `json:"low"`  // weighted SpreadLow quantile of the per-slot ratios
	High          float64   `json:"high"` // weighted SpreadHigh quantile of the per-slot ratios
	Slots         int       `json:"slots"`
	Weight        float64   `json:"weight"`      // sum of recency weights
	ForecastKWh   float64   `json:"forecastKWh"` // weighted forecast energy
	LastSample    time.Time `json:"lastSample"`  // slot start of the newest contributing sample
	Source        string    `json:"source"`      // learned or neighbour
}

// Map is a learned correction. The zero value and nil are valid and correct
// nothing (every lookup falls back).
type Map struct {
	Location Location
	Config   Config
	Cutoff   time.Time
	Days     int // distinct days with at least one usable sample
	Samples  int
	bins     map[binKey]*accumulator
}

func (c Config) key(az, el float64) binKey {
	return binKey{int(math.Floor(az / c.AzimuthStep)), int(math.Floor(el / c.ElevationStep))}
}

// weight returns a sample's weight for a map learned at cutoff, 0 if outside
// every training window.
func (c Config) weight(slot, cutoff time.Time) float64 {
	if !slot.Before(cutoff) {
		return 0
	}
	halfLives := func(d time.Duration) float64 {
		return math.Pow(0.5, math.Abs(float64(d))/float64(c.HalfLife))
	}
	if !slot.Before(cutoff.Add(-c.Window)) {
		return halfLives(cutoff.Sub(slot))
	}
	for k := 1; k <= c.SeasonYears; k++ {
		center := cutoff.AddDate(-k, 0, 0)
		if d := slot.Sub(center); math.Abs(float64(d)) <= float64(c.SeasonWindow) {
			return math.Pow(c.SeasonWeight, float64(k)) * halfLives(d)
		}
	}
	return 0
}

// Learn builds a map from samples completed before cutoff. Samples outside the
// training windows, with negligible forecast, or with the sun below the
// horizon are ignored.
func Learn(samples []Sample, loc Location, cutoff time.Time, cfg Config) *Map {
	m := &Map{Location: loc, Config: cfg, Cutoff: cutoff, bins: make(map[binKey]*accumulator)}
	days := make(map[string]bool)
	for _, s := range samples {
		if !finite(s.Forecast) || !finite(s.Measured) || s.Measured < 0 || s.Forecast < cfg.MinForecast {
			continue
		}
		w := cfg.weight(s.Slot, cutoff)
		if w <= 0 {
			continue
		}
		az, el := SunPosition(s.Slot.Add(SlotDuration/2), loc)
		if el <= 0 {
			continue
		}
		measured := min(s.Measured, cfg.MaxRatio*s.Forecast)
		k := cfg.key(az, el)
		a := m.bins[k]
		if a == nil {
			a = new(accumulator)
			m.bins[k] = a
		}
		a.measured += w * measured
		a.forecast += w * s.Forecast
		a.weight += w
		a.slots++
		if s.Slot.After(a.last) {
			a.last = s.Slot
		}
		a.ratios = append(a.ratios, ratioSample{measured / s.Forecast, w * s.Forecast})
		days[s.Slot.Format(time.DateOnly)] = true
		m.Samples++
	}
	m.Days = len(days)
	return m
}

func (c Config) enough(a *accumulator) bool {
	return a != nil && a.slots >= c.MinSlots && a.forecast >= c.MinWeightedFcst
}

func (c Config) clamp(v float64) float64 {
	return max(c.MinFactor, min(c.MaxFactor, v))
}

// Learned reports whether at least one bin has enough evidence of its own.
func (m *Map) Learned() bool {
	if m == nil {
		return false
	}
	for _, a := range m.bins {
		if m.Config.enough(a) {
			return true
		}
	}
	return false
}

// lookup returns the evidence for k: its own if sufficient, else the pooled
// 3x3 neighbourhood if that is sufficient, else nil.
func (m *Map) lookup(k binKey) (*accumulator, string) {
	if a := m.bins[k]; m.Config.enough(a) {
		return a, SourceLearned
	}
	pool := new(accumulator)
	if a := m.bins[k]; a != nil {
		pool.add(a)
	}
	for daz := -1; daz <= 1; daz++ {
		for del := -1; del <= 1; del++ {
			if daz == 0 && del == 0 {
				continue
			}
			if a := m.bins[m.Config.wrap(binKey{k.az + daz, k.el + del})]; a != nil {
				pool.add(a)
			}
		}
	}
	if m.Config.enough(pool) {
		return pool, SourceNeighbour
	}
	return nil, SourceFallback
}

// wrap keeps azimuth bins on the circle.
func (c Config) wrap(k binKey) binKey {
	n := int(math.Round(360 / c.AzimuthStep))
	k.az = ((k.az % n) + n) % n
	return k
}

// Correction is the factor for one slot. Low and High are the factor's band
// from the spread of the bin's per-slot ratios: Low <= Factor <= High. For a
// fallback or night slot all three are equal to Factor and the caller decides
// the value of Factor (see Map.Factor).
type Correction struct {
	Factor, Low, High float64
	Source            string
}

func (m *Map) correction(a *accumulator, source string) Correction {
	f := m.Config.clamp(a.measured / a.forecast)
	low := m.Config.clamp(weightedQuantile(a.ratios, m.Config.SpreadLow))
	high := m.Config.clamp(weightedQuantile(a.ratios, m.Config.SpreadHigh))
	return Correction{Factor: f, Low: min(low, f), High: max(high, f), Source: source}
}

// Factor returns the correction for the slot starting at slot. fallback is
// used where no bin or neighbourhood has enough evidence; below the horizon
// the factor is 1.
func (m *Map) Factor(slot time.Time, fallback float64) Correction {
	if m == nil {
		return Correction{fallback, fallback, fallback, SourceFallback}
	}
	az, el := SunPosition(slot.Add(SlotDuration/2), m.Location)
	if el <= 0 {
		return Correction{1, 1, 1, SourceNight}
	}
	a, source := m.lookup(m.Config.key(az, el))
	if a == nil {
		return Correction{fallback, fallback, fallback, SourceFallback}
	}
	return m.correction(a, source)
}

// Bins returns every cell that has a learned or neighbour factor, ordered by
// elevation then azimuth. Cells without enough evidence are omitted.
func (m *Map) Bins() []Bin {
	if m == nil {
		return nil
	}
	cand := make(map[binKey]bool)
	for k := range m.bins {
		for daz := -1; daz <= 1; daz++ {
			for del := -1; del <= 1; del++ {
				if n := m.Config.wrap(binKey{k.az + daz, k.el + del}); n.el >= 0 {
					cand[n] = true
				}
			}
		}
	}
	var res []Bin
	for k := range cand {
		a, source := m.lookup(k)
		if a == nil {
			continue
		}
		c := m.correction(a, source)
		b := Bin{
			AzimuthFrom:   float64(k.az) * m.Config.AzimuthStep,
			AzimuthTo:     float64(k.az+1) * m.Config.AzimuthStep,
			ElevationFrom: float64(k.el) * m.Config.ElevationStep,
			ElevationTo:   float64(k.el+1) * m.Config.ElevationStep,
			Factor:        c.Factor,
			Low:           c.Low,
			High:          c.High,
			Slots:         a.slots,
			Weight:        a.weight,
			ForecastKWh:   a.forecast,
			LastSample:    a.last,
			Source:        source,
		}
		res = append(res, b)
	}
	slices.SortFunc(res, func(a, b Bin) int {
		if a.ElevationFrom != b.ElevationFrom {
			return cmpFloat(a.ElevationFrom, b.ElevationFrom)
		}
		return cmpFloat(a.AzimuthFrom, b.AzimuthFrom)
	})
	return res
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// weightedQuantile returns the smallest ratio whose cumulative weight reaches
// q of the total.
func weightedQuantile(rr []ratioSample, q float64) float64 {
	if len(rr) == 0 {
		return 1
	}
	sorted := slices.Clone(rr)
	slices.SortFunc(sorted, func(a, b ratioSample) int { return cmpFloat(a.ratio, b.ratio) })
	var total float64
	for _, r := range sorted {
		total += r.weight
	}
	var cum float64
	for _, r := range sorted {
		cum += r.weight
		if cum >= q*total {
			return r.ratio
		}
	}
	return sorted[len(sorted)-1].ratio
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
