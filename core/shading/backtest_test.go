package shading

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/require"
)

// TestBacktest replays the ADR-016 backtest against a copy of a real evcc
// database. It is skipped unless EVCC_SHADING_DB points at a database file; the
// file is copied first and never opened directly. Optional EVCC_SHADING_LAT/LON
// override the coordinates (default: the ADR backtest's).
//
// Every test day learns only from samples before that day and is evaluated on
// its day-ahead (1440 min lead) forecast.
func TestBacktest(t *testing.T) {
	src := os.Getenv("EVCC_SHADING_DB")
	if src == "" {
		t.Skip("EVCC_SHADING_DB not set")
	}
	dst := filepath.Join(t.TempDir(), "evcc.db")
	copyFile(t, src, dst)
	require.NoError(t, db.NewInstance("sqlite", dst))
	t.Cleanup(func() { _ = db.Close() })

	loc := Location{49.2115, 9.1949}
	if v := os.Getenv("EVCC_SHADING_LAT"); v != "" {
		_, err := fmt.Sscan(v, &loc.Lat)
		require.NoError(t, err)
		_, err = fmt.Sscan(os.Getenv("EVCC_SHADING_LON"), &loc.Lon)
		require.NoError(t, err)
	}

	tz, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)
	first := time.Date(2026, 9, 24, 0, 0, 0, 0, tz)
	end := time.Date(2026, 10, 6, 0, 0, 0, 0, tz)

	ctx := context.Background()
	load := func(lead int) []Sample {
		rows, hasPV, err := metrics.QueryShadingSamples(ctx, lead, first.AddDate(-1, -3, 0), end)
		require.NoError(t, err)
		require.True(t, hasPV)
		res := make([]Sample, 0, len(rows))
		for _, r := range rows {
			res = append(res, Sample{Slot: r.Slot, Forecast: r.Forecast, Measured: r.Measured})
		}
		return res
	}
	byLead := map[int][]Sample{60: load(60), 1440: load(1440)}
	test := byLead[1440]

	// evcc's existing daily scale, exactly as querySolarScale computes it at d0
	dailyScale := func(d0 time.Time) float64 {
		series, err := metrics.QueryEnergy(d0.AddDate(0, 0, -30), d0, "day", true)
		require.NoError(t, err)
		pv, fc := map[string]float64{}, map[string]float64{}
		for _, s := range series {
			m := map[string]map[string]float64{metrics.PV: pv, metrics.Forecast: fc}[s.Group]
			for _, d := range s.Data {
				if m != nil {
					m[d.Start.Format(time.DateOnly)] = d.Energy
				}
			}
		}
		var ratios []float64
		for day, f := range fc {
			if p := pv[day]; day != d0.Format(time.DateOnly) && f > 0.5 && p > 0.5 {
				ratios = append(ratios, p/f)
			}
		}
		if len(ratios) < 14 {
			return 1
		}
		return metrics.Percentile(ratios, 0.5)
	}

	type result struct{ total, actual, absErr, dayErr float64 }
	run := func(name string, cfg Config, lead int, out io.Writer) result {
		var res result
		var days int
		for d0 := first; d0.Before(end); d0 = d0.AddDate(0, 0, 1) {
			d1 := d0.AddDate(0, 0, 1)
			scale := dailyScale(d0)
			var m *Map
			if lead > 0 {
				m = Learn(byLead[lead], loc, d0, cfg)
			}
			var total, actual float64
			for _, s := range test {
				if s.Slot.Before(d0) || !s.Slot.Before(d1) {
					continue
				}
				pred := s.Forecast
				switch {
				case name == "daily scale":
					pred *= scale
				case m != nil:
					pred *= m.Factor(s.Slot, scale).Factor
				}
				total += pred
				actual += s.Measured
				res.absErr += math.Abs(pred - s.Measured)
			}
			res.total += total
			res.actual += actual
			res.dayErr += math.Abs(total - actual)
			days++
		}
		res.dayErr /= float64(days)
		if out != nil {
			fmt.Fprintf(out, "%-44s total %6.1f vs actual %6.1f kWh | mean |daily err| %4.1f | slot abs err %3.0f%%\n",
				name, res.total, res.actual, res.dayErr, 100*res.absErr/res.actual)
		}
		return res
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\nADR-016 backtest %s..%s, test lead 1440 min, location %.4f/%.4f\n", first.Format(time.DateOnly), end.AddDate(0, 0, -1).Format(time.DateOnly), loc.Lat, loc.Lon)
	raw := run("raw", DefaultConfig, 0, &b)
	daily := run("daily scale", DefaultConfig, 0, &b)
	def := run(fmt.Sprintf("sun-position map (default, learn lead %d)", metrics.ShadingLeadMinutes), DefaultConfig, metrics.ShadingLeadMinutes, &b)
	run("sun-position map (learn lead 1440)", DefaultConfig, 1440, &b)

	fmt.Fprintf(&b, "\nparameter sweep (learn lead %d):\n", metrics.ShadingLeadMinutes)
	vary := func(name string, f func(*Config)) {
		cfg := DefaultConfig
		f(&cfg)
		run(name, cfg, metrics.ShadingLeadMinutes, &b)
	}
	vary("half-life 7d", func(c *Config) { c.HalfLife = 7 * 24 * time.Hour })
	vary("half-life 21d", func(c *Config) { c.HalfLife = 21 * 24 * time.Hour })
	vary("window 35d (reference script)", func(c *Config) { c.Window = 35 * 24 * time.Hour })
	vary("bins 15x5", func(c *Config) { c.AzimuthStep = 15 })
	vary("bins 5x5", func(c *Config) { c.AzimuthStep = 5 })
	vary("bins 10x10", func(c *Config) { c.ElevationStep = 10 })
	vary("no ratio bound", func(c *Config) { c.MaxRatio = math.Inf(1) })
	vary("min slots 8", func(c *Config) { c.MinSlots = 8 })
	vary("clamp [0.05, 1.2]", func(c *Config) { c.MaxFactor = 1.2 })

	// per-hour profile for the last test day
	day := time.Date(2026, 10, 5, 0, 0, 0, 0, tz)
	m := Learn(byLead[metrics.ShadingLeadMinutes], loc, day, DefaultConfig)
	scale := dailyScale(day)
	fmt.Fprintf(&b, "\n2026-10-05 per hour (kWh): daily scale %.3f\nhour  measured  raw  daily  corrected\n", scale)
	type hour struct{ meas, raw, daily, corr float64 }
	hours := make([]hour, 24)
	for _, s := range test {
		if s.Slot.Before(day) || !s.Slot.Before(day.AddDate(0, 0, 1)) {
			continue
		}
		h := &hours[s.Slot.In(tz).Hour()]
		h.meas += s.Measured
		h.raw += s.Forecast
		h.daily += s.Forecast * scale
		h.corr += s.Forecast * m.Factor(s.Slot, scale).Factor
	}
	var sum hour
	for i, h := range hours {
		if h.raw == 0 && h.meas == 0 {
			continue
		}
		fmt.Fprintf(&b, "%02d   %6.2f  %5.2f  %5.2f  %6.2f\n", i, h.meas, h.raw, h.daily, h.corr)
		sum.meas, sum.raw, sum.daily, sum.corr = sum.meas+h.meas, sum.raw+h.raw, sum.daily+h.daily, sum.corr+h.corr
	}
	fmt.Fprintf(&b, "sum  %6.2f  %5.2f  %5.2f  %6.2f\n", sum.meas, sum.raw, sum.daily, sum.corr)

	t.Log(b.String())

	// the map must not be worse than the raw forecast or the daily scale
	require.Less(t, def.absErr, raw.absErr)
	require.Less(t, def.absErr, daily.absErr)
	require.Less(t, math.Abs(def.total-def.actual), math.Abs(raw.total-raw.actual))
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	require.NoError(t, err)
	defer in.Close()
	out, err := os.Create(dst)
	require.NoError(t, err)
	_, err = io.Copy(out, in)
	require.NoError(t, err)
	require.NoError(t, out.Close())
}

// TestDumpFactors writes the per-slot factors and bands a map learned at the
// start of EVCC_SHADING_DAY would apply over the following three days, for
// replaying stored optimizer requests. Skipped unless EVCC_SHADING_DB and
// EVCC_SHADING_FACTORS_OUT are set; the database is copied first.
func TestDumpFactors(t *testing.T) {
	src, out := os.Getenv("EVCC_SHADING_DB"), os.Getenv("EVCC_SHADING_FACTORS_OUT")
	if src == "" || out == "" {
		t.Skip("EVCC_SHADING_DB/EVCC_SHADING_FACTORS_OUT not set")
	}
	dst := filepath.Join(t.TempDir(), "evcc.db")
	copyFile(t, src, dst)
	require.NoError(t, db.NewInstance("sqlite", dst))
	t.Cleanup(func() { _ = db.Close() })

	tz, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)
	day, err := time.ParseInLocation(time.DateOnly, os.Getenv("EVCC_SHADING_DAY"), tz)
	require.NoError(t, err)
	loc := Location{49.1579, 9.1737} // the live forecast-solar configuration

	cfg := DefaultConfig
	var samples []Sample
	for _, w := range cfg.TrainingWindows(day) {
		rows, _, err := metrics.QueryShadingSamples(context.Background(), metrics.ShadingLeadMinutes, w.From, w.To)
		require.NoError(t, err)
		for _, r := range rows {
			samples = append(samples, Sample{Slot: r.Slot, Forecast: r.Forecast, Measured: r.Measured})
		}
	}
	m := Learn(samples, loc, day, cfg)

	type row struct {
		Slot   int64   `json:"slot"`
		Factor float64 `json:"factor"`
		Low    float64 `json:"low"`
		High   float64 `json:"high"`
		Source string  `json:"source"`
	}
	var res []row
	for ts := day; ts.Before(day.AddDate(0, 0, 3)); ts = ts.Add(SlotDuration) {
		c := m.Factor(ts, 1) // the caller substitutes its daily scale where Source is fallback
		res = append(res, row{ts.Unix(), c.Factor, c.Low, c.High, c.Source})
	}
	b, err := json.Marshal(res)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(out, b, 0o600))
}
