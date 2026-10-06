package shading

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testLoc = Location{49.2115, 9.1949}

const day = 24 * time.Hour

// noon is a slot near solar noon close to the June solstice, where the sun's
// position barely moves from day to day, so slots on neighbouring days share
// one bin.
func noon(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 11, 0, 0, 0, time.UTC)
}

func sample(slot time.Time, forecast, measured float64) Sample {
	return Sample{Slot: slot, Forecast: forecast, Measured: measured}
}

// sameBin fails the test if the fixture's slots do not share one bin, so a
// test never silently checks a different bin than it thinks.
func sameBin(t *testing.T, cfg Config, ss ...Sample) {
	t.Helper()
	az, el := SunPosition(ss[0].Slot.Add(SlotDuration/2), testLoc)
	want := cfg.key(az, el)
	for _, s := range ss {
		az, el := SunPosition(s.Slot.Add(SlotDuration/2), testLoc)
		require.Equal(t, want, cfg.key(az, el), "fixture slot %v left the bin", s.Slot)
	}
}

func repeat(n int, f func(i int) Sample) []Sample {
	res := make([]Sample, n)
	for i := range res {
		res[i] = f(i)
	}
	return res
}

func TestLearnRecencyWeighting(t *testing.T) {
	cfg := DefaultConfig
	cutoff := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)

	recent := repeat(4, func(i int) Sample { return sample(noon(2026, 6, 24-i), 0.5, 0.25) })   // ratio 0.5
	older := repeat(4, func(i int) Sample { return sample(noon(2026, 6, 14-i), 0.5, 0.5) })     // ratio 1.0
	all := append(append([]Sample{}, recent...), older...)
	sameBin(t, cfg, all...)

	var wm, wf float64
	for _, s := range all {
		w := math.Pow(0.5, cutoff.Sub(s.Slot).Hours()/24/14)
		wm += w * s.Measured
		wf += w * s.Forecast
	}

	m := Learn(all, testLoc, cutoff, cfg)
	c := m.Factor(noon(2026, 6, 25), 0.9)
	assert.Equal(t, SourceLearned, c.Source)
	assert.InDelta(t, wm/wf, c.Factor, 1e-9)
	assert.Greater(t, 0.75, c.Factor, "the recent half dominates an unweighted 0.75")
	assert.InDelta(t, 0.5, c.Low, 1e-9, "low band from the per-slot ratio spread")
	assert.InDelta(t, 1.0, c.High, 1e-9, "high band from the per-slot ratio spread")
}

func TestLearnIgnoresWhatItMust(t *testing.T) {
	cfg := DefaultConfig
	cutoff := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	base := repeat(4, func(i int) Sample { return sample(noon(2026, 6, 24-i), 0.5, 0.25) })

	for _, tc := range []struct {
		name  string
		extra Sample
	}{
		{"negligible forecast", sample(noon(2026, 6, 23), cfg.MinForecast/2, 10)},
		{"after cutoff", sample(noon(2026, 6, 25), 0.5, 5)},
		{"outside window", sample(noon(2026, 6, 25).Add(-cfg.Window-day), 0.5, 5)},
		{"negative measurement", sample(noon(2026, 6, 23), 0.5, -1)},
		{"NaN measurement", sample(noon(2026, 6, 23), 0.5, math.NaN())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Learn(append(base, tc.extra), testLoc, cutoff, cfg)
			assert.InDelta(t, 0.5, m.Factor(noon(2026, 6, 25), 1).Factor, 1e-9)
			assert.Equal(t, 4, m.Samples)
		})
	}
}

func TestLearnRatioBounded(t *testing.T) {
	cfg := DefaultConfig
	cutoff := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	ss := repeat(4, func(i int) Sample { return sample(noon(2026, 6, 24-i), 0.5, 0.25) })
	ss[0].Measured = 50 // a 100x meter glitch on the most heavily weighted slot
	sameBin(t, cfg, ss...)

	m := Learn(ss, testLoc, cutoff, cfg)
	var wm, wf float64
	for i, s := range ss {
		w := math.Pow(0.5, cutoff.Sub(s.Slot).Hours()/24/14)
		meas := s.Measured
		if i == 0 {
			meas = cfg.MaxRatio * s.Forecast
		}
		wm += w * meas
		wf += w * s.Forecast
	}
	c := m.Factor(noon(2026, 6, 25), 1)
	assert.InDelta(t, cfg.clamp(wm/wf), c.Factor, 1e-9)
	assert.LessOrEqual(t, c.High, cfg.MaxFactor)
}

func TestFactorClamp(t *testing.T) {
	cfg := DefaultConfig
	cutoff := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ measured, want float64 }{
		{0.001, cfg.MinFactor}, // 0.002 -> clamped up
		{1.25, cfg.MaxFactor},  // 2.5 -> clamped down
		{0.4, 0.8},
	} {
		ss := repeat(4, func(i int) Sample { return sample(noon(2026, 6, 24-i), 0.5, tc.measured) })
		c := Learn(ss, testLoc, cutoff, cfg).Factor(noon(2026, 6, 25), 1)
		assert.InDelta(t, tc.want, c.Factor, 1e-9)
		assert.InDelta(t, tc.want, c.Low, 1e-9)
		assert.InDelta(t, tc.want, c.High, 1e-9)
	}
}

func TestFactorFallbackOrder(t *testing.T) {
	cfg := DefaultConfig
	cutoff := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	ss := repeat(4, func(i int) Sample { return sample(noon(2026, 6, 24-i), 0.5, 0.3) }) // 0.6
	m := Learn(ss, testLoc, cutoff, cfg)
	require.True(t, m.Learned())

	az, el := SunPosition(noon(2026, 6, 25).Add(SlotDuration/2), testLoc)
	k := cfg.key(az, el)

	// find slots on the same day whose bins are the learned one, a direct
	// neighbour and one further away
	var neighbour, far time.Time
	for ts := noon(2026, 6, 25).Add(-6 * time.Hour); ts.Before(noon(2026, 6, 25).Add(6 * time.Hour)); ts = ts.Add(SlotDuration) {
		a, e := SunPosition(ts.Add(SlotDuration/2), testLoc)
		if e <= 0 {
			continue
		}
		n := cfg.key(a, e)
		d := max(abs(n.az-k.az), abs(n.el-k.el))
		if d == 1 && neighbour.IsZero() {
			neighbour = ts
		}
		if d >= 3 && far.IsZero() {
			far = ts
		}
	}
	require.False(t, neighbour.IsZero())
	require.False(t, far.IsZero())

	assert.Equal(t, Correction{0.6, 0.6, 0.6, SourceLearned}, round(m.Factor(noon(2026, 6, 25), 0.9)))
	assert.Equal(t, Correction{0.6, 0.6, 0.6, SourceNeighbour}, round(m.Factor(neighbour, 0.9)))
	assert.Equal(t, Correction{0.9, 0.9, 0.9, SourceFallback}, m.Factor(far, 0.9))
	assert.Equal(t, Correction{1, 1, 1, SourceNight}, m.Factor(time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC), 0.9))

	// a bin with too little evidence of its own is not learned
	thin := Learn(ss[:cfg.MinSlots-1], testLoc, cutoff, cfg)
	assert.False(t, thin.Learned())
	assert.Equal(t, SourceFallback, thin.Factor(noon(2026, 6, 25), 0.9).Source)

	// no map at all
	var none *Map
	assert.Equal(t, Correction{0.9, 0.9, 0.9, SourceFallback}, none.Factor(noon(2026, 6, 25), 0.9))
	assert.False(t, none.Learned())
	assert.Nil(t, none.Bins())

	bins := m.Bins()
	require.NotEmpty(t, bins)
	var learned int
	for _, b := range bins {
		if b.Source == SourceLearned {
			learned++
			assert.Equal(t, 4, b.Slots)
			assert.Equal(t, ss[0].Slot, b.LastSample)
			assert.InDelta(t, 0.6, b.Factor, 1e-9)
		}
	}
	assert.Equal(t, 1, learned)
	assert.Len(t, bins, 9, "the learned bin and its eight neighbours")
}

func TestSeasonalWindow(t *testing.T) {
	cfg := DefaultConfig
	cutoff := time.Date(2027, 6, 25, 0, 0, 0, 0, time.UTC)

	hl := func(d time.Duration) float64 { return math.Pow(0.5, d.Hours()/24/14) }
	for _, tc := range []struct {
		name string
		slot time.Time
		want float64
	}{
		{"recent", cutoff.Add(-2 * day), hl(2 * day)},
		{"one year back", cutoff.AddDate(-1, 0, 0), 0.5},
		{"one year back, 10 days later", cutoff.AddDate(-1, 0, 10), 0.5 * hl(10*day)},
		{"one year back, 20 days earlier", cutoff.AddDate(-1, 0, -20), 0.5 * hl(20*day)},
		{"one year back, 22 days earlier", cutoff.AddDate(-1, 0, -22), 0},
		{"two years back", cutoff.AddDate(-2, 0, 0), 0.25},
		{"four years back", cutoff.AddDate(-4, 0, 0), 0},
		{"between windows", cutoff.AddDate(0, -6, 0), 0},
	} {
		assert.InDelta(t, tc.want, cfg.weight(tc.slot, cutoff), 1e-12, tc.name)
	}

	// a synthetic previous year teaches the map before any recent sample exists
	lastYear := repeat(4, func(i int) Sample { return sample(noon(2026, 6, 24-i), 0.5, 0.2) })
	m := Learn(lastYear, testLoc, cutoff, cfg)
	c := m.Factor(noon(2027, 6, 25), 1)
	assert.Equal(t, SourceLearned, c.Source)
	assert.InDelta(t, 0.4, c.Factor, 1e-9)

	// the same samples are ignored without the seasonal window
	cfg.SeasonYears = 0
	assert.False(t, Learn(lastYear, testLoc, cutoff, cfg).Learned())

	assert.Equal(t, []Window{
		{cutoff.Add(-DefaultConfig.Window), cutoff},
		{cutoff.AddDate(-1, 0, -21), cutoff.AddDate(-1, 0, 21)},
		{cutoff.AddDate(-2, 0, -21), cutoff.AddDate(-2, 0, 21)},
		{cutoff.AddDate(-3, 0, -21), cutoff.AddDate(-3, 0, 21)},
	}, DefaultConfig.TrainingWindows(cutoff))
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func round(c Correction) Correction {
	r := func(v float64) float64 { return math.Round(v*1e9) / 1e9 }
	return Correction{r(c.Factor), r(c.Low), r(c.High), c.Source}
}
