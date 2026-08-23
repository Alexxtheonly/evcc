package core

import (
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/session"
	"github.com/evcc-io/evcc/core/types"
	"github.com/evcc-io/evcc/core/vehicle"
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

		req, _ := site.batteryRequest(dev, m, nil, 8, 15*time.Minute, 0)

		assert.Equal(t, float32(1500), req.SMin)
		assert.Equal(t, float32(10000), req.SMax)
		assert.LessOrEqual(t, req.SMin, req.SInitial)
	})

	t.Run("soc above maxSoc", func(t *testing.T) {
		soc := 95.0
		dev := newBatteryDevice(t, 0, 80)
		m := types.Measurement{Capacity: &capacity, Soc: &soc}

		req, _ := site.batteryRequest(dev, m, nil, 8, 15*time.Minute, 0)

		assert.Equal(t, float32(0), req.SMin)
		assert.Equal(t, float32(9500), req.SMax)
		assert.GreaterOrEqual(t, req.SMax, req.SInitial)
	})

	t.Run("soc within limits", func(t *testing.T) {
		soc := 50.0
		dev := newBatteryDevice(t, 20, 80)
		m := types.Measurement{Capacity: &capacity, Soc: &soc}

		req, _ := site.batteryRequest(dev, m, nil, 8, 15*time.Minute, 0)

		assert.Equal(t, float32(2000), req.SMin)
		assert.Equal(t, float32(8000), req.SMax)
	})

	t.Run("empty maxSoc defaults to 100%", func(t *testing.T) {
		soc := 50.0
		dev := newBatteryDevice(t, 20, 0)
		m := types.Measurement{Capacity: &capacity, Soc: &soc}

		req, _ := site.batteryRequest(dev, m, nil, 8, 15*time.Minute, 0)

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

// TestBatteryPowerLimits verifies the fallback and observed-maximum paths of
// batteryPowerLimits end to end (Site -> metrics.Collector -> sqlite -> W). History must only
// ever raise the batteryPower fallback, never lower it - see the function's doc comment.
func TestBatteryPowerLimits(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())

	site := &Site{log: util.NewLogger("foo"), collectors: map[string]*metrics.Collector{}}

	t.Run("no collector for this device: falls back", func(t *testing.T) {
		charge, discharge := site.batteryPowerLimits("unknown")
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
		charge, discharge := site.batteryPowerLimits("sparse")
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
		// demonstrated maximum, not an average of the whole history, becomes the limit
		dischargePowers := append([]float64{}, repeat(19, 4000.0)...)
		dischargePowers = append(dischargePowers, 12000) // demonstrated sustained capability
		chargePowers := append([]float64{}, repeat(19, -5000.0)...)
		chargePowers = append(chargePowers, -15000)

		persistBatteryQuarterHours(t, c, clk, dischargePowers)
		persistBatteryQuarterHours(t, c, clk, chargePowers)

		site.collectors["seasoned"] = c
		charge, discharge := site.batteryPowerLimits("seasoned")

		assert.Equal(t, 12000.0, discharge, "discharge limit must reflect the demonstrated maximum, not an average")
		assert.Equal(t, 15000.0, charge, "charge limit must reflect the demonstrated maximum, not an average")
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
		persistBatteryQuarterHours(t, c, clk, repeat(20, 1500.0))
		persistBatteryQuarterHours(t, c, clk, repeat(20, -2000.0))

		site.collectors["trickler"] = c
		charge, discharge := site.batteryPowerLimits("trickler")

		assert.Equal(t, float64(batteryPower), discharge, "must not be pinned below the default fallback")
		assert.Equal(t, float64(batteryPower), charge, "must not be pinned below the default fallback")
	})
}

func repeat(n int, v float64) []float64 {
	res := make([]float64, n)
	for i := range res {
		res[i] = v
	}
	return res
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
			lp.EXPECT().GetSmartCostLimitPercentile().Return(nil).AnyTimes()
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
		lp.EXPECT().GetSmartCostLimitPercentile().Return(nil).AnyTimes()
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

	// the home battery's own CPriority is 0 (see homeBatteryCPriority) - any explicitly
	// raised loadpoint priority outranks it, a default-priority one ties rather than losing
	assert.Equal(t, 0, homeBatteryCPriority)
	assert.GreaterOrEqual(t, effectivePriorityToCPriority(0), homeBatteryCPriority)
	assert.Greater(t, effectivePriorityToCPriority(10), homeBatteryCPriority)
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

func TestNextOccurrence(t *testing.T) {
	now := time.Date(2026, 8, 23, 14, 0, 0, 0, time.UTC)

	// later today
	assert.Equal(t, time.Date(2026, 8, 23, 18, 30, 0, 0, time.UTC), nextOccurrence(18*60+30, now))

	// already passed today without showing up: pinned to now, not deferred a full day -
	// this is the case right after the predicted (early-quantile) time, when arrival is
	// most likely, so the reservation must stay live rather than vanish until tomorrow
	assert.Equal(t, now, nextOccurrence(6*60, now))

	// exactly now: today, not pushed out a day
	assert.Equal(t, now, nextOccurrence(14*60, now))

	// a new calendar day naturally produces a fresh, still-future occurrence again
	tomorrow := time.Date(2026, 8, 24, 0, 5, 0, 0, time.UTC)
	assert.Equal(t, time.Date(2026, 8, 24, 6, 0, 0, 0, time.UTC), nextOccurrence(6*60, tomorrow))
}

func TestArrivalSlot(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	timestamps := []time.Time{now, now.Add(15 * time.Minute), now.Add(30 * time.Minute), now.Add(45 * time.Minute)}
	horizonEnd := now.Add(time.Hour)

	assert.Equal(t, 0, arrivalSlot(timestamps, horizonEnd, now.Add(5*time.Minute)))
	assert.Equal(t, 2, arrivalSlot(timestamps, horizonEnd, now.Add(35*time.Minute)))
	assert.Equal(t, 3, arrivalSlot(timestamps, horizonEnd, now.Add(59*time.Minute)), "last slot covers up to horizon end")
	assert.Equal(t, -1, arrivalSlot(timestamps, horizonEnd, now.Add(2*time.Hour)), "beyond horizon: not modelled")
}

// TestExpectedArrivalDemand exercises the gates expectedArrivalDemand must enforce: opted
// out, connected elsewhere, stale or absent prediction, and no capacity to convert soc
// into Wh must all leave Gt untouched, while a vehicle that clears every gate contributes
// its predicted energy at its predicted slot and nowhere else.
func TestExpectedArrivalDemand(t *testing.T) {
	site := &Site{log: util.NewLogger("foo")}
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	timestamps := []time.Time{now, now.Add(15 * time.Minute), now.Add(30 * time.Minute), now.Add(45 * time.Minute)}
	horizonEnd := now.Add(time.Hour)
	arrival := session.ExpectedArrival{TimeOfDay: 12*60 + 20, SocUsed: 30} // 12:20 today -> slot 1

	newVehicleMock := func(t *testing.T, capacity float64) *api.MockVehicle {
		ctrl := gomock.NewController(t)
		mv := api.NewMockVehicle(ctrl)
		mv.EXPECT().Capacity().Return(capacity).AnyTimes()
		return mv
	}

	t.Run("opted out", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		v := vehicle.NewMockAPI(ctrl)
		v.EXPECT().GetExpectedArrivalLearning().Return(false)

		got := site.expectedArrivalDemand([]vehicle.API{v}, 4, now, timestamps, horizonEnd, nil, false, 0)
		assert.Nil(t, got)
	})

	t.Run("connected elsewhere", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mv := newVehicleMock(t, 50)
		v := vehicle.NewMockAPI(ctrl)
		v.EXPECT().GetExpectedArrivalLearning().Return(true)
		v.EXPECT().Instance().Return(mv).AnyTimes()

		got := site.expectedArrivalDemand([]vehicle.API{v}, 4, now, timestamps, horizonEnd, map[api.Vehicle]bool{mv: true}, false, 0)
		assert.Nil(t, got, "already connected: never modelled twice")
	})

	t.Run("an unidentified connected vehicle suppresses every prediction", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		v := vehicle.NewMockAPI(ctrl) // no expectations: must not be called at all

		got := site.expectedArrivalDemand([]vehicle.API{v}, 4, now, timestamps, horizonEnd, nil, true, 0)
		assert.Nil(t, got, "unidentified vehicle physically connected: could be any configured vehicle, so none are predicted")
	})

	t.Run("no confident prediction", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mv := newVehicleMock(t, 50)
		v := vehicle.NewMockAPI(ctrl)
		v.EXPECT().GetExpectedArrivalLearning().Return(true)
		v.EXPECT().Instance().Return(mv).AnyTimes()
		v.EXPECT().GetExpectedArrival().Return(session.ExpectedArrival{}, time.Time{})

		got := site.expectedArrivalDemand([]vehicle.API{v}, 4, now, timestamps, horizonEnd, nil, false, 0)
		assert.Nil(t, got, "absent vehicle with no usable history changes nothing")
	})

	t.Run("stale prediction", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mv := newVehicleMock(t, 50)
		v := vehicle.NewMockAPI(ctrl)
		v.EXPECT().GetExpectedArrivalLearning().Return(true)
		v.EXPECT().Instance().Return(mv).AnyTimes()
		v.EXPECT().GetExpectedArrival().Return(arrival, now.Add(-vehicle.AdaptivePlansValidity-time.Hour))

		got := site.expectedArrivalDemand([]vehicle.API{v}, 4, now, timestamps, horizonEnd, nil, false, 0)
		assert.Nil(t, got, "stale prediction is not trusted")
	})

	t.Run("no capacity to convert soc into energy", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mv := newVehicleMock(t, 0)
		v := vehicle.NewMockAPI(ctrl)
		v.EXPECT().GetExpectedArrivalLearning().Return(true)
		v.EXPECT().Instance().Return(mv).AnyTimes()
		v.EXPECT().GetExpectedArrival().Return(arrival, now)

		got := site.expectedArrivalDemand([]vehicle.API{v}, 4, now, timestamps, horizonEnd, nil, false, 0)
		assert.Nil(t, got)
	})

	t.Run("confident prediction lands in a single slot when it fits under the clamp", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mv := newVehicleMock(t, 50) // 50 kWh
		v := vehicle.NewMockAPI(ctrl)
		v.EXPECT().Name().Return("car").AnyTimes()
		v.EXPECT().GetExpectedArrivalLearning().Return(true)
		v.EXPECT().Instance().Return(mv).AnyTimes()
		v.EXPECT().GetExpectedArrival().Return(arrival, now)

		// 30% of 50kWh = 15kWh = 15000Wh; a 100kW clamp covers that in one 15min slot (25000Wh)
		got := site.expectedArrivalDemand([]vehicle.API{v}, 4, now, timestamps, horizonEnd, nil, false, 100000)
		require.Len(t, got, 4)
		assert.Equal(t, []float32{0, 15000, 0, 0}, got, "at slot 1 (12:20 falls in [12:15,12:30))")
	})

	t.Run("prediction exceeding the per-slot clamp spreads into later slots", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mv := newVehicleMock(t, 50) // 50 kWh
		v := vehicle.NewMockAPI(ctrl)
		v.EXPECT().Name().Return("car").AnyTimes()
		v.EXPECT().GetExpectedArrivalLearning().Return(true)
		v.EXPECT().Instance().Return(mv).AnyTimes()
		v.EXPECT().GetExpectedArrival().Return(arrival, now)

		// 30% of 50kWh = 15000Wh; a 20kW clamp caps each 15min slot at 5000Wh, so a single
		// slot (180kW-equivalent, the real regression: 45kWh in one 15min slot implies
		// 180kW) can never happen - the energy must spread across three slots instead.
		got := site.expectedArrivalDemand([]vehicle.API{v}, 4, now, timestamps, horizonEnd, nil, false, 20000)
		require.Len(t, got, 4)
		assert.Equal(t, []float32{0, 5000, 5000, 5000}, got)
	})

	t.Run("energy left over once the horizon ends is dropped, not wrapped or errored", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mv := newVehicleMock(t, 50) // 50 kWh
		v := vehicle.NewMockAPI(ctrl)
		v.EXPECT().Name().Return("car").AnyTimes()
		v.EXPECT().GetExpectedArrivalLearning().Return(true)
		v.EXPECT().Instance().Return(mv).AnyTimes()
		v.EXPECT().GetExpectedArrival().Return(session.ExpectedArrival{TimeOfDay: 12*60 + 20, SocUsed: 90}, now)

		// 90% of 50kWh = 45000Wh; a 20kW clamp only fits 5000Wh/slot across the 3 slots
		// from the arrival slot to the horizon end (15000Wh total) - the remaining 30000Wh
		// has nowhere in this request to go and is silently dropped.
		got := site.expectedArrivalDemand([]vehicle.API{v}, 4, now, timestamps, horizonEnd, nil, false, 20000)
		require.Len(t, got, 4)
		assert.Equal(t, []float32{0, 5000, 5000, 5000}, got)
	})

	t.Run("prediction beyond the horizon is not modelled", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mv := newVehicleMock(t, 50)
		v := vehicle.NewMockAPI(ctrl)
		v.EXPECT().GetExpectedArrivalLearning().Return(true)
		v.EXPECT().Instance().Return(mv).AnyTimes()
		v.EXPECT().GetExpectedArrival().Return(session.ExpectedArrival{TimeOfDay: 23 * 60, SocUsed: 30}, now)

		got := site.expectedArrivalDemand([]vehicle.API{v}, 4, now, timestamps, horizonEnd, nil, false, 0)
		assert.Nil(t, got, "arrival predicted well past this request's short horizon")
	})
}

func TestSpreadDemand(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	timestamps := []time.Time{now, now.Add(15 * time.Minute), now.Add(30 * time.Minute), now.Add(45 * time.Minute)}
	horizonEnd := now.Add(time.Hour)

	// 20kW clamp over 15min slots (0.25h) caps each slot at 5000Wh
	demand := make([]float32, 4)
	placed := spreadDemand(demand, 1, 12000, timestamps, horizonEnd, 20000)
	assert.Equal(t, float32(12000), placed, "5000 in slot 1, 5000 in slot 2, remaining 2000 in slot 3")
	assert.Equal(t, []float32{0, 5000, 5000, 2000}, demand)

	// starting at the last slot, only that slot's capacity is available before horizonEnd
	demand2 := make([]float32, 4)
	placed2 := spreadDemand(demand2, 3, 12000, timestamps, horizonEnd, 20000)
	assert.Equal(t, float32(5000), placed2)
	assert.Equal(t, []float32{0, 0, 0, 5000}, demand2)

	// energy under the clamp lands entirely in the starting slot
	demand3 := make([]float32, 4)
	placed3 := spreadDemand(demand3, 0, 3000, timestamps, horizonEnd, 20000)
	assert.Equal(t, float32(3000), placed3)
	assert.Equal(t, []float32{3000, 0, 0, 0}, demand3)
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

func TestBlendScale(t *testing.T) {
	slots := []float32{100, 100, 100, 100, 100, 100}
	blendScale(slots, 2, 4)
	assert.Equal(t, []float32{200, 175, 150, 125, 100, 100}, slots)

	// fewer slots than decay length
	short := []float64{100, 100}
	blendScale(short, 0.5, 4)
	assert.Equal(t, []float64{50, 62.5}, short)
}

func TestCurrentSlotSuggestion(t *testing.T) {
	// BYD-sized battery well between its SoC bounds
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
