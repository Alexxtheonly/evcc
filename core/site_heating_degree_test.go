package core

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func berlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)
	return loc
}

// heatingDays returns slot samples for consecutive local days from first: the
// day's temperature is temp(d) for every slot and each slot's energy is
// base + slope x HDD (Wh), plus a daily load cycle that does not depend on
// temperature. skip(d, i) drops slot i of day d.
func heatingDays(loc *time.Location, first time.Time, days int, temp func(d int) float64, base, slope float64, skip func(d, i int) bool) []metrics.TemperatureSample {
	var res []metrics.TemperatureSample
	for d := range days {
		day := time.Date(first.Year(), first.Month(), first.Day()+d, 0, 0, 0, 0, loc)
		next := day.AddDate(0, 0, 1)
		for ts, i := day, 0; ts.Before(next); ts, i = ts.Add(tariff.SlotDuration), i+1 {
			if skip != nil && skip(d, i) {
				continue
			}
			cycle := base / 2 * math.Sin(float64(i)/96*2*math.Pi)
			res = append(res, metrics.TemperatureSample{Ts: ts.Unix(), Energy: base + cycle + slope*heatingDegree(temp(d)), Temperature: temp(d)})
		}
	}
	return res
}

// winterTemp varies day to day between -1 and 9 °C.
func winterTemp(d int) float64 { return 4 + 5*math.Sin(float64(d)*1.3) }

func TestHeatingDegreeDaysDST(t *testing.T) {
	loc := berlin(t)
	// 2026-03-29 has 92 slots, 2026-10-25 100 slots
	for _, c := range []struct {
		date   time.Time
		length int
	}{
		{time.Date(2026, 3, 28, 0, 0, 0, 0, loc), 96},
		{time.Date(2026, 3, 29, 0, 0, 0, 0, loc), 92},
		{time.Date(2026, 10, 25, 0, 0, 0, 0, loc), 100},
	} {
		samples := heatingDays(loc, c.date, 1, func(int) float64 { return 5 }, 100, 0, nil)
		require.Len(t, samples, c.length)
		days := heatingDegreeDays(samples, loc)
		require.Len(t, days, 1)
		assert.Equal(t, c.length, days[0].Length, c.date)
		assert.True(t, days[0].Complete)
	}

	t.Run("incomplete days: 90 % of the slots make a day; energy scaled by length/present", func(t *testing.T) {
		day := time.Date(2026, 10, 25, 0, 0, 0, 0, loc) // 100 slots
		// 90 present: complete; energy scaled 100/90
		samples := heatingDays(loc, day, 1, func(int) float64 { return 20 }, 90, 0, func(_, i int) bool { return i < 10 })
		for i := range samples {
			samples[i].Energy = 90
		}
		days := heatingDegreeDays(samples, loc)
		require.Len(t, days, 1)
		assert.True(t, days[0].Complete)
		assert.Equal(t, 90, days[0].Slots)
		assert.InDelta(t, 9000, days[0].Energy, 1e-9, "90 slots x 90 Wh x 100/90")

		// 89 present: incomplete
		samples = heatingDays(loc, day, 1, func(int) float64 { return 20 }, 90, 0, func(_, i int) bool { return i < 11 })
		assert.False(t, heatingDegreeDays(samples, loc)[0].Complete)

		// 92-slot day needs 83 (82.8)
		dst := time.Date(2026, 3, 29, 0, 0, 0, 0, loc)
		samples = heatingDays(loc, dst, 1, func(int) float64 { return 20 }, 90, 0, func(_, i int) bool { return i < 9 })
		assert.True(t, heatingDegreeDays(samples, loc)[0].Complete, "83/92")
		samples = heatingDays(loc, dst, 1, func(int) float64 { return 20 }, 90, 0, func(_, i int) bool { return i < 10 })
		assert.False(t, heatingDegreeDays(samples, loc)[0].Complete, "82/92")
	})

	t.Run("daily HDD is the mean slot HDD", func(t *testing.T) {
		day := time.Date(2026, 2, 3, 0, 0, 0, 0, loc)
		samples := heatingDays(loc, day, 1, func(int) float64 { return 5 }, 100, 0, nil)
		for i := range samples {
			samples[i].Temperature = []float64{5, 25}[i%2] // HDD 10 and 0
		}
		assert.InDelta(t, 5, heatingDegreeDays(samples, loc)[0].HDD, 1e-9)
	})

	t.Run("dates by local time, not UTC", func(t *testing.T) {
		// 23:30 local on Feb 3 is 22:30 UTC: still Feb 3
		ts := time.Date(2026, 2, 3, 23, 30, 0, 0, loc)
		days := heatingDegreeDays([]metrics.TemperatureSample{{Ts: ts.Unix(), Energy: 1, Temperature: 1}}, loc)
		assert.Equal(t, time.Date(2026, 2, 3, 0, 0, 0, 0, loc), days[0].Date)
	})
}

func TestAnalyzeHeatingDegreeGate(t *testing.T) {
	loc := berlin(t)
	first := time.Date(2026, 1, 10, 0, 0, 0, 0, loc)

	t.Run("heating season: open", func(t *testing.T) {
		a := analyzeHeatingDegree(heatingDays(loc, first, 30, winterTemp, 300, 30, nil), loc)
		assert.True(t, a.Open, a.Reason)
		assert.Equal(t, 30, a.CompleteDays)
		assert.Equal(t, 30*96, a.Samples)
		require.NotNil(t, a.DailyR)
		assert.InDelta(t, 1, *a.DailyR, 1e-9)
		assert.InDelta(t, 30, a.Fit.slope, 1e-9)
		require.NotNil(t, a.SlotR)
		assert.Less(t, *a.SlotR, 0.95, "the daily cycle dilutes the slot r")
	})

	t.Run("fewer than 20 complete days: shut", func(t *testing.T) {
		// 30 days, 11 of them incomplete: 19 complete, still 2000+ samples
		samples := heatingDays(loc, first, 30, winterTemp, 300, 30, func(d, i int) bool { return d < 11 && i%2 == 0 })
		require.GreaterOrEqual(t, len(samples), heatingDegreeMinSamples)
		a := analyzeHeatingDegree(samples, loc)
		assert.Equal(t, 19, a.CompleteDays)
		assert.False(t, a.Open)
		assert.Equal(t, heatingDegreeFewDays, a.Reason)

		samples = heatingDays(loc, first, 30, winterTemp, 300, 30, func(d, i int) bool { return d < 10 && i%2 == 0 })
		assert.True(t, analyzeHeatingDegree(samples, loc).Open, "20 complete days suffice")
	})

	t.Run("fewer than 2000 samples: shut", func(t *testing.T) {
		// 22 complete days at 90 of 96 slots: 1980 samples
		samples := heatingDays(loc, first, 22, winterTemp, 300, 30, func(_, i int) bool { return i < 6 })
		require.Len(t, samples, 1980)
		a := analyzeHeatingDegree(samples, loc)
		assert.Equal(t, 22, a.CompleteDays)
		assert.False(t, a.Open)
		assert.Equal(t, heatingDegreeFewSamples, a.Reason)

		samples = heatingDays(loc, first, 22, winterTemp, 300, 30, func(_, i int) bool { return i < 5 })
		require.Len(t, samples, 2002)
		assert.True(t, analyzeHeatingDegree(samples, loc).Open)
	})

	t.Run("daily r below 0.3: shut", func(t *testing.T) {
		// load follows a cycle unrelated to the temperature; a weak heating share
		samples := heatingDays(loc, first, 30, winterTemp, 300, 2, nil)
		for i := range samples {
			d := i / 96
			samples[i].Energy += 100 * math.Cos(float64(d)*2.1)
		}
		a := analyzeHeatingDegree(samples, loc)
		require.NotNil(t, a.DailyR)
		assert.Less(t, *a.DailyR, heatingDegreeMinDailyCorrelation)
		assert.False(t, a.Open)
		assert.Equal(t, heatingDegreeDailyRLow, a.Reason)
	})

	t.Run("no temperature variance: shut on daily r", func(t *testing.T) {
		a := analyzeHeatingDegree(heatingDays(loc, first, 30, func(int) float64 { return 5 }, 300, 30, nil), loc)
		assert.Nil(t, a.DailyR)
		assert.Equal(t, heatingDegreeDailyRLow, a.Reason)
	})

	t.Run("daily r passes but the slot slope is not positive: shut", func(t *testing.T) {
		// daily totals rise with HDD, but within each day load is highest in the warm
		// half-hours: the slot fit's slope is negative (the 2026-04-30 -> 06-03 days)
		var samples []metrics.TemperatureSample
		for d := range 30 {
			day := first.AddDate(0, 0, d)
			for i := range 96 {
				warm := i%2 == 0
				temp := winterTemp(d) + map[bool]float64{true: 12, false: -12}[warm]
				energy := 300 + 3*heatingDegree(winterTemp(d)) + map[bool]float64{true: 200, false: 0}[warm]
				samples = append(samples, metrics.TemperatureSample{Ts: day.Add(time.Duration(i) * tariff.SlotDuration).Unix(), Energy: energy, Temperature: temp})
			}
		}
		a := analyzeHeatingDegree(samples, loc)
		require.NotNil(t, a.DailyR)
		assert.GreaterOrEqual(t, *a.DailyR, heatingDegreeMinDailyCorrelation)
		assert.Less(t, a.Fit.slope, 0.0)
		assert.False(t, a.Open)
		assert.Equal(t, heatingDegreeSlopeSign, a.Reason)
	})
}

func TestCapHeatingDegree(t *testing.T) {
	loc := berlin(t)
	start := time.Date(2026, 2, 3, 23, 0, 0, 0, loc) // 4 slots on Feb 3, then Feb 4

	t.Run("per local date: floor 2 kWh x n/96 or 25 %, scaled proportionally", func(t *testing.T) {
		profile := make([]float64, 4+96)
		corrections := make([]float64, len(profile))
		for i := range profile {
			profile[i] = 0.05 // Feb 4: 4.8 kWh, 25 % = 1.2 kWh < 2 kWh floor
			corrections[i] = 0.1
		}
		profile[0], profile[2], profile[3] = 1, 1, 1 // Feb 3: 3.05 kWh, 25 % = 0.7625 kWh
		corrections[1] = math.NaN()                  // no forecast: counts as zero, stays NaN
		days := capHeatingDegree(profile, corrections, start, loc)
		require.Len(t, days, 2)

		// Feb 3: n=4, cap max(2 x 4/96, 0.7625) = 0.7625 > sum 0.3: unchanged
		assert.Equal(t, "2026-02-03", days[0].Date)
		assert.Equal(t, 4, days[0].Slots)
		assert.InDelta(t, 3.05, days[0].Uncorrected, 1e-12)
		assert.InDelta(t, 0.3, days[0].Uncapped, 1e-12)
		assert.InDelta(t, 0.7625, days[0].Cap, 1e-12)
		assert.False(t, days[0].Capped)
		assert.Equal(t, 0.1, corrections[0])
		assert.True(t, math.IsNaN(corrections[1]))

		// Feb 4: n=96, sum 9.6 > cap max(2, 1.2) = 2: the floor binds, each slot x 2/9.6
		assert.Equal(t, "2026-02-04", days[1].Date)
		assert.InDelta(t, 2, days[1].Cap, 1e-12)
		assert.InDelta(t, 9.6, days[1].Uncapped, 1e-9)
		assert.InDelta(t, 2, days[1].Correction, 1e-9)
		assert.True(t, days[1].Capped)
		assert.InDelta(t, 0.1*2/9.6, corrections[50], 1e-12)

		// a short date (4 slots) at the floor: 2 x 4/96
		small := []float64{0.01, 0.01, 0.01, 0.01}
		c := []float64{0.05, 0.05, 0.05, 0.05}
		d := capHeatingDegree(small, c, start, loc)
		assert.InDelta(t, 2.0*4/96, d[0].Cap, 1e-12)
		assert.InDelta(t, 2.0*4/96, d[0].Correction, 1e-12)
	})

	t.Run("share binds, negative sums capped by magnitude", func(t *testing.T) {
		profile := make([]float64, 96)
		corrections := make([]float64, 96)
		for i := range profile {
			profile[i] = 0.4 // 38.4 kWh: 25 % = 9.6 kWh
			corrections[i] = -0.2
		}
		days := capHeatingDegree(profile, corrections, time.Date(2026, 2, 4, 0, 0, 0, 0, loc), loc)
		require.Len(t, days, 1)
		assert.InDelta(t, 9.6, days[0].Cap, 1e-9)
		assert.InDelta(t, -9.6, days[0].Correction, 1e-9)
		assert.InDelta(t, -0.1, corrections[0], 1e-12)
	})

	t.Run("within the cap: unchanged", func(t *testing.T) {
		profile := []float64{1, 1, 1, 1}
		corrections := []float64{0.1, -0.05, 0.02, math.NaN()}
		days := capHeatingDegree(profile, corrections, start, loc)
		assert.False(t, days[0].Capped)
		assert.InDelta(t, 0.07, days[0].Correction, 1e-12)
		assert.Equal(t, 0.1, corrections[0])
	})

	t.Run("clamp first, then cap: scaled slots stay within their clamp", func(t *testing.T) {
		profile := make([]float64, 96)
		for i := range profile {
			profile[i] = 0.1 + 0.01*float64(i%10)
		}
		temps := api.Rates{}
		day := time.Date(2026, 2, 4, 0, 0, 0, 0, loc)
		for i := range 96 {
			s := day.Add(time.Duration(i) * tariff.SlotDuration)
			temps = append(temps, api.Rate{Start: s, End: s.Add(tariff.SlotDuration), Value: -15})
		}
		fit := heatingDegreeFit{slope: 400, meanHDD: 5} // +10 kWh per slot raw
		got, days := correctHeatingDegreeOn(profile, day, temps, fit, loc)
		var uncorrected float64
		for i := range profile {
			uncorrected += profile[i]
			assert.LessOrEqual(t, got[i], 2*profile[i]+1e-12, "never above the per-slot clamp")
			assert.GreaterOrEqual(t, got[i], profile[i])
		}
		require.Len(t, days, 1)
		assert.InDelta(t, uncorrected, days[0].Uncapped, 1e-9, "clamped at +100 % per slot before the cap")
		assert.True(t, days[0].Capped)
		assert.InDelta(t, max(2, 0.25*uncorrected), days[0].Correction, 1e-9)
		var sum float64
		for i := range got {
			sum += got[i] - profile[i]
		}
		assert.InDelta(t, days[0].Correction, sum, 1e-9)
		// proportional: each slot gets the same share of its clamp
		ratio := (got[0] - profile[0]) / profile[0]
		for i := range got {
			assert.InDelta(t, ratio, (got[i]-profile[i])/profile[i], 1e-9)
		}
	})
}

func heatingDegreeTestSite(t *testing.T, temps api.Rates, auto heatingDegreeResult, daily *heatingDegreeAnalysis) *Site {
	t.Helper()
	ctrl := gomock.NewController(t)
	temp := api.NewMockTariff(ctrl)
	temp.EXPECT().Type().Return(api.TariffTypeTemperature).AnyTimes()
	temp.EXPECT().Rates().Return(temps, nil).AnyTimes()
	site := &Site{log: util.NewLogger("foo"), tariffs: &tariff.Tariffs{Temperature: temp}}
	site.heatingDegreeCached = func() (heatingDegreeResult, error) { return auto, nil }
	site.heatingDegreeDaily = func() (*heatingDegreeAnalysis, error) { return daily, nil }
	return site
}

func coldRates(n int, temp float64) api.Rates {
	start := time.Now().Truncate(tariff.SlotDuration)
	var res api.Rates
	for i := range n {
		s := start.Add(time.Duration(i) * tariff.SlotDuration)
		res = append(res, api.Rate{Start: s, End: s.Add(tariff.SlotDuration), Value: temp})
	}
	return res
}

func TestHeatingDegreeModes(t *testing.T) {
	profile := make([]float64, 192)
	for i := range profile {
		profile[i] = 0.3
	}
	fit := heatingDegreeFit{slope: 20, meanHDD: 5}
	open := &heatingDegreeAnalysis{Open: true, Fit: fit}
	site := heatingDegreeTestSite(t, coldRates(192, 0), heatingDegreeResult{fit: fit, ok: true}, open)

	t.Run("off never corrects, even with both gates open", func(t *testing.T) {
		got, days := site.heatingDegreeProfile(heatingDegreeOff, profile)
		assert.Equal(t, profile, got)
		assert.Nil(t, days)

		active, shadow := site.heatingDegreeVariants(heatingDegreeOff, profile)
		assert.Equal(t, profile, active)
		assert.Len(t, shadow, 2, "both variants archived as shadow")
		assert.NotEqual(t, profile, shadow[heatingDegreeAuto])
		assert.NotEqual(t, profile, shadow[heatingDegreeOn])
	})

	t.Run("on corrects with the cap, auto without", func(t *testing.T) {
		on, days := site.heatingDegreeProfile(heatingDegreeOn, profile)
		auto, _ := site.heatingDegreeProfile(heatingDegreeAuto, profile)
		require.NotEmpty(t, days)
		// +0.2 kWh per slot raw (HDD 15 vs 5 x 20 Wh); a full day: 19.2 kWh vs cap max(2, 7.2)
		assert.InDelta(t, 0.5, auto[100], 1e-12)
		assert.Less(t, on[100], auto[100])
		for _, d := range days {
			if d.Slots == 96 {
				assert.True(t, d.Capped)
				assert.InDelta(t, 7.2, d.Correction, 1e-9)
			}
		}
	})

	t.Run("on: gate shut means unchanged", func(t *testing.T) {
		shut := heatingDegreeTestSite(t, coldRates(192, 0), heatingDegreeResult{fit: fit, ok: true}, &heatingDegreeAnalysis{Reason: heatingDegreeDailyRLow, Fit: fit})
		got, days := shut.heatingDegreeProfile(heatingDegreeOn, profile)
		assert.Equal(t, profile, got)
		assert.Nil(t, days)
	})

	t.Run("on: no temperature forecast means unchanged", func(t *testing.T) {
		none := &Site{log: util.NewLogger("foo"), tariffs: &tariff.Tariffs{}}
		none.heatingDegreeDaily = func() (*heatingDegreeAnalysis, error) { return open, nil }
		got, _ := none.heatingDegreeProfile(heatingDegreeOn, profile)
		assert.Equal(t, profile, got)
	})

	t.Run("variants per mode", func(t *testing.T) {
		assert.Equal(t, []string{heatingDegreeOn}, heatingDegreeShadowVariants(""))
		assert.Equal(t, []string{heatingDegreeOn}, heatingDegreeShadowVariants(heatingDegreeAuto))
		assert.Equal(t, []string{heatingDegreeAuto}, heatingDegreeShadowVariants(heatingDegreeOn))
		assert.Equal(t, []string{heatingDegreeAuto, heatingDegreeOn}, heatingDegreeShadowVariants(heatingDegreeOff))

		active, shadow := site.heatingDegreeVariants(heatingDegreeOn, profile)
		on, _ := site.heatingDegreeProfile(heatingDegreeOn, profile)
		auto, _ := site.heatingDegreeProfile(heatingDegreeAuto, profile)
		assert.Equal(t, on, active)
		assert.Equal(t, map[string][]float64{heatingDegreeAuto: auto}, shadow)

		active, shadow = site.heatingDegreeVariants("", profile)
		assert.Equal(t, auto, active)
		assert.Equal(t, map[string][]float64{heatingDegreeOn: on}, shadow)
	})

	t.Run("status breakdown records both corrections per date", func(t *testing.T) {
		site.heatingDegreeVariants(heatingDegreeAuto, profile)
		days := site.heatingDegreeLast.days
		require.NotEmpty(t, days)
		var auto, on float64
		for _, d := range days {
			auto += d.Auto
			on += d.Correction
			assert.True(t, d.Applied)
			assert.True(t, d.AutoApplied)
		}
		assert.InDelta(t, 0.2*192, auto, 1e-9)
		assert.Less(t, on, auto)
	})
}

// shadowSite is a site on an in-memory database with a request of n slots
// from start, home forecasts per variant (Wh per slot) and a flat profile.
func shadowSite(t *testing.T, start time.Time, n int, mode string) (*Site, optimizer.OptimizationInput, requestDetails) {
	t.Helper()
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())

	site := &Site{log: util.NewLogger("foo")}
	site.energyInsights.Settings.HeatingDegree = mode
	site.energyProfile = &metrics.HomeForecastResult{}
	req := optimizer.OptimizationInput{TimeSeries: optimizer.TimeSeries{Dt: make([]int, n), Ft: make([]float32, n), PN: make([]float32, n)}}
	var details requestDetails
	home := map[string][]float64{heatingDegreeOff: make([]float64, n), heatingDegreeAuto: make([]float64, n), heatingDegreeOn: make([]float64, n)}
	for i := range n {
		s := start.Add(time.Duration(i) * tariff.SlotDuration)
		details.Timestamps = append(details.Timestamps, s)
		req.TimeSeries.Dt[i] = int(tariff.SlotDuration.Seconds())
		site.energyProfile.Rates = append(site.energyProfile.Rates, metrics.HomeForecastSlot{Start: s, Low: 100, Base: 200, High: 400})
		home[heatingDegreeOff][i], home[heatingDegreeAuto][i], home[heatingDegreeOn][i] = 200, 250, 230
	}
	site.energyHome = home[heatingDegreeMode(mode)]
	site.energyHomeShadow = make(map[string][]float64)
	for _, v := range heatingDegreeShadowVariants(mode) {
		site.energyHomeShadow[v] = home[v]
	}
	return site, req, details
}

type shadowRow struct {
	Slot        int64
	LeadMinutes int
	Variant     string
	Issued      int64
	Base        float64
	Low         float64
	High        float64
}

func shadowRows(t *testing.T) []shadowRow {
	t.Helper()
	var rows []shadowRow
	require.NoError(t, db.Instance.Table("home_forecast_shadow_samples").Order("variant, lead_minutes").Scan(&rows).Error)
	return rows
}

func TestHeatingDegreeShadowArchive(t *testing.T) {
	loc := berlin(t)
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = loc

	t.Run("leads: the variant not in use, the active one only in home_forecast_samples", func(t *testing.T) {
		for _, c := range []struct {
			mode   string
			want   map[string]float64 // variant -> base kWh
			active float64
		}{
			{"", map[string]float64{heatingDegreeOn: 0.23}, 0.25},
			{heatingDegreeAuto, map[string]float64{heatingDegreeOn: 0.23}, 0.25},
			{heatingDegreeOn, map[string]float64{heatingDegreeAuto: 0.25}, 0.23},
			{heatingDegreeOff, map[string]float64{heatingDegreeAuto: 0.25, heatingDegreeOn: 0.23}, 0.2},
		} {
			issued := time.Date(2026, 2, 3, 9, 7, 0, 0, loc) // no snapshot period
			site, req, details := shadowSite(t, issued.Truncate(tariff.SlotDuration), 192, c.mode)
			site.archiveHeatingDegreeShadow(req, details, issued)

			rows := shadowRows(t)
			require.Len(t, rows, 3*len(c.want), c.mode)
			for _, r := range rows {
				want, ok := c.want[r.Variant]
				require.True(t, ok, "mode %q archived %s", c.mode, r.Variant)
				assert.InDelta(t, want, r.Base, 1e-12)
				// the profile band (100/200/400) shifted by the correction
				assert.InDelta(t, want-0.1, r.Low, 1e-12)
				assert.InDelta(t, want+0.2, r.High, 1e-12)
				assert.Contains(t, []int{60, 360, 1440}, r.LeadMinutes)
			}
			var n int64
			require.NoError(t, db.Instance.Table("home_forecast_samples").Count(&n).Error)
			assert.Zero(t, n, "the shadow never writes home_forecast_samples")

			// energyForecast archives the active forecast there, unchanged by the shadow
			site, req, details = shadowSite(t, time.Now().Truncate(tariff.SlotDuration), 192, c.mode)
			require.NoError(t, site.energyForecast(req, details))
			var bases []float64
			require.NoError(t, db.Instance.Table("home_forecast_samples").Pluck("base", &bases).Error)
			require.NotEmpty(t, bases)
			for _, b := range bases {
				assert.InDelta(t, c.active, b, 1e-12, c.mode)
			}
		}
	})

	t.Run("horizon snapshots of auto and on at 00:00 and at the first 12:00 run covering the night", func(t *testing.T) {
		day := time.Date(2026, 2, 3, 0, 0, 0, 0, loc)
		for _, mode := range []string{heatingDegreeAuto, heatingDegreeOn, heatingDegreeOff} {
			// midnight: horizon to 24:00 covers the day
			issued := day.Add(2 * time.Minute)
			site, req, details := shadowSite(t, day, 96, mode)
			site.archiveHeatingDegreeShadow(req, details, issued)
			byVariant := map[string]int{}
			for _, r := range shadowRows(t) {
				byVariant[r.Variant]++
			}
			assert.Equal(t, 95+2, byVariant[heatingDegreeAuto], mode)
			assert.Equal(t, 95+2, byVariant[heatingDegreeOn], mode)
			assert.True(t, site.heatingDegreeSnapshot.Equal(day))

			// a second run in the same period takes none
			site.archiveHeatingDegreeShadow(req, details, issued.Add(10*time.Minute))
			assert.Len(t, shadowRows(t), 2*97, "no new snapshot rows (the lead targets are taken)")

			// after a restart the database says the period is done
			site.heatingDegreeSnapshot = time.Time{}
			assert.False(t, site.heatingDegreeSnapshotDue(issued.Add(20*time.Minute), day.AddDate(0, 0, 1)))
		}

		// noon: a horizon ending at midnight does not cover the night, the first run after
		// the day-ahead prices does
		site, req, details := shadowSite(t, day.Add(12*time.Hour), 48, heatingDegreeAuto)
		site.archiveHeatingDegreeShadow(req, details, day.Add(12*time.Hour+time.Minute))
		for _, r := range shadowRows(t) {
			assert.Contains(t, []int{60, 360}, r.LeadMinutes, "lead rows only")
		}
		site, req, details = shadowSite(t, day.Add(13*time.Hour), 44+96, heatingDegreeAuto)
		site.archiveHeatingDegreeShadow(req, details, day.Add(13*time.Hour+time.Minute))
		assert.Len(t, shadowRows(t), 2*(44+96-1+3), "both variants' horizon and lead rows")
		assert.True(t, site.heatingDegreeSnapshot.Equal(day.Add(12*time.Hour)))

		// after 17:00 no snapshot
		site, req, details = shadowSite(t, day.Add(17*time.Hour), 96, heatingDegreeAuto)
		assert.False(t, site.heatingDegreeSnapshotDue(day.Add(17*time.Hour+time.Minute), day.AddDate(0, 0, 2)))
		site.archiveHeatingDegreeShadow(req, details, day.Add(17*time.Hour+time.Minute))
		assert.Len(t, shadowRows(t), 2, "on lead rows only (1440 min is beyond the horizon)")
	})
}

func TestHeatingDegreeSnapshotPeriod(t *testing.T) {
	loc := berlin(t)
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = loc

	day := time.Date(2026, 10, 25, 0, 0, 0, 0, loc) // 25 hours
	from, until, start, end, ok := heatingDegreeSnapshotPeriod(day.Add(30 * time.Minute))
	require.True(t, ok)
	assert.Equal(t, day, from)
	assert.Equal(t, day.Add(time.Hour), until)
	assert.Equal(t, day, start)
	assert.Equal(t, time.Date(2026, 10, 26, 0, 0, 0, 0, loc), end)
	assert.Equal(t, 25*time.Hour, end.Sub(start))

	_, _, start, end, ok = heatingDegreeSnapshotPeriod(time.Date(2026, 10, 25, 14, 0, 0, 0, loc))
	require.True(t, ok)
	assert.Equal(t, time.Date(2026, 10, 25, 17, 0, 0, 0, loc), start)
	assert.Equal(t, time.Date(2026, 10, 26, 7, 0, 0, 0, loc), end)

	for _, h := range []int{1, 11, 17, 23} {
		_, _, _, _, ok := heatingDegreeSnapshotPeriod(time.Date(2026, 10, 25, h, 0, 0, 0, loc))
		assert.False(t, ok, h)
	}
}

func TestBlockBootstrapMean(t *testing.T) {
	d := make([]float64, 30)
	for i := range d {
		d[i] = -0.3 + 0.1*math.Sin(float64(i))
	}
	low, high := blockBootstrapMean(d, 7, 10000)
	var mean float64
	for _, x := range d {
		mean += x / float64(len(d))
	}
	assert.Less(t, low, mean)
	assert.Greater(t, high, mean)
	assert.Less(t, high, 0.0, "a consistent improvement excludes zero")
	l2, h2 := blockBootstrapMean(d, 7, 10000)
	assert.Equal(t, low, l2, "fixed seed")
	assert.Equal(t, high, h2)

	constant := []float64{0.5, 0.5, 0.5}
	low, high = blockBootstrapMean(constant, 7, 100)
	assert.Equal(t, 0.5, low)
	assert.Equal(t, 0.5, high)

	assert.InDelta(t, 2.5, percentileLinear([]float64{1, 2, 3, 4}, 0.5), 1e-12)
	assert.InDelta(t, 1.075, percentileLinear([]float64{1, 2, 3, 4}, 0.025), 1e-12)
}

func TestScoreHeatingDegreeShadow(t *testing.T) {
	window := func(missing int, actual, on, auto float64) metrics.HomeForecastShadowWindow {
		return metrics.HomeForecastShadowWindow{Actual: actual, Missing: missing, Variants: map[string]float64{heatingDegreeOn: on, heatingDegreeAuto: auto}}
	}
	var windows []metrics.HomeForecastShadowWindow
	for i := range 13 {
		windows = append(windows, window(0, 20, 21, 23+float64(i%3)))
	}
	windows = append(windows, window(5, 20, 50, 20)) // too many gaps: not scored

	s := scoreHeatingDegreeShadow("day", 4, windows)
	assert.Equal(t, 13, s.Windows)
	assert.False(t, s.Ready, "collecting (13/14)")
	assert.Nil(t, s.Delta)

	windows = append(windows, window(4, 20, 21, 24))
	s = scoreHeatingDegreeShadow("day", 4, windows)
	assert.Equal(t, 14, s.Windows)
	require.True(t, s.Ready)
	assert.InDelta(t, 1, *s.MAEOn, 1e-12)
	assert.Greater(t, *s.MAEAuto, *s.MAEOn)
	assert.InDelta(t, *s.MAEOn-*s.MAEAuto, *s.Delta, 1e-12)
	assert.LessOrEqual(t, *s.CILow, *s.Delta)
	assert.GreaterOrEqual(t, *s.CIHigh, *s.Delta)
	assert.Equal(t, 14, s.Required)
}

func TestHeatingDegreeStatus(t *testing.T) {
	loc := berlin(t)
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = loc
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())

	profile := make([]float64, 96)
	for i := range profile {
		profile[i] = 0.3
	}
	samples := heatingDays(loc, time.Date(2026, 1, 10, 0, 0, 0, 0, loc), 30, winterTemp, 300, 30, nil)
	a := analyzeHeatingDegree(samples, loc)
	a.From, a.Computed = time.Date(2026, 1, 10, 0, 0, 0, 0, loc), time.Date(2026, 2, 9, 0, 1, 0, 0, loc)
	site := heatingDegreeTestSite(t, coldRates(96, 0), heatingDegreeResult{}, &a)
	site.heatingDegreeVariants(heatingDegreeOn, profile)

	st := site.HeatingDegree()
	assert.Equal(t, heatingDegreeAuto, st.Mode, "nothing saved: auto")
	assert.True(t, st.TemperatureForecast)
	assert.Equal(t, "none", st.Applied, "auto's slot gate is shut and the setting is not on")
	assert.True(t, st.Open)
	assert.Empty(t, st.Reason)
	assert.Equal(t, 30, st.CompleteDays)
	assert.Equal(t, 30*96, st.Samples)
	require.NotNil(t, st.SlopeWhPerSlotK)
	assert.InDelta(t, 30, *st.SlopeWhPerSlotK, 1e-9)
	assert.InDelta(t, 30*96/1e3, *st.SlopeKWhPerDayK, 1e-9)
	assert.InDelta(t, a.Fit.meanHDD, *st.MeanHDD, 1e-12)
	require.NotEmpty(t, st.Days)
	var correction float64
	for _, d := range st.Days {
		correction += d.Correction
		assert.True(t, d.Applied)
		assert.Zero(t, d.Auto)
	}
	assert.Positive(t, correction)
	require.Len(t, st.Shadow, 2)
	assert.Equal(t, "day", st.Shadow[0].Window)
	assert.Equal(t, "overnight", st.Shadow[1].Window)
	assert.Zero(t, st.Shadow[0].Windows)
	assert.False(t, st.Shadow[0].Ready)

	b, err := json.Marshal(st)
	require.NoError(t, err)
	for _, key := range []string{`"mode":"auto"`, `"open":true`, `"completeDays":30`, `"capKWh":`, `"shadow":[{"window":"day"`} {
		assert.Contains(t, string(b), key)
	}

	t.Run("shut gate reports its reason", func(t *testing.T) {
		shut := analyzeHeatingDegree(samples[:15*96], loc)
		site.heatingDegreeDaily = func() (*heatingDegreeAnalysis, error) { return &shut, nil }
		st := site.HeatingDegree()
		assert.False(t, st.Open)
		assert.Equal(t, heatingDegreeFewDays, st.Reason)
		assert.Equal(t, 15, st.CompleteDays)
	})
}

// TestHeatingDegreeParity compares the "on" gate and correction with the Python
// backtest harness's T3dg on the same synthetic February-shaped input
// (testdata/heating_degree_parity.py writes the fixture).
func TestHeatingDegreeParity(t *testing.T) {
	loc := berlin(t)
	b, err := os.ReadFile("testdata/heating_degree_parity.json")
	require.NoError(t, err)
	var fx struct {
		Samples []struct {
			Ts     int64   `json:"ts"`
			Energy float64 `json:"energy"`
			Temp   float64 `json:"temp"`
		} `json:"samples"`
		Targets      []time.Time `json:"targets"`
		TempForecast []float64   `json:"tempForecast"`
		Raw          []float64   `json:"raw"`
		Expected     []float64   `json:"expected"`
		Python       struct {
			DailyR  float64 `json:"dailyR"`
			Days    int     `json:"days"`
			Slope   float64 `json:"slope"`
			MeanHDD float64 `json:"meanHDD"`
		} `json:"python"`
	}
	require.NoError(t, json.Unmarshal(b, &fx))

	samples := make([]metrics.TemperatureSample, len(fx.Samples))
	for i, s := range fx.Samples {
		samples[i] = metrics.TemperatureSample{Ts: s.Ts, Energy: s.Energy, Temperature: s.Temp}
	}
	a := analyzeHeatingDegree(samples, loc)
	require.True(t, a.Open, a.Reason)
	require.NotNil(t, a.DailyR)
	assert.InDelta(t, fx.Python.DailyR, *a.DailyR, 1e-9)
	assert.Equal(t, fx.Python.Days, a.CompleteDays)
	assert.InDelta(t, fx.Python.Slope, a.Fit.slope, 1e-9)
	assert.InDelta(t, fx.Python.MeanHDD, a.Fit.meanHDD, 1e-9)

	start := fx.Targets[0]
	var rates api.Rates
	profile := make([]float64, len(fx.Raw))
	for i, ts := range fx.Targets {
		rates = append(rates, api.Rate{Start: ts, End: ts.Add(tariff.SlotDuration), Value: fx.TempForecast[i]})
		profile[i] = fx.Raw[i] / 1e3
	}
	got, days := correctHeatingDegreeOn(profile, start, rates, a.Fit, loc)
	var maxDiff float64
	for i := range got {
		maxDiff = max(maxDiff, math.Abs(got[i]*1e3-fx.Expected[i]))
	}
	assert.LessOrEqual(t, maxDiff, 1.0, "per slot within 1 Wh")
	var capped int
	for _, d := range days {
		if d.Capped {
			capped++
		}
		t.Logf("%s: %d slots, uncorrected %.2f kWh, correction %.2f of %.2f kWh, cap %.2f kWh, capped %v", d.Date, d.Slots, d.Uncorrected, d.Correction, d.Uncapped, d.Cap, d.Capped)
	}
	t.Logf("daily r %.4f on %d days, slope %.3f Wh/slot/K, meanHDD %.3f, max |Go - Python| %.2e Wh", *a.DailyR, a.CompleteDays, a.Fit.slope, a.Fit.meanHDD, maxDiff)
	assert.Positive(t, capped, "the fixture exercises the cap")
}
