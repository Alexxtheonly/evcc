package core

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/server/db/settings"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const (
	hpTruthA = 2.0 // kWh per day
	hpTruthB = 1.3 // kWh per K and day
)

// hpAnchor makes the day level a function of the date, so history and forecast agree.
var hpAnchor = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func hpTempAt(t time.Time) float64 {
	local := t.In(time.Local)
	d := int(time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, time.UTC).Sub(hpAnchor).Hours() / 24)
	return hpSlotTemp(hpLevel(d), local)
}

// hpWeight is the heat pump's true time-of-day weight.
func hpWeight(idx int) float64 {
	if idx >= 24 && idx < 36 {
		return 4
	}
	return 1
}

// hpNonHP is the true non-heat-pump load (kWh per slot).
func hpNonHP(idx int) float64 { return 0.05 + 0.01*float64(idx%8) }

// hpTruthHP is the heat pump's true energy (kWh) in the slot starting at t: the day's
// a + b x HDD (from the true temperature) shared by the weights of the date's actual slots.
func hpTruthHP(t time.Time) float64 {
	local := t.In(time.Local)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	var hdd, weights float64
	var n int
	for s := day; s.Before(day.AddDate(0, 0, 1)); s = s.Add(tariff.SlotDuration) {
		hdd += heatingDegree(hpTempAt(s))
		weights += hpWeight(slotIndex(s))
		n++
	}
	return (hpTruthA + hpTruthB*hdd/float64(n)) * hpWeight(slotIndex(local)) / weights
}

type hpSeed struct {
	ts         time.Time
	home, hp   float64
	incomplete bool
	skipHP     bool
}

// hpFixture is a site on an in-memory database holding days of true history up to now: the
// household (non-heat-pump load plus heat pump), the heat pump ext meter "db:19" and the
// temperature meter.
type hpFixture struct {
	site *Site
	now  time.Time
	hp   int
}

func seedHeatPump(t *testing.T, now time.Time, days int, rates func(now time.Time) api.Rates, adjust func(s *hpSeed)) *hpFixture {
	t.Helper()
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())
	saveHeatPumpSettings(t, heatPumpShadow, "db:19")
	require.NoError(t, db.Instance.Exec(`INSERT INTO entities ("group", name, title) VALUES ('meter', 'db:19', 'Heat pump')`).Error)
	require.NoError(t, db.Instance.Exec(`INSERT INTO entities ("group", name, title, is_temp) VALUES ('temperature', 'temperature', 'Temperature', 1)`).Error)
	var hp, temp, home int
	require.NoError(t, db.Instance.Raw(`SELECT id FROM entities WHERE name = 'db:19'`).Scan(&hp).Error)
	require.NoError(t, db.Instance.Raw(`SELECT id FROM entities WHERE name = 'temperature'`).Scan(&temp).Error)
	require.NoError(t, db.Instance.Raw(`SELECT id FROM entities WHERE "group" = 'home'`).Scan(&home).Error)

	sqlDB, err := db.Instance.DB()
	require.NoError(t, err)
	tx, err := sqlDB.Begin()
	require.NoError(t, err)
	stmt, err := tx.Prepare(`INSERT INTO meters (meter, ts, energy, return_energy, soc_temp, recovered, incomplete) VALUES (?, ?, ?, 0, ?, 0, ?)`)
	require.NoError(t, err)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	for ts := midnight.AddDate(0, 0, -days); !ts.Add(tariff.SlotDuration).After(now); ts = ts.Add(tariff.SlotDuration) {
		s := hpSeed{ts: ts, hp: hpTruthHP(ts)}
		s.home = hpNonHP(slotIndex(ts)) + s.hp
		if adjust != nil {
			adjust(&s)
		}
		_, err := stmt.Exec(home, ts.Unix(), s.home, nil, false)
		require.NoError(t, err)
		if !s.skipHP {
			_, err = stmt.Exec(hp, ts.Unix(), s.hp, nil, s.incomplete)
			require.NoError(t, err)
		}
		_, err = stmt.Exec(temp, ts.Unix(), 0, hpTempAt(ts), false)
		require.NoError(t, err)
	}
	require.NoError(t, stmt.Close())
	require.NoError(t, tx.Commit())

	site := &Site{log: util.NewLogger("foo"), tariffs: &tariff.Tariffs{}}
	if rates != nil {
		ctrl := gomock.NewController(t)
		tempTariff := api.NewMockTariff(ctrl)
		tempTariff.EXPECT().Type().Return(api.TariffTypeTemperature).AnyTimes()
		tempTariff.EXPECT().Rates().Return(rates(now), nil).AnyTimes()
		site.tariffs = &tariff.Tariffs{Temperature: tempTariff}
	}
	site.heatingDegreeCached = func() (heatingDegreeResult, error) { return heatingDegreeResult{}, nil }
	site.heatingDegreeDaily = func() (*heatingDegreeAnalysis, error) {
		return &heatingDegreeAnalysis{Reason: heatingDegreeDailyRLow}, nil
	}
	return &hpFixture{site: site, now: now, hp: hp}
}

// trueRates is the true temperature from the start of today for days.
func trueRates(days int, from func(now time.Time) time.Time) func(now time.Time) api.Rates {
	return func(now time.Time) api.Rates {
		var res api.Rates
		start := from(now)
		for i := range days * 96 {
			s := start.Add(time.Duration(i) * tariff.SlotDuration)
			res = append(res, api.Rate{Start: s, End: s.Add(tariff.SlotDuration), Value: hpTempAt(s)})
		}
		return res
	}
}

func startOfToday(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
}

// profileFor returns the household profile for n slots from the slot containing now.
func (f *hpFixture) profileFor(t *testing.T, n int) (*metrics.HomeForecastResult, []float64, time.Time) {
	t.Helper()
	start := f.now.Truncate(tariff.SlotDuration)
	profile, err := metrics.HomeForecast(start, start.Add(time.Duration(n)*tariff.SlotDuration))
	require.NoError(t, err)
	active := make([]float64, n)
	for i, s := range profile.Rates {
		active[i] = s.Base / 1e3
	}
	return profile, active, start
}

// saveHeatPumpSettings stores the heat-pump settings the status reads.
func saveHeatPumpSettings(t *testing.T, mode, meter string) {
	t.Helper()
	t.Cleanup(func() {
		_ = settings.SetJson(energyIntelligenceKey, EnergyIntelligenceSettings{SettlementMode: "simulation"})
	})
	require.NoError(t, settings.SetJson(energyIntelligenceKey, EnergyIntelligenceSettings{SettlementMode: "simulation", HeatPumpModel: mode, HeatPumpMeter: meter}))
}

func withBerlin(t *testing.T) {
	t.Helper()
	l := time.Local
	time.Local = berlin(t)
	t.Cleanup(func() { time.Local = l })
}

var hpNow = func() time.Time { return time.Date(2026, 2, 10, 12, 7, 0, 0, berlinFixed) }

var berlinFixed = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		panic(err)
	}
	return loc
}()

func TestHeatPumpSplitForecastsTheTruth(t *testing.T) {
	withBerlin(t)
	now := hpNow()
	f := seedHeatPump(t, now, 45, trueRates(6, startOfToday), nil)
	n := 3 * 96
	profile, active, start := f.profileFor(t, n)

	series, band := f.site.heatPumpSplit(heatPumpShadow, "db:19", now, profile, active)
	require.NotNil(t, series)
	require.Len(t, series, n)
	require.Len(t, band, n)

	for i := range series {
		ts := start.Add(time.Duration(i) * tariff.SlotDuration)
		want := hpNonHP(slotIndex(ts)) + hpTruthHP(ts)
		require.InDelta(t, want, series[i], 1e-9, "slot %s, kWh", ts)
		assert.InDelta(t, series[i]*1e3, band[i].Base, 1e-9, "band in Wh")
		assert.GreaterOrEqual(t, band[i].Low, 0.0)
		assert.LessOrEqual(t, band[i].Low, band[i].Base)
		assert.LessOrEqual(t, band[i].Base, band[i].High)
	}
	assert.Greater(t, active[0], 0.0)
	assert.NotEqual(t, active[30], series[30], "the profile includes the heat pump at its 30 day average, the split follows the forecast temperature")

	st := f.site.heatPump(now)
	assert.Equal(t, heatPumpShadow, st.Mode)
	assert.True(t, st.Model.Open, st.Model.Reason)
	require.NotNil(t, st.Model.Fit)
	assert.InDelta(t, hpTruthA, st.Model.Fit.A, 1e-9)
	assert.InDelta(t, hpTruthB, st.Model.Fit.B, 1e-9)
	assert.InDelta(t, 1, st.Model.Fit.R, 1e-9)
	assert.Equal(t, 45, st.Model.Fit.Days)
	assert.Equal(t, 30, st.Model.ShapeDays)
	assert.True(t, st.Model.ShapeLearned)
	assert.Zero(t, st.Model.DroppedSlots)
	assert.Positive(t, st.Model.PairedSlots)
	require.Len(t, st.Days, 4)
	for _, d := range st.Days {
		assert.True(t, d.Modelled)
		assert.InDelta(t, d.HeatPump+d.NonHP, d.Total, 1e-9)
	}
	assert.InDelta(t, hpTruthA+hpTruthB*st.Days[1].HDD, st.Days[1].HeatPump, 1e-9)
}

func TestHeatPumpSplitGates(t *testing.T) {
	withBerlin(t)
	now := hpNow()
	rates := trueRates(6, startOfToday)

	run := func(t *testing.T, f *hpFixture, ref string) (series []float64, reason string) {
		t.Helper()
		profile, active, _ := f.profileFor(t, 2*96)
		series, _ = f.site.heatPumpSplit(heatPumpShadow, ref, now, profile, active)
		f.site.heatPumpLast.mu.Lock()
		defer f.site.heatPumpLast.mu.Unlock()
		require.False(t, f.site.heatPumpLast.updated.IsZero())
		assert.Equal(t, series != nil, f.site.heatPumpLast.open)
		return series, f.site.heatPumpLast.reason
	}

	t.Run("no meter configured or found", func(t *testing.T) {
		f := seedHeatPump(t, now, 45, rates, nil)
		_, reason := run(t, f, "")
		assert.Equal(t, heatPumpNoMeter, reason)
		_, reason = run(t, f, "db:99")
		assert.Equal(t, heatPumpNoMeter, reason)
	})

	t.Run("too few valid days", func(t *testing.T) {
		f := seedHeatPump(t, now, 10, rates, func(s *hpSeed) { s.skipHP = s.ts.Before(now.AddDate(0, 0, -9)) })
		_, reason := run(t, f, "db:19")
		assert.Equal(t, heatPumpFewDays, reason)
	})

	t.Run("a poor fit", func(t *testing.T) {
		f := seedHeatPump(t, now, 45, rates, func(s *hpSeed) {
			// the heat pump follows the weekday, not the temperature
			s.hp = 0.05 + 0.02*float64(s.ts.Weekday())
			s.home = hpNonHP(slotIndex(s.ts)) + s.hp
		})
		_, reason := run(t, f, "db:19")
		assert.Equal(t, heatPumpFitPoor, reason)
	})

	t.Run("temperature forecast missing, ended or flat", func(t *testing.T) {
		for name, r := range map[string]func(now time.Time) api.Rates{
			"missing": nil,
			"ended":   trueRates(1, func(now time.Time) time.Time { return startOfToday(now).AddDate(0, 0, -3) }),
			"flat": func(now time.Time) api.Rates {
				var res api.Rates
				for i := range 6 * 96 {
					s := startOfToday(now).Add(time.Duration(i) * tariff.SlotDuration)
					res = append(res, api.Rate{Start: s, End: s.Add(tariff.SlotDuration), Value: 1.1})
				}
				return res
			},
		} {
			f := seedHeatPump(t, now, 45, r, nil)
			series, reason := run(t, f, "db:19")
			assert.Nil(t, series, name)
			assert.Equal(t, heatPumpTemperatureStale, reason, name)
		}
	})

	t.Run("a flagged zero reading is not a heat pump that is off", func(t *testing.T) {
		f := seedHeatPump(t, now, 45, rates, func(s *hpSeed) {
			if s.ts.After(now.Add(-100 * time.Minute)) {
				s.hp, s.incomplete = 0, true // the meter was unreachable: reads zero, flagged
			}
		})
		series, reason := run(t, f, "db:19")
		assert.Nil(t, series, "it must not forecast with a heat pump at zero")
		assert.Equal(t, heatPumpMeterIncomplete, reason)

		// the flag is what matters: the same zeros unflagged are real readings
		f = seedHeatPump(t, now, 45, rates, func(s *hpSeed) {
			if s.ts.After(now.Add(-100 * time.Minute)) {
				s.hp = 0
				s.home = hpNonHP(slotIndex(s.ts))
			}
		})
		series, reason = run(t, f, "db:19")
		assert.NotNil(t, series, reason)
	})

	t.Run("an incomplete slot within 24 h closes it, an older one does not", func(t *testing.T) {
		f := seedHeatPump(t, now, 45, rates, func(s *hpSeed) { s.incomplete = s.ts.Equal(now.Add(-20 * time.Hour).Truncate(tariff.SlotDuration)) })
		_, reason := run(t, f, "db:19")
		assert.Equal(t, heatPumpMeterIncomplete, reason)

		f = seedHeatPump(t, now, 45, rates, func(s *hpSeed) { s.incomplete = s.ts.Equal(now.Add(-30 * time.Hour).Truncate(tariff.SlotDuration)) })
		series, reason := run(t, f, "db:19")
		assert.NotNil(t, series, reason)
	})

	t.Run("the newest reading is older than an hour", func(t *testing.T) {
		f := seedHeatPump(t, now, 45, rates, func(s *hpSeed) { s.skipHP = s.ts.After(now.Add(-3 * time.Hour)) })
		_, reason := run(t, f, "db:19")
		assert.Equal(t, heatPumpMeterIncomplete, reason)
		f = seedHeatPump(t, now, 45, rates, func(s *hpSeed) { s.skipHP = s.ts.After(now.Add(-45 * time.Minute)) })
		series, reason := run(t, f, "db:19")
		assert.NotNil(t, series, reason)
	})

	t.Run("negative residuals: dropped and counted, too many close it", func(t *testing.T) {
		skew := func(share int) func(s *hpSeed) {
			return func(s *hpSeed) {
				if s.ts.After(now.AddDate(0, 0, -20)) && s.ts.Before(now.Add(-24*time.Hour)) && int(s.ts.Unix()/900)%100 < share {
					s.home = s.hp - 0.01 // the household meter reads less than the heat pump
				}
			}
		}
		f := seedHeatPump(t, now, 45, rates, skew(5))
		series, reason := run(t, f, "db:19")
		require.NotNil(t, series, reason)
		assert.Positive(t, f.site.heatPump(now).Model.DroppedSlots)

		f = seedHeatPump(t, now, 45, rates, skew(50))
		series, reason = run(t, f, "db:19")
		assert.Nil(t, series)
		assert.Equal(t, heatPumpNegativeResidual, reason)
		assert.Positive(t, f.site.heatPump(now).Model.DroppedSlots)
	})

	t.Run("backfilled history, but the real heat pump slots do not make a profile yet", func(t *testing.T) {
		// the meter was added a day ago: its earlier days exist only as the hourly backfill
		realFrom := now.Add(-26 * time.Hour)
		f := seedHeatPump(t, now, 45, rates, func(s *hpSeed) { s.skipHP = s.ts.Before(realFrom) })
		backfill := func() {
			for ts := startOfToday(now).AddDate(0, 0, -45); ts.Before(realFrom); ts = ts.Add(time.Hour) {
				for k := range 4 {
					slot := ts.Add(time.Duration(k) * tariff.SlotDuration)
					require.NoError(t, db.Instance.Exec(`INSERT INTO meters_backfill (meter, ts, energy, source) VALUES (?, ?, ?, 'ha_hourly')`,
						f.hp, slot.Unix(), hpTruthHP(slot)).Error)
				}
			}
		}
		backfill()
		series, reason := run(t, f, "db:19")
		assert.Nil(t, series)
		assert.Equal(t, heatPumpProfile, reason, "a fit from the backfill alone is not enough to subtract")
		st := f.site.heatPump(now)
		require.NotNil(t, st.Model.Fit)
		assert.Greater(t, st.Model.Fit.Days, 30, "the fit does use the backfill")
		assert.False(t, st.Model.ShapeLearned, "and the shape never does")

		// three real days later the profile exists, the shape is still uniform
		f = seedHeatPump(t, now, 45, rates, func(s *hpSeed) { s.skipHP = s.ts.Before(startOfToday(now).AddDate(0, 0, -3)) })
		for ts := startOfToday(now).AddDate(0, 0, -45); ts.Before(startOfToday(now).AddDate(0, 0, -3)); ts = ts.Add(tariff.SlotDuration) {
			require.NoError(t, db.Instance.Exec(`INSERT INTO meters_backfill (meter, ts, energy, source) VALUES (?, ?, ?, 'ha_hourly')`, f.hp, ts.Unix(), hpTruthHP(ts)).Error)
		}
		series, reason = run(t, f, "db:19")
		assert.NotNil(t, series, reason)
		assert.False(t, f.site.heatPump(now).Model.ShapeLearned)
		assert.Equal(t, 3, f.site.heatPump(now).Model.ShapeDays)
	})

	t.Run("the evening of today: a tariff without past hours is filled from the recorded temperature", func(t *testing.T) {
		fromNow := trueRates(5, func(now time.Time) time.Time { return now.Truncate(tariff.SlotDuration) })
		f := seedHeatPump(t, now, 45, fromNow, nil)
		profile, active, start := f.profileFor(t, 2*96)
		series, _ := f.site.heatPumpSplit(heatPumpShadow, "db:19", now, profile, active)
		require.NotNil(t, series)
		for i := range series {
			ts := start.Add(time.Duration(i) * tariff.SlotDuration)
			require.InDelta(t, hpNonHP(slotIndex(ts))+hpTruthHP(ts), series[i], 1e-9, "slot %s", ts)
		}

		// the recorded morning is flagged unusable: today keeps the active forecast, tomorrow is modelled
		f2 := seedHeatPump(t, now, 45, fromNow, nil)
		require.NoError(t, db.Instance.Exec(`UPDATE meters SET incomplete = 1 WHERE soc_temp IS NOT NULL AND ts >= ?`, startOfToday(now).Unix()).Error)
		profile, active, _ = f2.profileFor(t, 2*96)
		series, _ = f2.site.heatPumpSplit(heatPumpShadow, "db:19", now, profile, active)
		require.NotNil(t, series)
		days := f2.site.heatPump(now).Days
		require.Len(t, days, 3)
		assert.False(t, days[0].Modelled)
		assert.Equal(t, heatPumpTemperatureStale, days[0].Reason)
		assert.True(t, days[1].Modelled)
		assert.Equal(t, active[0], series[0])
	})

	t.Run("a missing meter is remembered for the retry interval, not queried every run", func(t *testing.T) {
		f := seedHeatPump(t, now, 45, rates, nil)
		_, reason := run(t, f, "db:20")
		assert.Equal(t, heatPumpNoMeter, reason)
		require.NoError(t, db.Instance.Exec(`INSERT INTO entities ("group", name, title) VALUES ('meter', 'db:20', 'late')`).Error)
		_, reason = run(t, f, "db:20")
		assert.Equal(t, heatPumpNoMeter, reason, "still cached")
		f.site.heatPumpDailyState.at = time.Now().Add(-2 * heatPumpRetry)
		_, reason = run(t, f, "db:20")
		assert.NotEqual(t, heatPumpNoMeter, reason, "found after the retry interval")
	})

	t.Run("a date the temperature forecast does not cover keeps the active forecast", func(t *testing.T) {
		short := func(now time.Time) api.Rates {
			return trueRates(3, startOfToday)(now)[:96+96+40] // the third date has 40 slots of 96
		}
		f := seedHeatPump(t, now, 45, short, nil)
		profile, active, _ := f.profileFor(t, 3*96)
		series, band := f.site.heatPumpSplit(heatPumpShadow, "db:19", now, profile, active)
		require.NotNil(t, series)
		days := f.site.heatPump(now).Days
		require.Len(t, days, 4)
		assert.True(t, days[1].Modelled)
		assert.False(t, days[2].Modelled)
		for i := range series {
			if i >= 96+(96-int(now.Sub(startOfToday(now))/tariff.SlotDuration)) && i < len(series) {
				assert.Equal(t, active[i], series[i])
				assert.Equal(t, profile.Rates[i], band[i])
			}
		}
	})
}

func TestHeatPumpOffDoesNothing(t *testing.T) {
	withBerlin(t)
	f := seedHeatPump(t, hpNow(), 45, trueRates(6, startOfToday), nil)
	profile, active, _ := f.profileFor(t, 96)

	// a closed database turns any query into an error, which would be recorded
	sqlDB, err := db.Instance.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	series, band := f.site.heatPumpSplit(heatPumpOff, "db:19", hpNow(), profile, active)
	assert.Nil(t, series)
	assert.Nil(t, band)
	assert.True(t, f.site.heatPumpLast.updated.IsZero(), "no state recorded")
	assert.Nil(t, f.site.heatPumpDailyState.res, "no analysis computed")

	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())
	saveHeatPumpSettings(t, heatPumpOff, "db:19")
	st := f.site.heatPump(hpNow())
	assert.Equal(t, heatPumpOff, st.Mode)
	assert.False(t, st.Model.Open)
	assert.Equal(t, heatPumpOff, st.Model.Reason)
	assert.Empty(t, st.ShadowError)
	assert.Empty(t, st.Days)
	assert.Nil(t, f.site.heatPumpDailyState.res)
}

// homeProfileSite is a fixture whose data ends now, so homeProfile can run on the clock.
func homeProfileSite(t *testing.T, mode, meter string, rates func(now time.Time) api.Rates) (*Site, time.Time) {
	t.Helper()
	withBerlin(t)
	now := time.Now()
	f := seedHeatPump(t, now, 45, rates, nil)
	f.site.energyInsights.Settings.HeatPumpModel = mode
	f.site.energyInsights.Settings.HeatPumpMeter = meter
	return f.site, now
}

func TestHeatPumpModeWiring(t *testing.T) {
	withBerlin(t)
	rates := trueRates(9, startOfToday)
	const n = 3 * 96

	// run sets the mode on the site and returns the plan input of one homeProfile call
	run := func(t *testing.T, site *Site, mode string) []float64 {
		t.Helper()
		site.energyInsights.Settings.HeatPumpModel = mode
		res, err := site.homeProfile(n)
		require.NoError(t, err)
		return res
	}

	t.Run("off does nothing", func(t *testing.T) {
		site, _ := homeProfileSite(t, heatPumpOff, "db:19", rates)
		run(t, site, heatPumpOff)
		assert.NotContains(t, site.energyHomeShadow, heatPumpVariant)
		assert.NotContains(t, site.energyHomeShadow, profileVariant)
		assert.Nil(t, site.energyHeatPumpBand)
		assert.False(t, site.energyHeatPumpOn)
		assert.True(t, site.heatPumpLast.updated.IsZero())
		assert.Nil(t, site.heatPumpDailyState.res)
	})

	t.Run("shadow leaves the plan input identical and records both variants", func(t *testing.T) {
		site, _ := homeProfileSite(t, heatPumpOff, "db:19", rates)
		off := run(t, site, heatPumpOff)
		res := run(t, site, "") // "" is shadow
		assert.Equal(t, off, res, "planning input unchanged")
		require.Contains(t, site.energyHomeShadow, heatPumpVariant, site.heatPumpLast.reason)
		require.Contains(t, site.energyHomeShadow, profileVariant)
		assert.False(t, site.energyHeatPumpOn)
		assert.NotNil(t, site.energyHeatPumpBand)
		assert.NotEqual(t, off, site.energyHomeShadow[heatPumpVariant])
		for i, v := range site.energyHomeShadow[profileVariant] {
			assert.InDelta(t, site.energyProfile.Rates[i].Base, v, 1e-9, "the uncorrected profile, Wh")
		}
		assert.True(t, site.heatPumpLast.open)
	})

	t.Run("shadow with a closed model archives nothing for it and says why", func(t *testing.T) {
		site, _ := homeProfileSite(t, heatPumpOff, "", rates)
		off := run(t, site, heatPumpOff)
		res := run(t, site, heatPumpShadow)
		assert.Equal(t, off, res)
		assert.NotContains(t, site.energyHomeShadow, heatPumpVariant)
		assert.Contains(t, site.energyHomeShadow, profileVariant)
		assert.Equal(t, heatPumpNoMeter, site.heatPumpLast.reason)
	})

	t.Run("on swaps the forecast in, the heating-degree result stays as a shadow", func(t *testing.T) {
		site, _ := homeProfileSite(t, heatPumpOff, "db:19", rates)
		site.energyInsights.Settings.HeatingDegree = heatingDegreeOn
		off := run(t, site, heatPumpOff)
		res := run(t, site, heatPumpOn)
		require.True(t, site.energyHeatPumpOn, site.heatPumpLast.reason)
		assert.Equal(t, site.energyHomeShadow[heatPumpVariant], res)
		assert.NotEqual(t, off, res)
		assert.Equal(t, off, site.energyHomeShadow[heatingDegreeOn], "the heating-degree result in use before, now a shadow")
		assert.Contains(t, site.energyHomeShadow, profileVariant)
		assert.NotContains(t, site.energyHomeShadow, heatingDegreeOff)
	})

	t.Run("on falls back to the heating-degree result while closed", func(t *testing.T) {
		for name, c := range map[string]struct {
			meter  string
			rates  func(now time.Time) api.Rates
			reason string
		}{
			"stale temperature": {"db:19", nil, heatPumpTemperatureStale},
			"no meter":          {"", rates, heatPumpNoMeter},
		} {
			site, _ := homeProfileSite(t, heatPumpOff, c.meter, c.rates)
			off := run(t, site, heatPumpOff)
			res := run(t, site, heatPumpOn)
			assert.Equal(t, off, res, name)
			assert.False(t, site.energyHeatPumpOn, name)
			assert.Nil(t, site.energyHeatPumpBand, name)
			assert.NotContains(t, site.energyHomeShadow, heatPumpVariant, name)
			assert.Equal(t, c.reason, site.heatPumpLast.reason, name)
		}
	})
}

// hpArchiveSite is shadowSite with the heat-pump variants: flat profile band 100/200/400 Wh,
// hp-split band 150/300/360 Wh, home forecasts per variant.
func hpArchiveSite(t *testing.T, start time.Time, n int, hpOpen, hpOn bool) (*Site, optimizer.OptimizationInput, requestDetails) {
	t.Helper()
	site, req, details := shadowSite(t, start, n, heatingDegreeAuto)
	site.energyHomeShadow[profileVariant] = make([]float64, n)
	site.energyHeatPumpBand = nil
	for i := range n {
		site.energyHomeShadow[profileVariant][i] = 200
	}
	if hpOpen {
		site.energyHomeShadow[heatPumpVariant] = make([]float64, n)
		for i := range n {
			site.energyHomeShadow[heatPumpVariant][i] = 300
			site.energyHeatPumpBand = append(site.energyHeatPumpBand, metrics.HomeForecastSlot{Start: details.Timestamps[i], Low: 150, Base: 300, High: 360})
		}
	}
	if hpOn {
		site.energyHome, site.energyHeatPumpOn = site.energyHomeShadow[heatPumpVariant], true
		site.energyHomeShadow[heatingDegreeAuto] = make([]float64, n)
		for i := range n {
			site.energyHomeShadow[heatingDegreeAuto][i] = 250
		}
	}
	return site, req, details
}

func variantCounts(t *testing.T) map[string]int {
	t.Helper()
	res := map[string]int{}
	for _, r := range shadowRows(t) {
		res[r.Variant]++
	}
	return res
}

func TestHeatPumpShadowArchive(t *testing.T) {
	loc := berlin(t)
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = loc
	day := time.Date(2026, 2, 3, 0, 0, 0, 0, loc)

	t.Run("snapshot: hp-split and profile beside auto and on", func(t *testing.T) {
		site, req, details := hpArchiveSite(t, day, 96, true, false)
		site.archiveHeatingDegreeShadow(req, details, day.Add(2*time.Minute))
		assert.Equal(t, map[string]int{heatingDegreeAuto: 97, heatingDegreeOn: 97, heatPumpVariant: 97, profileVariant: 97}, variantCounts(t))
		assert.True(t, site.heatingDegreeSnapshot.Equal(day))

		for _, r := range shadowRows(t) {
			switch r.Variant {
			case heatPumpVariant:
				assert.InDelta(t, 0.3, r.Base, 1e-12)
				assert.InDelta(t, 0.15, r.Low, 1e-12, "the split's own band, not the profile's")
				assert.InDelta(t, 0.36, r.High, 1e-12)
			case profileVariant:
				assert.InDelta(t, 0.2, r.Base, 1e-12)
				assert.InDelta(t, 0.1, r.Low, 1e-12)
				assert.InDelta(t, 0.4, r.High, 1e-12)
			}
			assert.LessOrEqual(t, r.Low, r.Base)
			assert.LessOrEqual(t, r.Base, r.High)
		}
	})

	t.Run("a closed hp-split archives nothing for it, the others pair as before", func(t *testing.T) {
		site, req, details := hpArchiveSite(t, day, 96, false, false)
		site.archiveHeatingDegreeShadow(req, details, day.Add(2*time.Minute))
		counts := variantCounts(t)
		assert.Zero(t, counts[heatPumpVariant])
		assert.Equal(t, 97, counts[heatingDegreeOn])
		assert.Equal(t, 97, counts[profileVariant])
		assert.True(t, site.heatingDegreeSnapshot.Equal(day), "the period counts as taken")

		// the day was measured: the heating-degree windows are complete, hp-split's lacks every slot
		for ts := day; ts.Before(day.AddDate(0, 0, 1)); ts = ts.Add(tariff.SlotDuration) {
			require.NoError(t, db.Instance.Exec(`INSERT INTO meters (meter, ts, energy) VALUES (1, ?, 0.2)`, ts.Unix()).Error)
		}
		to := day.AddDate(0, 0, 2)
		w, err := metrics.HomeForecastShadowWindows(heatingDegreeSnapshotWindow, []string{heatingDegreeAuto, heatingDegreeOn}, to)
		require.NoError(t, err)
		require.Len(t, w, 1)
		assert.Equal(t, 1, w[0].Missing, "slot 0 started before the issue and is never archived")
		w, err = metrics.HomeForecastShadowWindows(heatingDegreeSnapshotWindow, []string{heatingDegreeOn, profileVariant}, to)
		require.NoError(t, err)
		require.Len(t, w, 1)
		assert.Equal(t, 1, w[0].Missing, "profile pairs with on")
		w, err = metrics.HomeForecastShadowWindows(heatingDegreeSnapshotWindow, []string{heatingDegreeOn, heatPumpVariant}, to)
		require.NoError(t, err)
		require.Len(t, w, 1)
		assert.Equal(t, 96, w[0].Missing, "no hp-split: the window is not scored")
	})

	t.Run("lead rows of hp-split, profile only in snapshots", func(t *testing.T) {
		issued := time.Date(2026, 2, 3, 9, 7, 0, 0, loc)
		site, req, details := hpArchiveSite(t, issued.Truncate(tariff.SlotDuration), 192, true, false)
		site.archiveHeatingDegreeShadow(req, details, issued)
		counts := variantCounts(t)
		assert.Equal(t, 3, counts[heatPumpVariant])
		assert.Zero(t, counts[profileVariant])
		assert.Equal(t, 3, counts[heatingDegreeOn])
	})

	t.Run("on: the swapped-out heating-degree mode is archived as a shadow, hp-split too", func(t *testing.T) {
		issued := time.Date(2026, 2, 3, 9, 7, 0, 0, loc)
		site, req, details := hpArchiveSite(t, issued.Truncate(tariff.SlotDuration), 192, true, true)
		site.archiveHeatingDegreeShadow(req, details, issued)
		counts := variantCounts(t)
		assert.Equal(t, 3, counts[heatingDegreeAuto])
		assert.Equal(t, 3, counts[heatPumpVariant])
		for _, r := range shadowRows(t) {
			if r.Variant == heatingDegreeAuto {
				assert.InDelta(t, 0.25, r.Base, 1e-12)
				assert.InDelta(t, 0.25-0.1, r.Low, 1e-12, "shifted profile band")
			}
		}

	})

	t.Run("the forecast in use is the split with its own band", func(t *testing.T) {
		site, req, details := hpArchiveSite(t, time.Now().Truncate(tariff.SlotDuration), 192, true, true)
		require.NoError(t, site.energyForecast(req, details))
		var rows []struct{ Base, Low, High float64 }
		require.NoError(t, db.Instance.Table("home_forecast_samples").Select("base, low, high").Scan(&rows).Error)
		require.NotEmpty(t, rows)
		for _, r := range rows {
			assert.InDelta(t, 0.3, r.Base, 1e-12)
			assert.InDelta(t, 0.15, r.Low, 1e-12)
			assert.InDelta(t, 0.36, r.High, 1e-12)
		}
	})

	t.Run("a blended first slot keeps the band valid", func(t *testing.T) {
		site, req, details := hpArchiveSite(t, day, 96, true, false)
		site.energyHomeShadow[heatPumpVariant][1] = 10 // the measured slot pulled the forecast far below its band
		site.energyHomeShadow[profileVariant][1] = 900
		site.archiveHeatingDegreeShadow(req, details, day.Add(2*time.Minute))
		assert.Equal(t, 97, variantCounts(t)[heatPumpVariant])
		assert.Equal(t, 97, variantCounts(t)[profileVariant])
	})
}

func TestScoreHeatPump(t *testing.T) {
	window := func(start time.Time, actual, hp, profile, on, auto float64) metrics.HomeForecastShadowWindow {
		return metrics.HomeForecastShadowWindow{Start: start, Actual: actual, Variants: map[string]float64{
			heatPumpVariant: hp, profileVariant: profile, heatingDegreeOn: on, heatingDegreeAuto: auto,
		}}
	}
	loc := berlin(t)
	byType := func(n int, hp, profile, on, auto func(i int) float64) map[string][]metrics.HomeForecastShadowWindow {
		res := map[string][]metrics.HomeForecastShadowWindow{}
		for i := range n {
			d := time.Date(2026, 2, 1+i, 0, 0, 0, 0, loc)
			res["day"] = append(res["day"], window(d, 20, hp(i), profile(i), on(i), auto(i)))
			res["overnight"] = append(res["overnight"], window(d.Add(17*time.Hour), 8, hp(i)-12, profile(i)-12, on(i)-12, auto(i)-12))
		}
		return res
	}
	wiggle := func(base float64) func(int) float64 { return func(i int) float64 { return base + float64(i%3)*0.2 } }

	t.Run("beats everything", func(t *testing.T) {
		scores, d := scoreHeatPump(byType(14, wiggle(20.5), wiggle(24), wiggle(22), wiggle(23)))
		require.Len(t, scores, 6)
		for _, s := range scores {
			assert.True(t, s.Ready, "%s vs %s", s.Window, s.Versus)
			assert.Equal(t, 14, s.Windows)
			assert.Equal(t, 14, s.Required)
			require.NotNil(t, s.Delta)
			assert.Negative(t, *s.Delta)
			assert.Negative(t, *s.CIHigh)
			assert.InDelta(t, *s.MAEHeatPump-*s.MAEOther, *s.Delta, 1e-12)
		}
		assert.True(t, d.Ready)
		assert.True(t, d.BeatsProfile)
		assert.True(t, d.BeatsHeatingDegOn)
		assert.Equal(t, 14, d.Required)
		assert.NotEmpty(t, d.Rule)
	})

	t.Run("collecting", func(t *testing.T) {
		scores, d := scoreHeatPump(byType(13, wiggle(20.5), wiggle(24), wiggle(22), wiggle(23)))
		for _, s := range scores {
			assert.False(t, s.Ready)
			assert.Equal(t, 13, s.Windows)
			assert.Nil(t, s.Delta)
		}
		assert.False(t, d.Ready)
		assert.False(t, d.BeatsProfile)
		assert.False(t, d.BeatsHeatingDegOn)
	})

	t.Run("better than the profile but only tying heating-degree on parks it", func(t *testing.T) {
		scores, d := scoreHeatPump(byType(20, wiggle(20.5), wiggle(24), wiggle(20.5), wiggle(23)))
		assert.True(t, d.Ready)
		assert.True(t, d.BeatsProfile)
		assert.False(t, d.BeatsHeatingDegOn)
		for _, s := range scores {
			if s.Versus == heatingDegreeOn {
				assert.Zero(t, *s.Delta)
			}
		}
	})

	t.Run("windows lacking a variant are not scored", func(t *testing.T) {
		m := byType(20, wiggle(20.5), wiggle(24), wiggle(22), wiggle(23))
		for i := range m["day"] {
			m["day"][i].Missing = 5
		}
		scores, d := scoreHeatPump(m)
		assert.Zero(t, scores[0].Windows)
		assert.False(t, d.Ready)
	})

	t.Run("the heating-degree score is the same function of the same pairs", func(t *testing.T) {
		m := byType(14, wiggle(20.5), wiggle(24), wiggle(22), wiggle(23))
		a := scoreHeatingDegreeShadow("day", 4, m["day"])
		p := scoreShadowPair(4, m["day"], heatingDegreeOn, heatingDegreeAuto)
		require.True(t, a.Ready)
		assert.Equal(t, *p.Delta, *a.Delta)
		assert.Equal(t, *p.CILow, *a.CILow)
		assert.Equal(t, *p.MAEA, *a.MAEOn)
		assert.Equal(t, *p.MAEB, *a.MAEAuto)
	})
}

func TestHeatPumpStatusJSON(t *testing.T) {
	withBerlin(t)
	now := time.Now()
	f := seedHeatPump(t, now, 45, trueRates(6, startOfToday), nil)

	saveHeatPumpSettings(t, "", "")
	st := f.site.heatPump(now)
	assert.Equal(t, heatPumpShadow, st.Mode, "nothing saved: shadow")
	assert.Equal(t, heatPumpNoMeter, st.Model.Reason)
	assert.False(t, st.Model.Open)

	saveHeatPumpSettings(t, heatPumpOn, "db:19")
	pending := f.site.heatPump(now)
	assert.False(t, pending.Model.Open, "the daily fit alone does not say the forecast would run")
	assert.Equal(t, heatPumpPending, pending.Model.Reason)
	require.NotNil(t, pending.Model.Fit)
	start := now.Truncate(tariff.SlotDuration)
	profile, err := metrics.HomeForecast(start, start.Add(96*tariff.SlotDuration))
	require.NoError(t, err)
	active := make([]float64, 96)
	series, _ := f.site.heatPumpSplit(heatPumpOn, "db:19", now, profile, active)
	require.NotNil(t, series)

	b, err := json.Marshal(f.site.heatPump(now))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, "on", got["mode"])
	assert.Equal(t, "db:19", got["meter"])
	model := got["model"].(map[string]any)
	assert.Equal(t, true, model["open"])
	fit := model["fit"].(map[string]any)
	assert.InDelta(t, hpTruthB, fit["b"], 1e-9)
	assert.InDelta(t, hpTruthA, fit["a"], 1e-9)
	assert.InDelta(t, 1, fit["r"], 1e-9)
	assert.InDelta(t, fit["maxHDD"].(float64)+3, fit["hddClamp"], 1e-9)
	assert.InDelta(t, fit["maxDailyKWh"].(float64)*1.5, fit["dailyCapKWh"], 1e-9)
	for _, key := range []string{"days", "hddRange", "validDays", "maxHDD", "hddClamp", "maxDailyKWh", "dailyCapKWh"} {
		assert.Contains(t, fit, key)
	}
	for _, key := range []string{"shapeDays", "droppedSlots", "shapeLearned"} {
		assert.Contains(t, model, key)
	}
	days := got["days"].([]any)
	require.NotEmpty(t, days)
	for _, key := range []string{"date", "heatPumpKWh", "nonHeatPumpKWh", "totalKWh", "hdd", "extrapolated"} {
		assert.Contains(t, days[0], key)
	}
	assert.Contains(t, got["thresholds"], "minDays")
	assert.Len(t, got["shadow"], 6)
	decision := got["decision"].(map[string]any)
	assert.Equal(t, false, decision["ready"])
	assert.EqualValues(t, 14, decision["required"])
	assert.Contains(t, decision, "rule")

	// all numbers are finite
	assert.False(t, math.IsNaN(fit["b"].(float64)))
}
