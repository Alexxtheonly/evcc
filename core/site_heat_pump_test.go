package core

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/tariff"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hpSlotTemp is the test temperature (°C) of an instant: a day level plus a daily swing, so no
// day is constant.
func hpSlotTemp(level float64, t time.Time) float64 {
	return level + 2*math.Sin(float64(slotIndex(t))/96*2*math.Pi)
}

// hpHistory returns days consecutive local days from first: per slot the heat pump's energy
// (kWh) of a day of a + b x HDD spread evenly, and the slot temperatures. level(d) is the
// day's temperature level.
func hpHistory(loc *time.Location, first time.Time, days int, a, b float64, level func(d int) float64) (rows []metrics.HeatPumpRow, temps map[int64]float64) {
	temps = map[int64]float64{}
	for d := range days {
		day := time.Date(first.Year(), first.Month(), first.Day()+d, 0, 0, 0, 0, loc)
		next := day.AddDate(0, 0, 1)
		var slots []time.Time
		var hdd float64
		for t := day; t.Before(next); t = t.Add(tariff.SlotDuration) {
			slots = append(slots, t)
			hdd += heatingDegree(hpSlotTemp(level(d), t))
		}
		daily := a + b*hdd/float64(len(slots))
		for _, t := range slots {
			rows = append(rows, metrics.HeatPumpRow{Ts: t.Unix(), Energy: daily / float64(len(slots))})
			temps[t.Unix()] = hpSlotTemp(level(d), t)
		}
	}
	return rows, temps
}

func hpLevel(d int) float64 { return 4 + 5*math.Sin(float64(d)*1.3) }

func TestHeatPumpFitRecoversKnownLine(t *testing.T) {
	loc := berlin(t)
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
	rows, temps := hpHistory(loc, first, 40, 2.5, 1.3, hpLevel)
	today := first.AddDate(0, 0, 40)

	days := heatPumpDays(rows, nil, temps, today, loc)
	require.Len(t, days, 40)
	var slotSum float64
	for _, r := range rows[:96] {
		slotSum += r.Energy
	}
	assert.InDelta(t, slotSum, days[0].Energy, 1e-9, "kWh, summed as recorded")
	assert.Less(t, days[0].Energy, 30.0, "a heat pump day is a few tens of kWh, not Wh")

	fit, reason := fitHeatPump(days, today)
	assert.Empty(t, reason)
	assert.InDelta(t, 2.5, fit.A, 1e-9)
	assert.InDelta(t, 1.3, fit.B, 1e-9, "kWh per K and day")
	assert.InDelta(t, 1, fit.R, 1e-9)
	assert.Equal(t, 40, fit.Days)
	assert.Equal(t, 40, fit.Valid)
	assert.Greater(t, fit.HDDRange, heatPumpMinHDDRange)

	// the same series read as Wh (the TemperatureSample unit) leaves the physical bounds
	wh := make([]heatPumpDay, len(days))
	for i, d := range days {
		wh[i] = heatPumpDay{Date: d.Date, Energy: d.Energy * 1e3, HDD: d.HDD}
	}
	_, reason = fitHeatPump(wh, today)
	assert.Equal(t, heatPumpSlopeOutOfBounds, reason)
}

func TestHeatPumpFitGates(t *testing.T) {
	loc := berlin(t)
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
	today := first.AddDate(0, 0, 200)
	series := func(n int, hdd, energy func(d int) float64) []heatPumpDay {
		var res []heatPumpDay
		for d := range n {
			res = append(res, heatPumpDay{Date: first.AddDate(0, 0, d), HDD: hdd(d), Energy: energy(d)})
		}
		return res
	}
	wide := func(d int) float64 { return float64(d % 10) } // 0..9 K
	line := func(a, b float64) func(d int) float64 {
		return func(d int) float64 { return a + b*wide(d) }
	}

	for _, c := range []struct {
		name   string
		days   []heatPumpDay
		reason string
	}{
		{"enough", series(14, wide, line(2, 1)), ""},
		{"no days", nil, heatPumpFewDays},
		{"13 days", series(13, wide, line(2, 1)), heatPumpFewDays},
		{"spread 2 K", series(30, func(d int) float64 { return float64(d % 3) }, line(2, 1)), heatPumpLowHDDRange},
		{"energy unrelated to temperature", series(30, wide, func(d int) float64 { return 10 + float64(d*7%5) }), heatPumpFitPoor},
		{"constant energy", series(30, wide, func(int) float64 { return 10 }), heatPumpFitPoor},
		{"slope below 0.3", series(30, wide, line(5, 0.2)), heatPumpSlopeOutOfBounds},
		{"slope above 3", series(30, wide, line(0, 3.5)), heatPumpSlopeOutOfBounds},
		{"slope at the bounds", series(30, wide, line(0, 3)), ""},
		{"negative slope", series(30, wide, line(30, -1)), heatPumpFitPoor},
	} {
		t.Run(c.name, func(t *testing.T) {
			fit, reason := fitHeatPump(c.days, today)
			assert.Equal(t, c.reason, reason, "%+v", fit)
		})
	}

	t.Run("the last 90 days once 60 of them qualify", func(t *testing.T) {
		// old days follow another line (shifted by 20 kWh); the recent 70 follow a=2, b=1
		var old, recent []heatPumpDay
		for d := range 100 {
			old = append(old, heatPumpDay{Date: today.AddDate(0, 0, -300+d), HDD: wide(d), Energy: 22 + wide(d)})
		}
		for d := range 70 {
			recent = append(recent, heatPumpDay{Date: today.AddDate(0, 0, -70+d), HDD: wide(d), Energy: 2 + wide(d)})
		}
		fit, reason := fitHeatPump(append(slices.Clone(old), recent...), today)
		assert.Empty(t, reason)
		assert.Equal(t, 70, fit.Days)
		assert.Equal(t, 170, fit.Valid)
		assert.InDelta(t, 1, fit.B, 1e-9)

		// 59 recent days: everything counts (expanding window)
		fit, _ = fitHeatPump(append(slices.Clone(old), recent[:59]...), today)
		assert.Equal(t, 159, fit.Days)
	})
}

func TestHeatPumpDays(t *testing.T) {
	loc := berlin(t)
	first := time.Date(2026, 1, 5, 0, 0, 0, 0, loc)
	today := first.AddDate(0, 0, 5)

	t.Run("flagged slots are unknown, not zero", func(t *testing.T) {
		rows, temps := hpHistory(loc, first, 1, 2, 1, hpLevel)
		want := 0.0
		for _, r := range rows {
			want += r.Energy
		}
		for i := 10; i < 15; i++ { // 5 of 96 slots read zero and are flagged
			rows[i].Energy, rows[i].Incomplete = 0, true
		}
		days := heatPumpDays(rows, nil, temps, today, loc)
		require.Len(t, days, 1)
		assert.InDelta(t, want, days[0].Energy, 1e-9, "scaled over the 91 present slots: the day is not 5 slots short")

		for i := 10; i < 30; i++ { // 20 flagged: below 90 % present
			rows[i].Energy, rows[i].Incomplete = 0, true
		}
		assert.Empty(t, heatPumpDays(rows, nil, temps, today, loc), "a day mostly flagged is not valid")
	})

	t.Run("recovered slots are unknown too", func(t *testing.T) {
		rows, temps := hpHistory(loc, first, 1, 2, 1, hpLevel)
		for i := range 20 {
			rows[i].Recovered = true
		}
		assert.Empty(t, heatPumpDays(rows, nil, temps, today, loc))
	})

	t.Run("incomplete day without temperature or with a constant one", func(t *testing.T) {
		rows, temps := hpHistory(loc, first, 3, 2, 1, hpLevel)
		for _, r := range rows[96 : 96+50] { // second day: only 52 of 96 slots have a temperature
			delete(temps, r.Ts)
		}
		for _, r := range rows[192:] { // third day: the sensor is stuck
			temps[r.Ts] = 1.1
		}
		days := heatPumpDays(rows, nil, temps, today, loc)
		require.Len(t, days, 1)
		assert.True(t, days[0].Date.Equal(first))
	})

	t.Run("today and later are not part of the fit", func(t *testing.T) {
		rows, temps := hpHistory(loc, first, 6, 2, 1, hpLevel)
		days := heatPumpDays(rows, nil, temps, today, loc)
		assert.Len(t, days, 5)
		assert.True(t, days[len(days)-1].Date.Before(today))
	})

	t.Run("the backfill fills days the meter lacks, the meter wins where both exist", func(t *testing.T) {
		rows, temps := hpHistory(loc, first, 3, 2, 1, hpLevel)
		var backfill []metrics.SlotValue
		for _, r := range rows {
			backfill = append(backfill, metrics.SlotValue{Ts: r.Ts, Value: r.Energy * 2})
		}
		measured := rows[96:] // the first day only in the backfill
		days := heatPumpDays(measured, backfill, temps, today, loc)
		require.Len(t, days, 3)
		var day0, day1 float64
		for _, r := range rows[:96] {
			day0 += r.Energy * 2
		}
		for _, r := range rows[96:192] {
			day1 += r.Energy
		}
		assert.InDelta(t, day0, days[0].Energy, 1e-9, "backfill")
		assert.InDelta(t, day1, days[1].Energy, 1e-9, "meter, not the doubled backfill")

		// a day the meter has only partly is taken whole from the backfill, not mixed
		partial := append([]metrics.HeatPumpRow{}, rows[:50]...)
		days = heatPumpDays(partial, backfill, temps, today, loc)
		assert.InDelta(t, day0, days[0].Energy, 1e-9)
	})

	t.Run("daylight saving days have 92 and 100 slots", func(t *testing.T) {
		for _, c := range []struct {
			day    time.Time
			length int
		}{
			{time.Date(2026, 3, 29, 0, 0, 0, 0, loc), 92},
			{time.Date(2026, 10, 25, 0, 0, 0, 0, loc), 100},
		} {
			rows, temps := hpHistory(loc, c.day, 1, 2, 1, hpLevel)
			require.Len(t, rows, c.length)
			days := heatPumpDays(rows, nil, temps, c.day.AddDate(0, 0, 2), loc)
			require.Len(t, days, 1, "complete with %d slots", c.length)
			var sum float64
			for _, r := range rows {
				sum += r.Energy
			}
			assert.InDelta(t, sum, days[0].Energy, 1e-9)

			// 90 % of the date's own length decides, not of 96
			short := rows[:int(math.Ceil(heatingDegreeDayFill*float64(c.length)))-1]
			assert.Empty(t, heatPumpDays(short, nil, temps, c.day.AddDate(0, 0, 2), loc))
		}
	})
}

func TestHeatPumpShape(t *testing.T) {
	loc := berlin(t)
	first := time.Date(2026, 1, 5, 0, 0, 0, 0, loc)
	// a heavy morning: 3x energy from 06:00 to 08:00
	weight := func(i int) float64 {
		if i >= 24 && i < 32 {
			return 3
		}
		return 1
	}
	build := func(days int) []metrics.HeatPumpRow {
		var rows []metrics.HeatPumpRow
		for d := range days {
			day := first.AddDate(0, 0, d)
			for t, i := day, 0; t.Before(day.AddDate(0, 0, 1)); t, i = t.Add(tariff.SlotDuration), i+1 {
				rows = append(rows, metrics.HeatPumpRow{Ts: t.Unix(), Energy: 0.1 * weight(slotIndex(t)) * float64(1+d%3)})
			}
		}
		return rows
	}
	today := first.AddDate(0, 0, 10)

	t.Run("learned, normalised, weighted per time of day", func(t *testing.T) {
		shape, days, learned := heatPumpShape(build(10), today, loc)
		assert.True(t, learned)
		assert.Equal(t, 10, days)
		var sum float64
		for _, s := range shape {
			sum += s
		}
		assert.InDelta(t, 1, sum, 1e-12)
		assert.InDelta(t, 3*shape[0], shape[26], 1e-12)
		assert.InDelta(t, 3.0/(88+8*3), shape[26], 1e-12)
	})

	t.Run("fewer than 7 complete real days is uniform", func(t *testing.T) {
		shape, days, learned := heatPumpShape(build(6), first.AddDate(0, 0, 6), loc)
		assert.False(t, learned)
		assert.Equal(t, 6, days)
		for _, s := range shape {
			assert.InDelta(t, 1.0/96, s, 1e-15)
		}
	})

	t.Run("flagged and incomplete days do not count", func(t *testing.T) {
		rows := build(10)
		for i := range rows[:96*4] { // four days unusable
			rows[i].Incomplete = true
		}
		_, days, learned := heatPumpShape(rows, today, loc)
		assert.Equal(t, 6, days)
		assert.False(t, learned)
	})

	t.Run("only the last 30 days before today", func(t *testing.T) {
		_, days, _ := heatPumpShape(build(10), first.AddDate(0, 0, 40), loc)
		assert.Zero(t, days)
		_, days, _ = heatPumpShape(build(10), first.AddDate(0, 0, 9), loc)
		assert.Equal(t, 9, days, "today is excluded")
	})

	t.Run("daylight saving days still give a full shape", func(t *testing.T) {
		start := time.Date(2026, 3, 25, 0, 0, 0, 0, loc) // contains the 29th, 92 slots
		var rows []metrics.HeatPumpRow
		for d := range 9 {
			day := start.AddDate(0, 0, d)
			for t := day; t.Before(day.AddDate(0, 0, 1)); t = t.Add(tariff.SlotDuration) {
				rows = append(rows, metrics.HeatPumpRow{Ts: t.Unix(), Energy: 0.1})
			}
		}
		shape, days, learned := heatPumpShape(rows, start.AddDate(0, 0, 9), loc)
		require.True(t, learned)
		assert.Equal(t, 9, days)
		for _, s := range shape {
			assert.InDelta(t, 1.0/96, s, 1e-12)
		}
	})

	t.Run("a time of day nobody observed is uniform", func(t *testing.T) {
		rows := build(10)
		var kept []metrics.HeatPumpRow
		for _, r := range rows {
			if slotIndex(time.Unix(r.Ts, 0).In(loc)) != 40 {
				kept = append(kept, r)
			}
		}
		// every day is still above 90 % but one index has no sample
		_, _, learned := heatPumpShape(kept, today, loc)
		assert.False(t, learned)
	})
}

func TestHeatPumpAnalysisBackfillDoesNotShapeButFits(t *testing.T) {
	loc := berlin(t)
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
	rows, temps := hpHistory(loc, first, 30, 2, 1.2, hpLevel)
	var backfill []metrics.SlotValue
	for _, r := range rows {
		backfill = append(backfill, metrics.SlotValue{Ts: r.Ts, Value: r.Energy})
	}
	a := analyzeHeatPump(nil, backfill, temps, first.AddDate(0, 0, 30), loc)
	assert.True(t, a.Open, a.Reason)
	assert.InDelta(t, 1.2, a.Fit.B, 1e-9)
	assert.Zero(t, a.ShapeDays)
	assert.False(t, a.ShapeLearned, "hourly backfill never makes a shape")
}

func TestHeatPumpForecastStale(t *testing.T) {
	now := time.Date(2026, 2, 10, 12, 7, 0, 0, time.UTC)
	rates := func(from time.Time, n int, value func(i int) float64) api.Rates {
		var res api.Rates
		for i := range n {
			s := from.Add(time.Duration(i) * tariff.SlotDuration)
			res = append(res, api.Rate{Start: s, End: s.Add(tariff.SlotDuration), Value: value(i)})
		}
		return res
	}
	varying := func(i int) float64 { return float64(i % 7) }

	for _, c := range []struct {
		name  string
		rates api.Rates
		stale bool
	}{
		{"none", nil, true},
		{"ended in the past", rates(now.Add(-48*time.Hour), 96, varying), true},
		{"ends exactly now", rates(now.Add(-24*time.Hour).Truncate(tariff.SlotDuration), 96, varying), true},
		{"constant for the next 24 h", rates(now.Add(-time.Hour), 4*30, func(int) float64 { return 1.1 }), true},
		{"constant, but only 3 slots ahead", rates(now.Truncate(tariff.SlotDuration), 3, func(int) float64 { return 1.1 }), false},
		{"varying", rates(now.Add(-time.Hour), 4*30, varying), false},
		{"changes only after 24 h", rates(now.Truncate(tariff.SlotDuration), 4*30, func(i int) float64 {
			if i < 100 {
				return 1.1
			}
			return 3
		}), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.stale, heatPumpForecastStale(c.rates, now))
		})
	}
}

// hpCompose composes n slots from start with a flat non-heat-pump profile of nonHP Wh.
func hpCompose(a *heatPumpAnalysis, start time.Time, n int, nonHP float64, rates api.Rates, loc *time.Location) ([]float64, []metrics.HomeForecastSlot, []heatPumpPlanDay) {
	profile := make([]metrics.HomeForecastSlot, n)
	active := make([]float64, n)
	for i := range profile {
		profile[i] = metrics.HomeForecastSlot{Start: start.Add(time.Duration(i) * tariff.SlotDuration), Low: 50, Base: 400, High: 900}
		active[i] = 0.4
	}
	flat := make([]metrics.HomeForecastSlot, n)
	for i := range flat {
		flat[i] = metrics.HomeForecastSlot{Start: profile[i].Start, Low: nonHP / 2, Base: nonHP, High: nonHP * 2}
	}
	return composeHeatPump(a, start, flat, profile, active, rates, loc)
}

func TestComposeHeatPump(t *testing.T) {
	loc := berlin(t)
	var shape [96]float64
	for i := range shape {
		shape[i] = 1.0 / 96
	}
	shape[10], shape[11] = 3.0/96, 3.0/96 // renormalised below
	var total float64
	for _, s := range shape {
		total += s
	}
	for i := range shape {
		shape[i] /= total
	}
	a := &heatPumpAnalysis{Shape: shape, Fit: heatPumpFit{A: 2, B: 1.3}}
	constant := func(from time.Time, n int, temp float64) api.Rates {
		var res api.Rates
		for i := range n {
			s := from.Add(time.Duration(i) * tariff.SlotDuration)
			res = append(res, api.Rate{Start: s, End: s.Add(tariff.SlotDuration), Value: temp})
		}
		return res
	}

	t.Run("daily energy is a + b x HDD, shared by the shape, plus the profile", func(t *testing.T) {
		day := time.Date(2026, 2, 10, 0, 0, 0, 0, loc)
		series, band, days := hpCompose(a, day, 96, 100, constant(day, 96, 5), loc)
		require.Len(t, days, 1)
		assert.True(t, days[0].Modelled)
		assert.InDelta(t, 10, days[0].HDD, 1e-12)
		assert.InDelta(t, 2+1.3*10, days[0].HeatPump, 1e-9)
		assert.InDelta(t, 96*0.1, days[0].NonHP, 1e-9)
		assert.InDelta(t, days[0].HeatPump+days[0].NonHP, days[0].Total, 1e-9)

		var sum float64
		for i, v := range series {
			sum += v
			assert.InDelta(t, 0.1+(2+13)*shape[i], v, 1e-12, "kWh per slot")
			assert.InDelta(t, v*1e3, band[i].Base, 1e-9)
		}
		assert.InDelta(t, days[0].Total, sum, 1e-9)
		assert.Greater(t, series[10], 2*series[0], "the shape moves energy to its heavy slots")
	})

	t.Run("daylight saving dates still sum to the daily energy", func(t *testing.T) {
		for _, c := range []struct {
			day    time.Time
			length int
		}{
			{time.Date(2026, 3, 29, 0, 0, 0, 0, loc), 92},
			{time.Date(2026, 10, 25, 0, 0, 0, 0, loc), 100},
		} {
			series, _, days := hpCompose(a, c.day, c.length, 0, constant(c.day, c.length, 5), loc)
			require.Len(t, days, 1, "%d slots are one date", c.length)
			var sum float64
			for _, v := range series {
				sum += v
			}
			assert.InDelta(t, 2+1.3*10, sum, 1e-9, "%d slots", c.length)
		}
	})

	t.Run("a milder day gets less, never below zero", func(t *testing.T) {
		day := time.Date(2026, 6, 10, 0, 0, 0, 0, loc)
		warm := &heatPumpAnalysis{Shape: shape, Fit: heatPumpFit{A: -4, B: 1}}
		series, band, days := hpCompose(warm, day, 96, 100, constant(day, 96, 25), loc)
		assert.Zero(t, days[0].HeatPump, "a + b x 0 is negative: clamped")
		for i := range series {
			assert.InDelta(t, 0.1, series[i], 1e-12)
			assert.Equal(t, 50.0, band[i].Low)
		}
	})

	t.Run("the band widens the heat-pump part by b x 1.5 K each side and stays valid", func(t *testing.T) {
		day := time.Date(2026, 2, 10, 0, 0, 0, 0, loc)
		series, band, _ := hpCompose(a, day, 96, 100, constant(day, 96, 5), loc)
		for i := range band {
			hp := (series[i] - 0.1) * 1e3
			half := 1.3 * 1.5 * shape[i] * 1e3
			assert.InDelta(t, 50+max(0, hp-half), band[i].Low, 1e-9)
			assert.InDelta(t, 200+hp+half, band[i].High, 1e-9)
			assert.LessOrEqual(t, band[i].Low, band[i].Base)
			assert.LessOrEqual(t, band[i].Base, band[i].High)
			assert.GreaterOrEqual(t, band[i].Low, 0.0)
		}
		// a high shape share must not push Low below the profile's low
		tiny := &heatPumpAnalysis{Shape: shape, Fit: heatPumpFit{A: 0, B: 3}}
		_, band, _ = hpCompose(tiny, day, 96, 100, constant(day, 96, 14.5), loc)
		for _, b := range band {
			assert.GreaterOrEqual(t, b.Low, 50.0)
			assert.LessOrEqual(t, b.Low, b.Base)
		}
	})

	t.Run("a date with under 90 % of its slots forecast keeps the active forecast and band", func(t *testing.T) {
		start := time.Date(2026, 2, 10, 0, 0, 0, 0, loc)
		// day 1 fully covered, day 2 covered for 80 % of its slots
		rates := constant(start, 96+77, 5)
		series, band, days := hpCompose(a, start, 192, 100, rates, loc)
		require.Len(t, days, 2)
		assert.True(t, days[0].Modelled)
		assert.False(t, days[1].Modelled)
		assert.Zero(t, days[1].HeatPump)
		for i := 96; i < 192; i++ {
			assert.Equal(t, 0.4, series[i], "active value")
			assert.Equal(t, 400.0, band[i].Base, "profile band")
		}
		assert.NotEqual(t, 0.4, series[0])
		assert.InDelta(t, 96*0.4, days[1].Total, 1e-9)
	})

	t.Run("a partial first day uses the whole date's temperature and covers only the slots asked for", func(t *testing.T) {
		midnight := time.Date(2026, 2, 10, 0, 0, 0, 0, loc)
		start := midnight.Add(20 * time.Hour)
		series, _, days := hpCompose(a, start, 16, 0, constant(midnight, 96, 5), loc)
		require.Len(t, days, 1)
		assert.True(t, days[0].Modelled)
		assert.InDelta(t, 10, days[0].HDD, 1e-12)
		var sum, want float64
		for i, v := range series {
			sum += v
			want += (2 + 13) * shape[slotIndex(start.Add(time.Duration(i)*tariff.SlotDuration))]
		}
		assert.InDelta(t, want, sum, 1e-9)
		assert.Less(t, sum, 2+13.0)

		// rates that only begin at the start still cover the asked-for slots
		_, _, days = hpCompose(a, start, 16, 0, constant(start, 32, 5), loc)
		assert.True(t, days[0].Modelled)
	})
}
