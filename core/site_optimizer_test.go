package core

import (
	"slices"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/types"
	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/jinzhu/now"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestLoadpointProfile(t *testing.T) {
	ctrl := gomock.NewController(t)

	lp := loadpoint.NewMockAPI(ctrl)
	lp.EXPECT().GetMode().Return(api.ModeMinPV).AnyTimes()
	lp.EXPECT().GetStatus().Return(api.StatusC).AnyTimes()
	lp.EXPECT().GetChargePower().Return(10000.0).AnyTimes()   //  10 kW
	lp.EXPECT().EffectiveMinPower().Return(1000.0).AnyTimes() //   1 kW
	lp.EXPECT().GetRemainingEnergy().Return(1.8).AnyTimes()   // 1.8 kWh

	// expected slots: 0.25 kWh...
	require.Equal(t, []float64{250, 250, 250, 250, 250, 250, 250, 50}, loadpointProfile(lp, 8))
}

func TestOptimizerHorizon(t *testing.T) {
	ts := time.Date(2025, 1, 1, 10, 30, 0, 0, time.Local)
	horizon := optimizerHorizon(ts)

	// 48h plus end of day
	assert.Equal(t, time.Date(2025, 1, 3, 23, 59, 59, int(time.Second-time.Nanosecond), time.Local), horizon)

	// before 6:00 the day is not extended
	assert.Equal(t, time.Date(2025, 1, 3, 5, 30, 0, 0, time.Local),
		optimizerHorizon(time.Date(2025, 1, 1, 5, 30, 0, 0, time.Local)))

	rates := make(api.Rates, 0, 4*96)
	for slot := ts.Truncate(tariff.SlotDuration); len(rates) < cap(rates); slot = slot.Add(tariff.SlotDuration) {
		rates = append(rates, api.Rate{Start: slot, End: slot.Add(tariff.SlotDuration)})
	}

	// 4 days of slots from 10:30, capped at the last slot of Jan 3rd
	assert.Equal(t, 246, slotsUntil(rates, horizon, len(rates)))
	assert.Equal(t, time.Date(2025, 1, 3, 23, 45, 0, 0, time.Local), rates[245].Start)

	// shorter forecast is not extended
	assert.Equal(t, 8, slotsUntil(rates, horizon, 8))
}

func TestApplyPrecondition(t *testing.T) {
	ctrl := gomock.NewController(t)

	lp := loadpoint.NewMockAPI(ctrl)
	lp.EXPECT().EffectiveMaxPower().Return(8000.0).AnyTimes() // 2 kWh per slot

	// no precondition configured
	lp.EXPECT().EffectivePlanStrategy().Return(api.PlanStrategy{}).Times(1)
	assert.Nil(t, applyPrecondition(lp, nil, 8))

	// no plan
	lp.EXPECT().EffectivePlanStrategy().Return(api.PlanStrategy{Precondition: time.Hour}).Times(1)
	lp.EXPECT().EffectivePlanTime().Return(time.Time{}).Times(1)
	assert.Nil(t, applyPrecondition(lp, nil, 8))

	// plan in 1h, 40min precondition: slots 1 (10min) and 2, 3 (full)
	lp.EXPECT().EffectivePlanTime().Return(time.Now().Add(time.Hour)).Times(1)
	lp.EXPECT().EffectivePlanStrategy().Return(api.PlanStrategy{Precondition: 40 * time.Minute}).Times(1)
	res := applyPrecondition(lp, nil, 8)
	require.Len(t, res, 8)
	assert.InDeltaSlice(t, []float32{0, 2000. / 1.5, 2000, 2000, 0, 0, 0, 0}, res, 1)

	// existing demand is kept where higher
	lp.EXPECT().EffectivePlanTime().Return(time.Now().Add(time.Hour)).Times(1)
	lp.EXPECT().EffectivePlanStrategy().Return(api.PlanStrategy{Precondition: 30 * time.Minute}).Times(1)
	res = applyPrecondition(lp, []float32{3000, 3000, 3000, 3000, 0, 0, 0, 0}, 8)
	assert.InDeltaSlice(t, []float32{3000, 3000, 3000, 3000, 0, 0, 0, 0}, res, 1)

	// plan beyond horizon
	lp.EXPECT().EffectivePlanTime().Return(time.Now().Add(24 * time.Hour)).Times(1)
	lp.EXPECT().EffectivePlanStrategy().Return(api.PlanStrategy{Precondition: time.Hour}).Times(1)
	assert.Nil(t, applyPrecondition(lp, nil, 8))
}

func TestLoadpointCurrentAction(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		status  api.ChargeStatus
		soc     float64
		want    string
	}{
		{"charging", true, api.StatusC, 0, actionCharge},
		{"enabled but idle (e.g. vehicle finished at limit)", true, api.StatusB, 0, actionStop},
		{"disabled", false, api.StatusB, 0, actionStop},
		{"charging at 100% soc with no explicit limit", true, api.StatusC, 100, actionStop},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lp := &Loadpoint{enabled: tc.enabled, status: tc.status, vehicleSoc: tc.soc}
			assert.Equal(t, tc.want, loadpointCurrentAction(lp))
		})
	}
}

func TestAsTimestamps(t *testing.T) {
	// now is 10 minutes into a 15-minute slot
	now := time.Date(2025, 1, 1, 12, 10, 0, 0, time.UTC)

	// dt[0]=300 means first event is 300s (5min) before end of current slot
	// dt[1..] just mark subsequent slot boundaries
	dt := []int{60 * 5, 60 * 15, 60 * 15}

	got := asTimestamps(dt, now)

	// current slot: 12:00–12:15
	// first timestamp: 12:15 - 5min = 12:10
	// subsequent: 12:15, 12:30
	assert.Equal(t, []time.Time{
		time.Date(2025, 1, 1, 12, 10, 0, 0, time.UTC),
		time.Date(2025, 1, 1, 12, 15, 0, 0, time.UTC),
		time.Date(2025, 1, 1, 12, 30, 0, 0, time.UTC),
	}, got)
}

func TestUnmodelledPower(t *testing.T) {
	ctrl := gomock.NewController(t)

	for _, tc := range []struct {
		name            string
		mode            api.ChargeMode
		status          api.ChargeStatus
		power, minPower float64
		expected        float64
	}{
		{"pv charging", api.ModePV, api.StatusC, 4000, 1380, 4000},
		{"pv connected", api.ModePV, api.StatusB, 0, 1380, 0},
		{"minpv floor before meter caught up", api.ModeMinPV, api.StatusC, 0, 4000, 4000},
		{"minpv floor must not lower measured", api.ModeMinPV, api.StatusC, 4000, 1000, 4000},
		{"minpv floor only applies while charging", api.ModeMinPV, api.StatusB, 0, 4000, 0},
		{"negative measurement clamped", api.ModePV, api.StatusC, -100, 0, 0},
	} {
		lp := loadpoint.NewMockAPI(ctrl)
		lp.EXPECT().GetMode().Return(tc.mode).AnyTimes()
		lp.EXPECT().GetStatus().Return(tc.status).AnyTimes()
		lp.EXPECT().GetChargePower().Return(tc.power).AnyTimes()
		lp.EXPECT().EffectiveMinPower().Return(tc.minPower).AnyTimes()

		assert.Equal(t, tc.expected, unmodelledPower(lp), tc.name)
	}
}

func TestBatteryForecastSocExtremes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		req       []optimizer.BatteryConfig
		soc       [][]float32
		high, low *batteryForecastSlot
	}{
		{
			"no home battery",
			[]optimizer.BatteryConfig{{SMax: 80}}, // SCapacity unset → vehicle
			[][]float32{{1000, 2000}},
			nil, nil,
		},
		{
			"single home battery rising — reaches full",
			[]optimizer.BatteryConfig{{SCapacity: 1000, SMax: 1000}},
			[][]float32{{200, 500, 1000}},
			&batteryForecastSlot{slot: 2, soc: 100, limit: true},
			&batteryForecastSlot{slot: 0, soc: 20, limit: false},
		},
		{
			"single home battery falling — reaches empty",
			[]optimizer.BatteryConfig{{SCapacity: 1000, SMax: 1000}},
			[][]float32{{900, 500, 0}},
			&batteryForecastSlot{slot: 0, soc: 90, limit: false},
			&batteryForecastSlot{slot: 2, soc: 0, limit: true},
		},
		{
			"single home battery — local extremes (no limit reached)",
			[]optimizer.BatteryConfig{{SCapacity: 1000, SMax: 900, SMin: 100}},
			[][]float32{{500, 800, 200}},
			&batteryForecastSlot{slot: 1, soc: 80, limit: false},
			&batteryForecastSlot{slot: 2, soc: 20, limit: false},
		},
		{
			"two home batteries aggregated",
			[]optimizer.BatteryConfig{
				{SCapacity: 1000, SMax: 1000},
				{SCapacity: 1000, SMax: 1000},
			},
			[][]float32{
				{200, 400, 1000},
				{800, 400, 1000},
			},
			&batteryForecastSlot{slot: 2, soc: 100, limit: true},
			&batteryForecastSlot{slot: 1, soc: 40, limit: false},
		},
		{
			"vehicle and home battery — vehicle ignored",
			[]optimizer.BatteryConfig{
				{SMax: 80},                    // vehicle
				{SCapacity: 1000, SMax: 1000}, // home
			},
			[][]float32{
				{0, 0, 80},
				{200, 500, 900},
			},
			&batteryForecastSlot{slot: 2, soc: 90, limit: false},
			&batteryForecastSlot{slot: 0, soc: 20, limit: false},
		},
		{
			"first slot at SMax wins for highest",
			[]optimizer.BatteryConfig{{SCapacity: 1000, SMax: 1000}},
			[][]float32{{500, 1000, 1000}},
			&batteryForecastSlot{slot: 1, soc: 100, limit: true},
			&batteryForecastSlot{slot: 0, soc: 50, limit: false},
		},
		{
			"already full — no highest",
			[]optimizer.BatteryConfig{{SCapacity: 1000, SMax: 1000}},
			[][]float32{{1000, 1000, 500}},
			nil,
			&batteryForecastSlot{slot: 2, soc: 50, limit: false},
		},
		{
			"already empty — no lowest",
			[]optimizer.BatteryConfig{{SCapacity: 1000, SMax: 1000, SMin: 100}},
			[][]float32{{100, 100, 500}},
			&batteryForecastSlot{slot: 2, soc: 50, limit: false},
			nil,
		},
		{
			"near SMax is not full",
			[]optimizer.BatteryConfig{{SCapacity: 1000, SMax: 1000}},
			[][]float32{{500, 999, 800}},
			&batteryForecastSlot{slot: 1, soc: 99.9, limit: false},
			&batteryForecastSlot{slot: 0, soc: 50, limit: false},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := make([]optimizer.BatteryResult, len(tc.soc))
			for i, s := range tc.soc {
				resp[i] = optimizer.BatteryResult{StateOfCharge: s}
			}

			high, low := batteryForecastSocExtremes(tc.req, resp)

			if tc.high == nil {
				assert.Nil(t, high, "high")
			} else {
				require.NotNil(t, high, "high")
				assert.Equal(t, tc.high.slot, high.slot, "high.slot")
				assert.InDelta(t, tc.high.soc, high.soc, 1e-3, "high.soc")
				assert.Equal(t, tc.high.limit, high.limit, "high.limit")
			}
			if tc.low == nil {
				assert.Nil(t, low, "low")
			} else {
				require.NotNil(t, low, "low")
				assert.Equal(t, tc.low.slot, low.slot, "low.slot")
				assert.InDelta(t, tc.low.soc, low.soc, 1e-3, "low.soc")
				assert.Equal(t, tc.low.limit, low.limit, "low.limit")
			}
		})
	}
}

// TestBatteryWillRefillToday covers the boundary sitePower's buffer relaxation relies on:
// only a forecast that actually reached SMax (Limit), and does so before the end of the
// current day, counts as "will refill by evening." A high point that never reached the
// limit, or one that reaches it only on a later day, must not be trusted - the caller's
// fallback (today's static buffer thresholds) is the safe default in both cases.
func TestBatteryWillRefillToday(t *testing.T) {
	asOf := time.Date(2026, 6, 15, 10, 0, 0, 0, time.Local)

	tc := []struct {
		name     string
		forecast *types.BatteryForecast
		want     bool
	}{
		{"no forecast at all", nil, false},
		{"forecast with no highest point", &types.BatteryForecast{}, false},
		{
			"reaches SMax later today",
			&types.BatteryForecast{Highest: &types.BatteryForecastPoint{Limit: true, Time: asOf.Add(4 * time.Hour)}},
			true,
		},
		{
			"trends up but never reaches SMax",
			&types.BatteryForecast{Highest: &types.BatteryForecastPoint{Limit: false, Soc: 92, Time: asOf.Add(4 * time.Hour)}},
			false,
		},
		{
			"reaches SMax, but only tomorrow",
			&types.BatteryForecast{Highest: &types.BatteryForecastPoint{Limit: true, Time: asOf.AddDate(0, 0, 1).Add(2 * time.Hour)}},
			false,
		},
		{
			"reaches SMax exactly at end of day",
			&types.BatteryForecast{Highest: &types.BatteryForecastPoint{Limit: true, Time: now.With(asOf).EndOfDay()}},
			true,
		},
		{
			// a stale forecast run: the peak it predicted has already come and gone
			// without the battery actually refilling, so it must no longer count as
			// "will refill by evening" even though it is technically still before EOD
			"reached SMax, but the peak is already in the past",
			&types.BatteryForecast{Highest: &types.BatteryForecastPoint{Limit: true, Time: asOf.Add(-30 * time.Minute)}},
			false,
		},
		{
			"peak exactly now does not count as still ahead",
			&types.BatteryForecast{Highest: &types.BatteryForecastPoint{Limit: true, Time: asOf}},
			false,
		},
	}

	for _, c := range tc {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, batteryWillRefillToday(c.forecast, asOf))
		})
	}
}

// TestBatteryRequestSocLimitsClamp ensures the reported soc is always clamped into
// the resulting [SMin, SMax] range, even when it lies outside the configured soc
// limits (e.g. right after a firmware update changed the reported soc or the min/max
// soc settings) - otherwise the optimizer is infeasible from the first slot.
func TestBatteryRequestSocLimitsClamp(t *testing.T) {
	newBatteryDevice := func(t *testing.T, minSoc, maxSoc float64) config.Device[api.Meter] {
		ctrl := gomock.NewController(t)

		var meter api.Meter
		batSocLimit := api.NewMockBatterySocLimiter(ctrl)
		batSocLimit.EXPECT().GetSocLimits().Return(minSoc, maxSoc).AnyTimes()

		bat := &struct {
			api.Meter
			api.BatterySocLimiter
		}{
			Meter:             meter,
			BatterySocLimiter: batSocLimit,
		}

		return config.NewStaticDevice(config.Named{}, api.Meter(bat))
	}

	site := &Site{log: util.NewLogger("foo")}
	capacity := 10.0 // kWh

	t.Run("soc below minSoc", func(t *testing.T) {
		soc := 15.0
		dev := newBatteryDevice(t, 20, 100)
		m := types.Measurement{Capacity: &capacity, Soc: &soc}

		req, _ := site.batteryRequest(dev, m, nil, 8, 15*time.Minute)

		assert.Equal(t, float32(1500), req.SMin)
		assert.Equal(t, float32(10000), req.SMax)
		assert.LessOrEqual(t, req.SMin, req.SInitial)
	})

	t.Run("soc above maxSoc", func(t *testing.T) {
		soc := 95.0
		dev := newBatteryDevice(t, 0, 80)
		m := types.Measurement{Capacity: &capacity, Soc: &soc}

		req, _ := site.batteryRequest(dev, m, nil, 8, 15*time.Minute)

		assert.Equal(t, float32(0), req.SMin)
		assert.Equal(t, float32(9500), req.SMax)
		assert.GreaterOrEqual(t, req.SMax, req.SInitial)
	})

	t.Run("soc within limits", func(t *testing.T) {
		soc := 50.0
		dev := newBatteryDevice(t, 20, 80)
		m := types.Measurement{Capacity: &capacity, Soc: &soc}

		req, _ := site.batteryRequest(dev, m, nil, 8, 15*time.Minute)

		assert.Equal(t, float32(2000), req.SMin)
		assert.Equal(t, float32(8000), req.SMax)
	})

	t.Run("empty maxSoc defaults to 100%", func(t *testing.T) {
		soc := 50.0
		dev := newBatteryDevice(t, 20, 0)
		m := types.Measurement{Capacity: &capacity, Soc: &soc}

		req, _ := site.batteryRequest(dev, m, nil, 8, 15*time.Minute)

		assert.Equal(t, float32(2000), req.SMin)
		assert.Equal(t, float32(10000), req.SMax)
	})
}

// persistBatteryQuarterHours drives a collector through len(powersW) consecutive 15min
// slots, each ending up persisted with the energy that constant power implies (Wh = W *
// 0.25h). Positive power lands in the discharge column, negative in the charge column - see
// the battery accumulation convention documented on metrics.Collector.BatteryPowerSamples.
func persistBatteryQuarterHours(t *testing.T, c *metrics.Collector, clk *clock.Mock, powersW []float64) {
	t.Helper()
	for _, p := range powersW {
		clk.Add(15 * time.Minute)
		require.NoError(t, c.AddEnergy(nil, nil, p, false))
	}
}

// testBatteryCapacity is the capacity (kWh) every subtest below models, so batteryMaxCRate
// puts the plausibility ceiling at 2 * 10kWh = 20kW - comfortably above every genuine
// fixture power and far below every corrupt one.
const testBatteryCapacity = 10.0

// TestBatteryPowerLimits verifies the fallback and observed-maximum paths of
// batteryPowerLimits end to end (Site -> metrics.Collector -> sqlite -> W). History must only
// ever raise the batteryPower fallback, never lower it - see the function's doc comment.
func TestBatteryPowerLimits(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())

	site := &Site{log: util.NewLogger("foo"), collectors: map[string]*metrics.Collector{}}

	t.Run("no collector for this device: falls back", func(t *testing.T) {
		charge, discharge := site.batteryPowerLimits("unknown", testBatteryCapacity)
		assert.Equal(t, float64(batteryPower), charge)
		assert.Equal(t, float64(batteryPower), discharge)
	})

	t.Run("not enough history: falls back", func(t *testing.T) {
		clk := clock.NewMock()
		clk.Set(time.Now().Truncate(tariff.SlotDuration))

		c, err := metrics.NewCollector(metrics.Battery, "sparse", "", metrics.WithClock(clk))
		require.NoError(t, err)
		require.NoError(t, c.AddEnergy(nil, nil, 0, false)) // baseline, no persist yet

		// well below batteryPowerMinSamples in each direction
		persistBatteryQuarterHours(t, c, clk, []float64{4000, 4000, 4000, -8000, -8000})

		site.collectors["sparse"] = c
		charge, discharge := site.batteryPowerLimits("sparse", testBatteryCapacity)
		assert.Equal(t, float64(batteryPower), charge)
		assert.Equal(t, float64(batteryPower), discharge)
	})

	t.Run("enough history above the fallback: raises the limit to the observed maximum", func(t *testing.T) {
		clk := clock.NewMock()
		clk.Set(time.Now().Truncate(tariff.SlotDuration))

		c, err := metrics.NewCollector(metrics.Battery, "seasoned", "", metrics.WithClock(clk))
		require.NoError(t, err)
		require.NoError(t, c.AddEnergy(nil, nil, 0, false)) // baseline, no persist yet

		// exactly batteryPowerMinSamples (20) slots per direction: mostly modest slot averages
		// (as a real battery running well under its cap for most 15min windows would produce)
		// plus one slot that came closest to running at full power for the whole window - the
		// demonstrated maximum, not an average of the whole history, becomes the limit. A
		// single such slot has to be enough: a battery that reaches its peak rarely must not
		// be modelled at half its real power for it (which is what screening by rank, rather
		// than by physical plausibility, would do here).
		dischargePowers := append(slices.Repeat([]float64{4000}, 19), 12000) // demonstrated sustained capability
		chargePowers := append(slices.Repeat([]float64{-5000}, 19), -15000)

		persistBatteryQuarterHours(t, c, clk, dischargePowers)
		persistBatteryQuarterHours(t, c, clk, chargePowers)

		site.collectors["seasoned"] = c
		charge, discharge := site.batteryPowerLimits("seasoned", testBatteryCapacity)

		assert.Equal(t, 12000.0, discharge, "discharge limit must reflect the demonstrated maximum, not an average")
		assert.Equal(t, 15000.0, charge, "charge limit must reflect the demonstrated maximum, not an average")
	})

	// a single corrupt meters row must not set CMax/DMax for the whole lookback window.
	// Nothing between the meters table and batteryPowerLimits bounds a sample's magnitude,
	// so this is the failure mode batteryMaxCRate exists for: an unscreened maximum would
	// hand the solver 120kW/150kW for a 10kWh battery that has never exceeded 8kW/9kW.
	// Note this fixture has the same shape as the "seasoned" case above - 19 equal slots
	// plus one higher - and only the magnitude of the odd slot differs, which is exactly
	// why the screen has to be physical rather than rank-based.
	t.Run("one implausible sample: the C-rate screen rejects it, plain max would not", func(t *testing.T) {
		clk := clock.NewMock()
		clk.Set(time.Now().Truncate(tariff.SlotDuration))

		c, err := metrics.NewCollector(metrics.Battery, "glitched", "", metrics.WithClock(clk))
		require.NoError(t, err)
		require.NoError(t, c.AddEnergy(nil, nil, 0, false)) // baseline, no persist yet

		// 19 consistent slots per direction plus one rollover-sized reading
		dischargePowers := append(slices.Repeat([]float64{8000}, 19), 120000) // counter rollover / double-reporting meter
		chargePowers := append(slices.Repeat([]float64{-9000}, 19), -150000)

		persistBatteryQuarterHours(t, c, clk, dischargePowers)
		persistBatteryQuarterHours(t, c, clk, chargePowers)

		site.collectors["glitched"] = c
		charge, discharge := site.batteryPowerLimits("glitched", testBatteryCapacity)

		// 30kWh/37.5kWh in one slot is 12C/15C for a 10kWh battery, so both are dropped and
		// the highest surviving slot - the 8kW/9kW the battery actually demonstrated - stands
		assert.Equal(t, 8000.0, discharge, "one implausible slot must not become the discharge limit")
		assert.Equal(t, 9000.0, charge, "one implausible slot must not become the charge limit")
	})

	// the screen needs a capacity to judge against; without one there is nothing to
	// compare a sample to, so the unscreened maximum is all that is left
	t.Run("unknown capacity: no screen, falls back to the plain maximum", func(t *testing.T) {
		charge, discharge := site.batteryPowerLimits("glitched", 0)

		assert.Equal(t, 120000.0, discharge)
		assert.Equal(t, 150000.0, charge)
	})

	t.Run("enough history but all below the fallback: keeps the default as a floor", func(t *testing.T) {
		clk := clock.NewMock()
		clk.Set(time.Now().Truncate(tariff.SlotDuration))

		c, err := metrics.NewCollector(metrics.Battery, "trickler", "", metrics.WithClock(clk))
		require.NoError(t, err)
		require.NoError(t, c.AddEnergy(nil, nil, 0, false)) // baseline, no persist yet

		// batteryPowerMinSamples slots per direction, every one well below the batteryPower
		// fallback - a battery that has only ever trickled must not get pinned below the
		// default just because that is all it has demonstrated so far
		persistBatteryQuarterHours(t, c, clk, slices.Repeat([]float64{1500}, 20))
		persistBatteryQuarterHours(t, c, clk, slices.Repeat([]float64{-2000}, 20))

		site.collectors["trickler"] = c
		charge, discharge := site.batteryPowerLimits("trickler", testBatteryCapacity)

		assert.Equal(t, float64(batteryPower), discharge, "must not be pinned below the default fallback")
		assert.Equal(t, float64(batteryPower), charge, "must not be pinned below the default fallback")
	})
}

// charge goal for vehicles with and without known capacity/soc, see #32890
func TestLoadpointRequestChargeGoal(t *testing.T) {
	site := &Site{log: util.NewLogger("foo")}

	for _, tc := range []struct {
		name                            string
		capacity, soc                   float64 // kWh, percent
		minSoc, limitSoc                int     // percent
		limitEnergy, charged            float64 // kWh, Wh
		wantInitial, wantSMin, wantSMax float32 // Wh
	}{
		{"soc limit", 50, 20, 0, 80, 0, 0, 10000, 0, 40000},
		{"no capacity, energy limit", 0, 0, 30, 100, 10, 0, 0, 0, 10000},
		{"no capacity, energy limit partially charged", 0, 0, 30, 100, 10, 4000, 4000, 0, 10000},
		{"no capacity, limit exceeded", 0, 0, 30, 100, 10, 11000, 11000, 0, 11000},
		{"capacity but no soc, energy limit", 50, 0, 30, 100, 10, 0, 0, 0, 10000},
		{"capacity but no soc, no energy limit", 50, 0, 30, 100, 0, 0, 0, 15000, 50000},
		// minSoc feeds the optimizer's floor so the plan reflects the same forced-charge
		// threshold the loadpoint enforces, instead of silently allowing the model to run
		// the vehicle down to empty.
		{"min soc below current soc", 50, 40, 30, 100, 0, 0, 20000, 15000, 50000},
		// minSoc above current soc: SMin is passed unclamped. s_min is a soft penalty in the
		// solver (optimizer.py s_min_pen, not a hard bound on s), so starting below the floor
		// stays feasible - the solver is pushed to charge back up to it instead of the
		// constraint going away, which is what clamping SMin to the current soc would do.
		{"min soc above current soc", 50, 20, 30, 100, 0, 0, 10000, 15000, 50000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)

			v := api.NewMockVehicle(ctrl)
			v.EXPECT().Capacity().Return(tc.capacity).AnyTimes()
			v.EXPECT().GetTitle().Return("").AnyTimes()

			lp := loadpoint.NewMockAPI(ctrl)
			lp.EXPECT().GetVehicle().Return(v).AnyTimes()
			lp.EXPECT().GetSoc().Return(tc.soc).AnyTimes()
			lp.EXPECT().EffectiveMinSoc().Return(tc.minSoc).AnyTimes()
			lp.EXPECT().EffectiveLimitSoc().Return(tc.limitSoc).AnyTimes()
			lp.EXPECT().GetLimitEnergy().Return(tc.limitEnergy).AnyTimes()
			lp.EXPECT().GetChargedEnergy().Return(tc.charged).AnyTimes()
			lp.EXPECT().GetTitle().Return("lp").AnyTimes()
			lp.EXPECT().EffectiveMinPower().Return(1380.0).AnyTimes()
			lp.EXPECT().EffectiveMaxPower().Return(11000.0).AnyTimes()
			lp.EXPECT().GetMode().Return(api.ModePV).AnyTimes()
			lp.EXPECT().GetStatus().Return(api.StatusB).AnyTimes()
			lp.EXPECT().GetSmartCostLimit().Return(nil).AnyTimes()
			lp.EXPECT().EffectivePlanStrategy().Return(api.PlanStrategy{}).AnyTimes()
			lp.EXPECT().GetPlanGoal().Return(0.0, false).AnyTimes()
			lp.EXPECT().EffectivePriority().Return(0).AnyTimes()

			req, _ := site.loadpointRequest(lp, 8, 15*time.Minute, nil, 0)

			assert.Equal(t, tc.wantInitial, req.SInitial)
			assert.Equal(t, tc.wantSMin, req.SMin)
			assert.Equal(t, tc.wantSMax, req.SMax)
		})
	}
}

// TestLoadpointRequestCPriorityNegativePriceHorizon is the integration counterpart to
// TestSafeCPriority: it verifies loadpointRequest actually wires minImportPrice into
// safeCPriority when building the request, not just that the helper itself is correct in
// isolation. A negative minImportPrice (one negatively-priced slot anywhere in the horizon is
// enough - see safeCPriority) must drop an explicitly raised loadpoint priority to 0 instead
// of letting it invert into a penalty.
func TestLoadpointRequestCPriorityNegativePriceHorizon(t *testing.T) {
	site := &Site{log: util.NewLogger("foo")}

	newMock := func(t *testing.T) loadpoint.API {
		ctrl := gomock.NewController(t)

		v := api.NewMockVehicle(ctrl)
		v.EXPECT().Capacity().Return(50.0).AnyTimes()
		v.EXPECT().GetTitle().Return("").AnyTimes()

		lp := loadpoint.NewMockAPI(ctrl)
		lp.EXPECT().GetVehicle().Return(v).AnyTimes()
		lp.EXPECT().GetSoc().Return(50.0).AnyTimes()
		lp.EXPECT().EffectiveMinSoc().Return(0).AnyTimes()
		lp.EXPECT().EffectiveLimitSoc().Return(100).AnyTimes()
		lp.EXPECT().GetLimitEnergy().Return(0.0).AnyTimes()
		lp.EXPECT().GetChargedEnergy().Return(0.0).AnyTimes()
		lp.EXPECT().GetTitle().Return("lp").AnyTimes()
		lp.EXPECT().EffectiveMinPower().Return(1380.0).AnyTimes()
		lp.EXPECT().EffectiveMaxPower().Return(11000.0).AnyTimes()
		lp.EXPECT().GetMode().Return(api.ModePV).AnyTimes()
		lp.EXPECT().GetStatus().Return(api.StatusB).AnyTimes()
		lp.EXPECT().GetSmartCostLimit().Return(nil).AnyTimes()
		lp.EXPECT().EffectivePlanStrategy().Return(api.PlanStrategy{}).AnyTimes()
		lp.EXPECT().GetPlanGoal().Return(0.0, false).AnyTimes()
		lp.EXPECT().EffectivePriority().Return(10).AnyTimes() // top third -> CPriority 2

		return lp
	}

	req, _ := site.loadpointRequest(newMock(t), 8, 15*time.Minute, nil, 0.0001) // positive horizon
	assert.Equal(t, 2, req.CPriority, "positive horizon: explicit priority passes through")

	req, _ = site.loadpointRequest(newMock(t), 8, 15*time.Minute, nil, -0.0001) // negative horizon
	assert.Equal(t, 0, req.CPriority, "negative horizon: would invert, dropped to 0")
}

func TestOptimizerChargingStrategy(t *testing.T) {
	site := &Site{log: util.NewLogger("foo")}

	// default when unset
	assert.Equal(t, defaultOptimizerChargingStrategy, site.GetOptimizerChargingStrategy())

	// invalid value rejected, strategy unchanged
	require.Error(t, site.SetOptimizerChargingStrategy("bogus"))
	assert.Equal(t, defaultOptimizerChargingStrategy, site.GetOptimizerChargingStrategy())

	// valid change is applied (re-trigger is gated on sponsor/enabled, not unit-tested here)
	require.NoError(t, site.SetOptimizerChargingStrategy(string(optimizer.OptimizerStrategyChargingStrategyAttenuateGridPeaks)))
	assert.Equal(t, "attenuate_grid_peaks", site.GetOptimizerChargingStrategy())
}

func TestGridExportLimit(t *testing.T) {
	site := &Site{log: util.NewLogger("foo")}

	// disabled by default
	assert.Equal(t, 0.0, site.GetGridExportLimit())

	// negative value rejected, limit unchanged
	require.Error(t, site.SetGridExportLimit(-1))
	assert.Equal(t, 0.0, site.GetGridExportLimit())

	require.NoError(t, site.SetGridExportLimit(7000))
	assert.Equal(t, 7000.0, site.GetGridExportLimit())
}

func TestEffectivePriorityToCPriority(t *testing.T) {
	// low third, including the common default of 0: no preference over other batteries
	for _, p := range []int{-5, 0, 1, 2, 3} {
		assert.Equal(t, 0, effectivePriorityToCPriority(p), "priority %d", p)
	}

	// middle third
	for _, p := range []int{4, 5, 6, 7} {
		assert.Equal(t, 1, effectivePriorityToCPriority(p), "priority %d", p)
	}

	// top third, including out-of-range values above the UI's 0..10 scale
	for _, p := range []int{8, 9, 10, 20} {
		assert.Equal(t, 2, effectivePriorityToCPriority(p), "priority %d", p)
	}
}

// TestSafeCPriority asserts the negative-price guard described on safeCPriority: the solver's
// CPriority preference term multiplies by min_import_price, so a negative minimum (one
// negatively-priced slot anywhere in the horizon is enough) would invert a positive priority
// into a penalty. Only a strictly negative minimum must trigger the guard - zero leaves the
// term at zero contribution either way, so there is nothing to invert.
func TestSafeCPriority(t *testing.T) {
	assert.Equal(t, 2, safeCPriority(2, 0.0001), "positive minimum: priority passes through")
	assert.Equal(t, 2, safeCPriority(2, 0), "zero minimum: nothing to invert, priority passes through")
	assert.Equal(t, 0, safeCPriority(2, -0.0001), "negative minimum: would invert, dropped to 0")
	assert.Equal(t, 0, safeCPriority(0, -0.0001), "already 0: stays 0")
}

// TestTerminalStorageValue pins the bounds terminalStorageValue is built on: it must never
// drop below zero (the dumping failure mode a negative-price horizon would otherwise
// cause), and - the property that actually matters for plan quality - it must stay low
// enough that discharging stored energy now is always at least as good as holding it for
// the terminal bonus. Get that inverted (as the divide-by-eta formula this replaces did)
// and the solver stops discharging altogether: proven on a flat 0.30 EUR/kWh tariff with
// the pinned solver, discharged energy went 4.0 kWh -> 0.0 kWh and grid import rose
// 8.0 kWh -> 12.0 kWh over a 12-slot horizon once pa exceeded eta*price everywhere.
func TestTerminalStorageValue(t *testing.T) {
	cases := []struct {
		name           string
		minImportPrice float32
	}{
		{"typical positive price", 0.0002},
		{"very small positive price", 1e-9},
		{"flat curve", 0.00015},
		{"zero", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pa := terminalStorageValue(tc.minImportPrice)

			assert.GreaterOrEqual(t, pa, float32(0), "never negative")

			if tc.minImportPrice > 0 {
				// discharge-over-hoarding bound: discharging one internal Wh right
				// now at the horizon's cheapest price realizes eta*price AC-side
				// currency (optimizer.py:617-631 - discharge multiplies by eta_d).
				// Holding it to the terminal instead realizes pa. Every slot in the
				// horizon prices at or above minImportPrice, so pinning this at the
				// minimum covers every slot: the solver must never prefer holding
				// over discharging anywhere, or a flat/near-flat tariff stalls
				// discharge for the whole horizon exactly as it did before this bound
				// held.
				assert.Less(t, pa, eta*tc.minImportPrice, "must stay below the discharge-over-hoarding bound")

				// anti-hoarding bound, unchanged by the direction of the formula:
				// charging one Wh in at the cheapest price costs price/eta on the
				// grid side (eta_c on the way in); banking pa instead of spending it
				// must never be pure profit.
				assert.Less(t, pa*eta, tc.minImportPrice, "must stay below the charge-to-hoard arbitrage breakeven")
			}
		})
	}

	// negative minimum: multiplying by eta (< 1) keeps the liability smaller in magnitude
	// than minImportPrice itself, but the floor still applies - a negative terminal value
	// would otherwise push a site configured with s_min = 0 to drain the battery to escape
	// the penalty, and the optimizer's own openapi contract declares p_a's minimum as 0.
	assert.Equal(t, float32(0), terminalStorageValue(-0.0002), "negative minimum: floored at zero, not a liability")
}

func TestBlendMeasured(t *testing.T) {
	slots := []float64{100, 100, 100, 100, 100, 100}
	blendMeasured(slots, 200, 4)
	assert.Equal(t, []float64{200, 175, 150, 125, 100, 100}, slots)

	// fewer slots than decay length
	short := []float32{100, 100}
	blendMeasured(short, 200, 4)
	assert.Equal(t, []float32{200, 175}, short)
}

// TestBlendScaleByLead: a flat ratio applied to every slot in the decay
// window mixes two different scales for every slot but the one whose lead happens to
// match the scale the ratio's denominator used. Fixture: 4 slots at leads 0, 15, 30,
// 45min, each forecast 2000Wh raw. scaleAt(0)=0.8 (the nowcast - matches what
// scaleAndPruneByLead used to build slot 0), scaleAt(lead>0)=0.6 (a different
// per-lead bias, entirely plausible in production since per-lead history can
// genuinely disagree with the nowcast). Measured last-slot PV 400Wh vs raw archived
// forecast 1000Wh.
func TestBlendScaleByLead(t *testing.T) {
	baseTime := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	leadSlots := api.Rates{
		{Start: baseTime},
		{Start: baseTime.Add(15 * time.Minute)},
		{Start: baseTime.Add(30 * time.Minute)},
		{Start: baseTime.Add(45 * time.Minute)},
	}
	scaleAt := func(lead time.Duration) float64 {
		if lead <= 0 {
			return 0.8
		}
		return 0.6
	}

	// ftSlots as scaleAndPruneByLead would have built them: rawForecast(2000) * scaleAt(lead)
	rawForecast := 2000.0
	newFtSlots := func() []float64 {
		return []float64{rawForecast * scaleAt(0), rawForecast * scaleAt(15*time.Minute), rawForecast * scaleAt(30*time.Minute), rawForecast * scaleAt(45*time.Minute)}
	}

	pv, fcstRaw := 400.0, 1000.0

	// OLD behaviour: a single flat ratio pv/(fcstRaw*0.8) = 0.5, computed with the
	// nowcast scale only, applied uniformly with the decay weight w = (4-i)/4 over
	// ftSlots [1600, 1200, 1200, 1200]:
	//   i=0: 1600*(1.00*0.5 + 0.00) =  800
	//   i=1: 1200*(0.75*0.5 + 0.25) =  750
	//   i=2: 1200*(0.50*0.5 + 0.50) =  900
	//   i=3: 1200*(0.25*0.5 + 0.75) = 1050
	oldSlots := []float64{800, 750, 900, 1050}

	// NEW behaviour: each slot's own lead selects the ratio's denominator scale.
	newSlots := newFtSlots()
	blendScaleByLead(newSlots, leadSlots, baseTime, func(lead time.Duration) float64 {
		return pv / (fcstRaw * scaleAt(lead))
	}, 4)

	// slot 0 is unaffected - its lead (0) matches the scale the old flat ratio used
	// too, so the scaleAt(0) term cancels identically either way.
	require.InDelta(t, 800, newSlots[0], 1e-9)
	require.InDelta(t, oldSlots[0], newSlots[0], 1e-9)

	// slots 1-3 diverge from the old, flat-ratio figures - this is the bug.
	require.InDelta(t, 900, newSlots[1], 1e-9)
	require.InDelta(t, 1000, newSlots[2], 1e-9)
	require.InDelta(t, 1100, newSlots[3], 1e-9)
	require.NotEqual(t, oldSlots[1], newSlots[1])
	require.NotEqual(t, oldSlots[2], newSlots[2])
	require.NotEqual(t, oldSlots[3], newSlots[3])

	// fewer slots than decay length, and fewer leadSlots than decay length
	short := []float64{100, 100}
	blendScaleByLead(short, leadSlots[:2], baseTime, func(time.Duration) float64 { return 2 }, 4)
	require.Equal(t, []float64{200, 175}, short)
}

// TestSurplusCharge covers the predicate shared by optimizerCharging (defer to the pv loop)
// and updatePower's #30541-style gate (add the priority adjustment back): "surplus charge"
// means an active charge suggestion the site can cover without extra grid import - not a
// forced full-power charge (always grid-fed by definition), and not a charge suggestion that
// itself plans to import.
func TestSurplusCharge(t *testing.T) {
	const maxPower = 11000.0 // W

	cases := []struct {
		name string
		s    *types.Suggestion
		want bool
	}{
		{"nil suggestion: optimizer stale/absent/unsponsored", nil, false},
		{"stop suggestion", &types.Suggestion{Action: actionStop}, false},
		{"discharge suggestion", &types.Suggestion{Action: actionDischarge}, false},
		{"full-power charge: grid-fed by definition, not surplus", &types.Suggestion{Action: actionCharge, Charge: maxPower}, false},
		{"just under full power, within threshold: still full", &types.Suggestion{Action: actionCharge, Charge: maxPower - suggestionThreshold + 1}, false},
		{"charge with planned grid import", &types.Suggestion{Action: actionCharge, Charge: 5000, Grid: 500}, false},
		{"charge with planned grid export beyond threshold", &types.Suggestion{Action: actionCharge, Charge: 5000, Grid: -500}, false},
		{"partial charge, flat grid flow: surplus", &types.Suggestion{Action: actionCharge, Charge: 5000, Grid: 0}, true},
		{"partial charge, grid flow within noise threshold: surplus", &types.Suggestion{Action: actionCharge, Charge: 5000, Grid: suggestionThreshold}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, surplusCharge(tc.s, maxPower))
		})
	}
}

func TestCurrentSlotSuggestion(t *testing.T) {
	// a ~19kWh home battery well between its SoC bounds
	midSocConfig := optimizer.BatteryConfig{SCapacity: 19320, SMin: 966, SMax: 18354, SInitial: 10000}

	// slotHours 1 makes the per-slot Wh values map 1:1 to W
	for _, tc := range []struct {
		name             string
		typ              batteryType
		charge, disch    float32
		gridImp, gridExp float32
		want             string
	}{
		{"battery grid charge", batteryTypeBattery, 3000, 0, 1000, 0, "charge"},
		{"battery pv charge (no import)", batteryTypeBattery, 3000, 0, 0, 1000, "normal"},
		{"battery hold (idle while importing)", batteryTypeBattery, 0, 0, 1000, 0, "hold"},
		{"battery holdcharge (idle while exporting)", batteryTypeBattery, 0, 0, 0, 1000, "holdcharge"},
		{"battery discharge (self-consumption while importing)", batteryTypeBattery, 0, 2000, 1000, 0, "normal"},
		{"battery grid discharge (discharge while exporting)", batteryTypeBattery, 0, 2000, 0, 1000, "discharge"},
		{"battery idle balanced", batteryTypeBattery, 0, 0, 0, 0, "normal"},
		{"loadpoint charge", batteryTypeLoadpoint, 11000, 0, 0, 0, "charge"},
		{"loadpoint stop", batteryTypeLoadpoint, 0, 0, 0, 0, "stop"},
		{"vehicle below threshold is stop", batteryTypeVehicle, 40, 0, 0, 0, "stop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := optimizer.BatteryResult{
				ChargingPower:    []float32{tc.charge},
				DischargingPower: []float32{tc.disch},
			}
			s := currentSlotSuggestion(batteryDetail{Type: tc.typ}, midSocConfig, res, tc.gridImp, tc.gridExp, 1)
			assert.Equal(t, tc.want, s.Action)
			assert.InDelta(t, tc.charge, s.Charge, 1e-3)
			assert.InDelta(t, tc.disch, s.Discharge, 1e-3)
			assert.InDelta(t, tc.gridImp-tc.gridExp, s.Grid, 1e-3)
		})
	}

	// no result yields an empty suggestion
	assert.Empty(t, currentSlotSuggestion(batteryDetail{Type: batteryTypeBattery}, midSocConfig, optimizer.BatteryResult{}, 1000, 0, 1))

	// a battery idling on a SoC bound is forced there, not making a choice:
	// at SMin it cannot discharge, at SMax it cannot charge — deriving
	// hold/holdcharge from a forced idle turns physics into phantom decisions
	idle := optimizer.BatteryResult{ChargingPower: []float32{0}, DischargingPower: []float32{0}}

	atMin := midSocConfig
	atMin.SInitial = atMin.SMin
	s := currentSlotSuggestion(batteryDetail{Type: batteryTypeBattery}, atMin, idle, 1000, 0, 1)
	assert.Equal(t, "normal", s.Action, "idle at SMin is forced, not hold")

	atMax := midSocConfig
	atMax.SInitial = atMax.SMax
	s = currentSlotSuggestion(batteryDetail{Type: batteryTypeBattery}, atMax, idle, 0, 1000, 1)
	assert.Equal(t, "normal", s.Action, "idle at SMax is forced, not holdcharge")

	// just outside the epsilon band the idle is a real decision again
	nearMin := midSocConfig
	nearMin.SInitial = nearMin.SMin + socBoundEpsilon(nearMin) + 1
	s = currentSlotSuggestion(batteryDetail{Type: batteryTypeBattery}, nearMin, idle, 1000, 0, 1)
	assert.Equal(t, "hold", s.Action, "idle above the SMin band is a chosen hold")
}

// TestSuggestionActionable ensures the actionable flag follows the current state
// instead of the state at optimizer run time
func TestSuggestionActionable(t *testing.T) {
	lp := NewLoadpoint(util.NewLogger("foo"), nil)

	site := &Site{
		batteryMode: api.BatteryNormal,
		loadpoints:  []*Loadpoint{lp},
	}
	site.setSuggestions(map[string]types.Suggestion{
		batteryKey("bat"): {Action: api.BatteryCharge.String()},
		loadpointKey(0):   {Action: actionCharge},
	})

	batterySuggestion := func(name string) *types.Suggestion {
		return site.suggestion(batteryKey(name), site.GetBatteryMode().String())
	}
	loadpointSuggestion := func(id int) *types.Suggestion {
		return site.suggestion(loadpointKey(id), loadpointCurrentAction(lp))
	}

	// battery mode differs from suggestion
	s := batterySuggestion("bat")
	require.NotNil(t, s)
	assert.True(t, s.Actionable)

	site.batteryMode = api.BatteryCharge
	assert.False(t, batterySuggestion("bat").Actionable)

	assert.Nil(t, batterySuggestion("unknown"))

	// loadpoint stopped, suggestion is to charge
	s = loadpointSuggestion(0)
	require.NotNil(t, s)
	assert.True(t, s.Actionable)

	// loadpoint charging matches the suggestion
	lp.enabled = true
	lp.status = api.StatusC
	assert.False(t, loadpointSuggestion(0).Actionable)

	assert.Nil(t, loadpointSuggestion(1))
}

func TestSuggestionEvent(t *testing.T) {
	id := 2

	// battery: no loadpoint id, carries name
	detail := batteryDetail{Type: batteryTypeBattery, Name: "home", Title: "Home"}
	assert.Equal(t, "battery:home", detail.key())

	ev := suggestionEvent(detail, types.Suggestion{Action: api.BatteryCharge.String()})
	assert.Nil(t, ev.Loadpoint)
	assert.Equal(t, evSuggestion, ev.Event)
	assert.Equal(t, api.BatteryCharge.String(), ev.Attributes["suggestionAction"])
	assert.Equal(t, "home", ev.Attributes["suggestionName"])
	assert.Equal(t, "Home", ev.Attributes["suggestionTitle"])

	// loadpoint: carries id, no name
	detail = batteryDetail{Type: batteryTypeVehicle, loadpoint: &id, Title: "Garage"}
	assert.Equal(t, "loadpoint:2", detail.key())

	ev = suggestionEvent(detail, types.Suggestion{Action: actionCharge})
	require.NotNil(t, ev.Loadpoint)
	assert.Equal(t, id, *ev.Loadpoint)
	assert.NotContains(t, ev.Attributes, "suggestionName")

	// vehicle without loadpoint can't act on a suggestion
	assert.Empty(t, batteryDetail{Type: batteryTypeVehicle}.key())
}

func TestDiffSuggestions(t *testing.T) {
	site := &Site{}

	pending := func(s types.Suggestion) map[string]pendingSuggestion {
		ev := suggestionEvent(batteryDetail{loadpoint: new(int)}, s)
		return map[string]pendingSuggestion{"loadpoint:0": {suggestion: s, event: ev}}
	}

	charge := types.Suggestion{Action: actionCharge, Actionable: true}
	stop := types.Suggestion{Action: actionStop, Actionable: true}
	notActionable := types.Suggestion{Action: actionCharge, Actionable: false}

	// first actionable suggestion fires
	assert.Len(t, site.diffSuggestions(pending(charge)), 1)

	// unchanged action does not fire again
	assert.Empty(t, site.diffSuggestions(pending(charge)))

	// changed action fires
	assert.Len(t, site.diffSuggestions(pending(stop)), 1)

	// non-actionable suggestion does not fire and clears tracking so the same
	// action re-notifies when it becomes actionable again
	assert.Empty(t, site.diffSuggestions(pending(notActionable)))
	assert.Len(t, site.diffSuggestions(pending(stop)), 1)

	// vanished device is pruned and re-notifies on return
	assert.Empty(t, site.diffSuggestions(map[string]pendingSuggestion{}))
	assert.Len(t, site.diffSuggestions(pending(stop)), 1)
}

func TestNewOptimizerDiagnosticsPublish(t *testing.T) {
	// overshoot is summed across the whole horizon, not just the current slot -
	// this is the diagnostic that makes a PMaxImp overshoot (priced per slot by the
	// solver regardless of PrcPExcImp) visible instead of vanishing unreported
	got := newOptimizerDiagnosticsPublish(optimizer.OptimizationResult{
		ObjectiveValue:      1.2345,
		GridImportOvershoot: []float32{0, 150, 50},
		GridExportOvershoot: []float32{0, 0, 25},
		LimitViolations: optimizer.LimitViolationResult{
			GridImportLimitExceeded: true,
		},
	})

	assert.InDelta(t, 1.2345, got.ObjectiveValue, 1e-4)
	assert.InDelta(t, 200, got.GridImportOvershoot, 1e-6)
	assert.InDelta(t, 25, got.GridExportOvershoot, 1e-6)
	assert.True(t, got.LimitViolations.GridImportLimitExceeded)
	assert.False(t, got.LimitViolations.GridExportLimitHit)

	// no overshoot at all: zero values throughout, not omitted
	empty := newOptimizerDiagnosticsPublish(optimizer.OptimizationResult{})
	assert.Zero(t, empty.ObjectiveValue)
	assert.Zero(t, empty.GridImportOvershoot)
	assert.Zero(t, empty.GridExportOvershoot)
	assert.False(t, empty.LimitViolations.GridImportLimitExceeded)
	assert.False(t, empty.LimitViolations.GridExportLimitHit)
}

// TestApplyOptimizerResultEmptyMeansActuallyEmpty guards the evopt-batteries "empty"
// diagnostic: it must mean the battery is actually drained (soc reaches 0), not merely at
// its configured SMin floor. SMin is a soft target the solver can plan below (see
// loadpointRequest, 587a3bdc6) - a vehicle sitting under its minSoc while plugged in and not
// yet charged back up is not "empty", and reporting it as such is misleading regardless of
// SMin being a real, nonzero floor (as configured here).
func TestApplyOptimizerResultEmptyMeansActuallyEmpty(t *testing.T) {
	params := make(chan util.Param, 64)
	site := &Site{log: util.NewLogger("foo"), valueChan: params}

	req := optimizer.OptimizationInput{
		TimeSeries: optimizer.TimeSeries{Dt: []int{900}},
		Batteries:  []optimizer.BatteryConfig{{SMin: 15000, SMax: 50000}},
	}
	res := optimizer.OptimizationResult{
		// stays under SMin (15000) throughout but never actually reaches 0
		Batteries: []optimizer.BatteryResult{{StateOfCharge: []float32{10000, 5000, 2000}}},
	}
	details := []batteryDetail{{}}

	drainBatteries := func() []batteryResult {
		for {
			select {
			case p := <-params:
				if p.Key == "evopt-batteries" {
					return p.Val.([]batteryResult)
				}
			default:
				return nil
			}
		}
	}

	site.applyOptimizerResult(req, details, res)

	got := drainBatteries()
	require.Len(t, got, 1)
	assert.True(t, got[0].Empty.IsZero(), "must not report empty just for being under SMin")

	// now genuinely empty from the first slot: below SMin AND at 0
	res.Batteries[0].StateOfCharge = []float32{0, 0, 0}
	site.applyOptimizerResult(req, details, res)

	got = drainBatteries()
	require.Len(t, got, 1)
	assert.False(t, got[0].Empty.IsZero(), "a soc of 0 must be reported empty")
}

// TestPersistControlSlotGate exercises the control_slots slot gate:
// the same partial-boot-slot skip and repeat-tick dedup as persistTariffs.
// Advancing "last persisted" backwards in place of sleeping avoids waiting on
// wall-clock time to cross a real 15min boundary.
func TestPersistControlSlotGate(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())

	site := &Site{log: util.NewLogger("foo")}
	site.batteryMode = api.BatteryCharge
	site.optimizerBatteryMode = api.BatteryCharge
	site.optimizerSuggestedMode = api.BatteryCharge
	site.optimizerChargePrice = 0.15
	site.optimizerHealthOk = true

	countRows := func() int64 {
		var n int64
		require.NoError(t, db.Instance.Table("control_slots").Count(&n).Error)
		return n
	}

	// the first call after boot only establishes the current slot - a
	// partial slot the process didn't observe from the start is not
	// persisted, mirroring persistTariffs
	site.persistControlSlot()
	assert.Equal(t, int64(0), countRows())

	// repeat ticks within the same slot (automatic mode calls this every
	// control-loop cycle, ~30s) must not write again
	site.persistControlSlot()
	assert.Equal(t, int64(0), countRows())

	// simulate having crossed into a new slot: rewind the tracked
	// "last persisted" slot by one slot duration instead of sleeping
	site.controlSlot = site.controlSlot.Add(-tariff.SlotDuration)
	site.persistControlSlot()
	assert.Equal(t, int64(1), countRows())

	// and the gate closes again immediately afterwards
	site.persistControlSlot()
	assert.Equal(t, int64(1), countRows())

	var appliedMode, suggestedMode string
	var price *float64
	require.NoError(t, db.Instance.Raw(
		"SELECT applied_mode, suggested_mode, price FROM control_slots",
	).Row().Scan(&appliedMode, &suggestedMode, &price))
	assert.Equal(t, api.BatteryCharge.String(), appliedMode)
	assert.Equal(t, api.BatteryCharge.String(), suggestedMode)
	require.NotNil(t, price)
	assert.InDelta(t, 0.15, *price, 0.001)
}

// TestPersistControlSlotPriceAbsentWhenNotCharging asserts the price column
// is absent (not 0) whenever the suggested mode isn't an active charge
// decision - a slot idling at 0 price must not be indistinguishable from a
// slot with no price recorded at all.
func TestPersistControlSlotPriceAbsentWhenNotCharging(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())

	site := &Site{log: util.NewLogger("foo")}
	site.batteryMode = api.BatteryNormal
	site.optimizerBatteryMode = api.BatteryNormal
	site.optimizerChargePrice = 0 // stale/zeroed, as setOptimizerBatteryMode leaves it for non-charge modes

	site.persistControlSlot()
	site.controlSlot = site.controlSlot.Add(-tariff.SlotDuration)
	site.persistControlSlot()

	var price *float64
	require.NoError(t, db.Instance.Raw("SELECT price FROM control_slots").Row().Scan(&price))
	assert.Nil(t, price)
}

// TestPersistControlSlotPaybackVetoPreservesSuggestion is the end-to-end
// counterpart to TestBatteryModeCandidate's payback-veto cases: it exercises
// the real path (setOptimizerBatteryMode -> persistControlSlot) a payback
// veto takes in production, confirming the tuple
// (applied_mode="normal", suggested_mode="charge", veto_reason="payback")
// is actually reachable - and that its price stays absent, since the
// suggestion was never accepted.
func TestPersistControlSlotPaybackVetoPreservesSuggestion(t *testing.T) {
	// enableAutomatic must run after db.NewInstance: server/db/settings'
	// migration reloads the in-memory settings cache from the (fresh, empty)
	// database and would otherwise wipe out the flags it just set
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())
	enableAutomatic(t)

	site := &Site{log: util.NewLogger("foo")}
	site.batteryMode = api.BatteryNormal

	site.setOptimizerBatteryMode(optimizerDecision{
		suggestedMode: api.BatteryCharge,
		chargeVetoed:  true,
		vetoReason:    vetoReasonPayback,
	})

	site.controlSlot = site.controlSlot.Add(-tariff.SlotDuration)
	site.persistControlSlot()

	var appliedMode, suggestedMode, vetoReason string
	var price *float64
	require.NoError(t, db.Instance.Raw(
		"SELECT applied_mode, suggested_mode, veto_reason, price FROM control_slots",
	).Row().Scan(&appliedMode, &suggestedMode, &vetoReason, &price))

	assert.Equal(t, api.BatteryNormal.String(), appliedMode, "nothing was applied")
	assert.Equal(t, api.BatteryCharge.String(), suggestedMode, "the rejected suggestion is still visible")
	assert.Equal(t, string(vetoReasonPayback), vetoReason)
	assert.Nil(t, price, "an unaccepted suggestion carries no price")
}

// TestPersistControlSlotAdvisoryModeRecordsSuggestion is the advisory-mode
// counterpart to TestPersistControlSlotPaybackVetoPreservesSuggestion:
// optimizerAutomatic is left off, so nothing is ever applied - but the
// vetted suggestion the optimizer derived this run is real and worth
// recording anyway. The fixture has no battery meter configured at all, which
// is the only case that still records applied_mode "unknown" (see
// appliedBatteryMode and TestPersistControlSlotNoOverrideRecordsNormal for
// the far more common advisory-with-a-battery case). Collecting that comparison
// before automatic mode is ever switched on is the whole point of the
// ledger: it lets a later decision to enable automatic mode be based on data
// gathered while advisory, at zero control risk.
func TestPersistControlSlotAdvisoryModeRecordsSuggestion(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())

	site := &Site{log: util.NewLogger("foo")}
	// optimizerAutomatic is left false (the settings cache reloads empty from
	// the fresh db above) - this is advisory mode
	require.False(t, site.Automatic())
	require.False(t, site.batteryConfigured(), "no battery: the one case that still records unknown")

	site.setOptimizerBatteryMode(optimizerDecision{
		mode:          api.BatteryCharge,
		suggestedMode: api.BatteryCharge,
		price:         0.12,
	})
	require.Equal(t, api.BatteryUnknown, site.optimizerBatteryMode, "advisory mode never sets an applyable mode")

	site.controlSlot = site.controlSlot.Add(-tariff.SlotDuration)
	site.persistControlSlot()

	var appliedMode, suggestedMode string
	var price *float64
	require.NoError(t, db.Instance.Raw(
		"SELECT applied_mode, suggested_mode, price FROM control_slots",
	).Row().Scan(&appliedMode, &suggestedMode, &price))

	assert.Equal(t, api.BatteryUnknown.String(), appliedMode, "advisory mode never applies anything")
	assert.Equal(t, api.BatteryCharge.String(), suggestedMode, "the vetted suggestion is recorded despite not being applied")
	require.NotNil(t, price, "an accepted charge suggestion carries its price even though advisory mode never spent it")
	assert.InDelta(t, 0.12, *price, 0.001)
}

// TestPersistControlSlotNoOverrideRecordsNormal: without this, every row on a site
// that never needs an override records applied_mode "unknown", which renders as
// "APPLIED unknown / SUGGESTED INSTEAD Normal operation".
// site.batteryMode is only ever written by
// SetBatteryMode, which updateBatteryMode calls only when requiredBatteryMode
// returns something other than api.BatteryUnknown, so a site that never needs
// an override (advisory mode, no grid-charge limit, no smart-cost limit)
// leaves it at its zero value for the whole process lifetime. "unknown" is
// the control loop's word for "no change required", not for "unobserved" -
// with a battery present it must be recorded as the normal operation it is.
func TestPersistControlSlotNoOverrideRecordsNormal(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())

	site := &Site{
		log:           util.NewLogger("foo"),
		batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{}, api.Meter(nil))},
	}
	site.optimizerSuggestedMode = api.BatteryNormal
	require.Equal(t, api.BatteryUnknown, site.GetBatteryMode(), "nothing has ever set a mode")

	site.persistControlSlot()
	site.controlSlot = site.controlSlot.Add(-tariff.SlotDuration)
	site.persistControlSlot()

	var appliedMode, suggestedMode string
	var modeChanged bool
	require.NoError(t, db.Instance.Raw(
		"SELECT applied_mode, suggested_mode, mode_changed FROM control_slots",
	).Row().Scan(&appliedMode, &suggestedMode, &modeChanged))

	assert.Equal(t, api.BatteryNormal.String(), appliedMode, "no override in effect is normal operation, not an unobserved mode")
	assert.Equal(t, api.BatteryNormal.String(), suggestedMode)
	assert.False(t, modeChanged)

	// applied and suggested must agree, so DecisionDeltas has nothing to price -
	// differing as strings ("unknown" vs "normal") while simulating identically
	// fabricates "the veto was worth EUR 0.00" on a slot where nothing was vetoed
	assert.Equal(t, appliedMode, suggestedMode)
}

// TestPersistControlSlotModeChangeIgnoresOverrideRelease asserts mode_changed
// tracks the recorded mode, not the raw enum: releasing an override
// (api.BatteryUnknown, "no change required") on a site already running normal
// must not flag a change the recorded row cannot show - a site nothing ever overrides
// records no changes at all, while a real hold/charge arriving mid-slot still flags it.
func TestPersistControlSlotModeChangeIgnoresOverrideRelease(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())

	site := &Site{
		log:           util.NewLogger("foo"),
		batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{}, api.Meter(nil))},
	}

	site.persistControlSlot()
	site.controlSlot = site.controlSlot.Add(-tariff.SlotDuration)
	site.persistControlSlot()

	modeChanged := func() bool {
		var v bool
		require.NoError(t, db.Instance.Raw("SELECT mode_changed FROM control_slots").Row().Scan(&v))
		return v
	}

	// unknown -> normal is the control loop releasing an override; the
	// battery's behaviour, and the recorded mode, are unchanged
	site.batteryMode = api.BatteryNormal
	site.persistControlSlot()
	assert.False(t, modeChanged(), "releasing an override is not a mode change")

	// an actual override arriving mid-slot is
	site.batteryMode = api.BatteryHold
	site.persistControlSlot()
	assert.True(t, modeChanged())
}

// TestPersistOptimizerRunGate exercises the optimizer_runs slot gate.
// Only the sampled Optimal/Feasible path is deduped to one row per slot, the
// same partial-boot-slot skip and repeat-tick dedup as persistTariffs -
// that's the happy path, and one representative sample per slot is enough.
// Every other status bypasses the gate and is always recorded: a solver
// going Infeasible after an already-sampled Optimal run in the same slot
// must still leave a row, or the exact failure this table exists to catch
// (a solver going bad for minutes at a time) would vanish whenever the
// slot's first run happened to succeed.
func TestPersistOptimizerRunGate(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())

	site := &Site{log: util.NewLogger("foo")}

	res := optimizer.OptimizationResult{
		ObjectiveValue:      2.5,
		GridImportOvershoot: []float32{10, 20},
	}

	countRows := func() int64 {
		var n int64
		require.NoError(t, db.Instance.Table("optimizer_runs").Count(&n).Error)
		return n
	}

	site.persistOptimizerRun("Optimal", res)
	assert.Equal(t, int64(0), countRows())

	site.persistOptimizerRun("Optimal", res)
	assert.Equal(t, int64(0), countRows())

	site.optimizerRunSlot = site.optimizerRunSlot.Add(-tariff.SlotDuration)
	site.persistOptimizerRun("Optimal", res)
	assert.Equal(t, int64(1), countRows(), "the slot's first Optimal run is sampled")

	// a later Optimal run in the same slot is still deduped - the happy path
	// stays at one row per slot
	site.persistOptimizerRun("Optimal", res)
	assert.Equal(t, int64(1), countRows(), "a later Optimal run in the same slot is not a fresh sample")

	// an Infeasible run right after: not dropped as a "duplicate" of the
	// slot - this is the failure mode itself. A second, real-timestamped
	// Infeasible run landing its own row (rather than colliding with the
	// first) is covered at the mechanical layer by
	// TestPersistOptimizerRunDistinctTimestamps, where the calls are given
	// deliberate timestamp separation; two calls made back to back here would
	// land in the same wall-clock second and legitimately collide (see
	// TestPersistOptimizerRunNonSampledCollisionUpdates).
	site.persistOptimizerRun("Infeasible", res)
	assert.Equal(t, int64(2), countRows(), "a non-Optimal run always gets its own row")

	var rows []struct {
		Status         string
		ObjectiveValue *float64
	}
	require.NoError(t, db.Instance.Raw(
		"SELECT status, objective_value FROM optimizer_runs ORDER BY ts",
	).Scan(&rows).Error)
	require.Len(t, rows, 2)

	assert.Equal(t, "Optimal", rows[0].Status)
	require.NotNil(t, rows[0].ObjectiveValue)
	assert.InDelta(t, 2.5, *rows[0].ObjectiveValue, 0.001)

	for _, r := range rows[1:] {
		assert.Equal(t, "Infeasible", r.Status)
		assert.Nil(t, r.ObjectiveValue)
	}
}

// TestPersistOptimizerRunInfeasibleHasNoDiagnostics asserts that a run which
// didn't produce a schedule persists no numeric diagnostics at all, rather
// than the wire format's zero-valued ObjectiveValue/overshoot fields being
// mistaken for a real "zero overshoot" result.
func TestPersistOptimizerRunInfeasibleHasNoDiagnostics(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())

	site := &Site{log: util.NewLogger("foo")}

	// zero-valued result, as the client would decode for an Infeasible run
	site.persistOptimizerRun("Infeasible", optimizer.OptimizationResult{})
	site.optimizerRunSlot = site.optimizerRunSlot.Add(-tariff.SlotDuration)
	site.persistOptimizerRun("Infeasible", optimizer.OptimizationResult{})

	var objective, importOvershoot *float64
	require.NoError(t, db.Instance.Raw(
		"SELECT objective_value, grid_import_overshoot FROM optimizer_runs",
	).Row().Scan(&objective, &importOvershoot))
	assert.Nil(t, objective)
	assert.Nil(t, importOvershoot)
}
