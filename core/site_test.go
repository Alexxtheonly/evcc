package core

import (
	"errors"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/core/types"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// TestSitePowerPriorityAdjustment verifies that sitePower returns the adjustment
// applied for battery priority below prioritySoc, such that adding it back yields
// the unadjusted site power for loadpoints with battery boost active (#30541)
func TestSitePowerPriorityAdjustment(t *testing.T) {
	const prioritySoc = 50

	for _, tc := range []struct {
		name                        string
		soc, power, excessDC        float64 // battery
		expSitePower, expAdjustment float64
		expReconstructed            float64 // sitePower + adjustment: the unadjusted site power a boost loadpoint sees
	}{
		// battery priority does not apply: no adjustment
		{"charging above prioritySoc", 80, -2000, 0, -2000, 0, -2000},
		// battery charge power hidden and residual power forced to 100W:
		// adding the adjustment back restores the unadjusted -2000W
		{"charging below prioritySoc", 30, -2000, 0, 100, -2100, -2000},
		// battery not charging: only the forced residual power applies
		{"discharging below prioritySoc", 30, 500, 0, 600, -100, 500},
		// excess DC power can only reach the battery, never the (AC) vehicle, so it
		// must stay netted out of the reconstructed surplus: of 2000W charging with
		// 500W un-redirectable DC excess, only 1500W is available to a boost loadpoint
		{"charging below prioritySoc with excess DC", 30, -2000, 500, 100, -1600, -1500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)

			meter := api.NewMockMeter(ctrl)
			meter.EXPECT().CurrentPower().Return(tc.power, nil).AnyTimes()

			battery := api.NewMockBattery(ctrl)
			battery.EXPECT().Soc().Return(tc.soc, nil).AnyTimes()

			var bat api.Meter = &struct {
				api.Meter
				api.Battery
			}{
				Meter:   meter,
				Battery: battery,
			}

			site := &Site{
				log:           util.NewLogger("foo"),
				batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{}, bat)},
				prioritySoc:   prioritySoc,
			}
			state, err := site.updateMeters()
			require.NoError(t, err)
			state.excessDCPower = tc.excessDC

			res := site.sitePower(state, 0, 0)
			assert.Equal(t, tc.expSitePower, res.power, "sitePower")
			assert.Equal(t, tc.expAdjustment, res.priorityAdjustment, "priority adjustment")
			assert.Equal(t, tc.expReconstructed, res.power+res.priorityAdjustment, "reconstructed (unadjusted) site power")
		})
	}
}

// TestSitePowerBatteryBufferRelaxedByForecast covers the §25 rule: a confident
// same-day refill forecast may relax an *enabled* buffer threshold (make
// batteryBuffered/batteryStart true where the static soc comparison alone would
// not), but never turns on a threshold the user left at 0 (disabled), and never
// relaxes anything without a forecast.
func TestSitePowerBatteryBufferRelaxedByForecast(t *testing.T) {
	ctrl := gomock.NewController(t)
	meter := api.NewMockMeter(ctrl) // sitePower only checks len(batteryMeters) > 0

	refillsToday := &types.BatteryForecast{
		Highest: &types.BatteryForecastPoint{Limit: true, Time: time.Now().Add(2 * time.Hour)},
	}

	newSite := func(bufferSoc, bufferStartSoc float64) *Site {
		return &Site{
			log:            util.NewLogger("foo"),
			batteryMeters:  []config.Device[api.Meter]{config.NewStaticDevice(config.Named{}, api.Meter(meter))},
			prioritySoc:    10,
			bufferSoc:      bufferSoc,
			bufferStartSoc: bufferStartSoc,
		}
	}

	state := func(soc float64, forecast *types.BatteryForecast) siteState {
		var s siteState
		s.battery.Soc = soc
		s.battery.Forecast = forecast
		return s
	}

	t.Run("below threshold, no forecast: static behaviour, unchanged", func(t *testing.T) {
		site := newSite(70, 80)
		res := site.sitePower(state(60, nil), 0, 0)
		assert.False(t, res.batteryBuffered)
		assert.False(t, res.batteryStart)
	})

	t.Run("below threshold, confident refill forecast: both relaxed", func(t *testing.T) {
		site := newSite(70, 80)
		res := site.sitePower(state(60, refillsToday), 0, 0)
		assert.True(t, res.batteryBuffered, "bufferSoc is enabled, forecast confirms refill")
		assert.True(t, res.batteryStart, "bufferStartSoc is enabled, forecast confirms refill")
	})

	t.Run("bufferStartSoc disabled: forecast never turns it on", func(t *testing.T) {
		site := newSite(70, 0)
		res := site.sitePower(state(60, refillsToday), 0, 0)
		assert.True(t, res.batteryBuffered)
		assert.False(t, res.batteryStart, "explicitly disabled (0) must stay off regardless of forecast")
	})

	t.Run("already above threshold: forecast changes nothing observable", func(t *testing.T) {
		site := newSite(70, 80)
		res := site.sitePower(state(90, nil), 0, 0)
		assert.True(t, res.batteryBuffered)
		assert.True(t, res.batteryStart)
	})

	// prioritySoc 50, bufferSoc 80, battery at 8% and idle: even with a confident
	// same-day refill forecast, the buffer must never relax below prioritySoc - that
	// would let a loadpoint drain an 8% home battery toward the inverter floor on
	// the strength of a forecast, which is exactly what prioritySoc exists to
	// prevent regardless of what the buffer thresholds say.
	t.Run("far below prioritySoc and idle: never relaxed, even with a confident forecast", func(t *testing.T) {
		site := &Site{
			log:            util.NewLogger("foo"),
			batteryMeters:  []config.Device[api.Meter]{config.NewStaticDevice(config.Named{}, api.Meter(meter))},
			prioritySoc:    50,
			bufferSoc:      80,
			bufferStartSoc: 80,
		}
		res := site.sitePower(state(8, refillsToday), 0, 0)
		assert.False(t, res.batteryBuffered, "8% is far below prioritySoc 50 - must never be buffer-eligible")
		assert.False(t, res.batteryStart)
	})
}

func TestGreenShare(t *testing.T) {
	tc := []struct {
		title                                                 string
		grid, pv, battery, home, lp                           float64
		greenShareTotal, greenShareHome, greenShareLoadpoints float64
	}{
		{
			"half grid, half pv, green home",
			1000, 1000, 0, 1000, 1000,
			0.5, 1, 0,
		},
		{
			"half grid, half pv, no home",
			1000, 1000, 0, 0, 2000,
			0.5, 1, 0.5,
		},
		{
			"half grid, half pv, no lp",
			2500, 2500, 0, 5000, 0,
			0.5, 0.5, 0,
		},
		{
			"full pv",
			0, 5000, 0, 1000, 4000,
			1, 1, 1,
		},
		{
			"full grid",
			5000, 0, 0, 1000, 4000,
			0, 0, 0,
		},
		{
			"half grid, half battery, green home",
			1000, 0, 1000, 1000, 1000,
			0.5, 1, 0,
		},
		{
			"half grid, half battery, no home",
			1000, 0, 1000, 0, 2000,
			0.5, 1, 0.5,
		},
		{
			"half grid, half battery, no lp",
			1000, 0, 1000, 2000, 0,
			0.5, 0.5, 0,
		},
		{
			"full pv, pv export",
			-5000, 10000, 0, 1000, 4000,
			1, 1, 1,
		},
		{
			"full pv, pv export, no lp",
			-5000, 10000, 0, 5000, 0,
			1, 1, 1,
		},
		{
			"full pv, pv export, battery charge",
			-2500, 10000, -2500, 1000, 4000,
			1, 1, 1,
		},
		{
			"full grid, battery charge",
			3000, 0, -1000, 1000, 1000,
			0, 0, 0,
		},
		{
			"full grid, battery charge, no lp",
			2000, 0, -1000, 1000, 0,
			0, 0, 0,
		},
		{
			"half grid, half pv, battery charge, no lp",
			1000, 1000, -1000, 1000, 0,
			0.5, 1, 0,
		},
		{
			"half grid, half pv, battery charge, home, lp",
			1000, 1000, -1000, 500, 500,
			0.5, 1, 0,
		},
		{
			"pv ac limited, battery charge & grid import",
			1000, 3000, -1000, 1000, 2000,
			0.75, 1, 0.5,
		},
	}

	for _, tc := range tc {
		t.Log(tc.title)

		s := &Site{
			siteState: siteState{
				gridPower: tc.grid,
				pvPower:   tc.pv,
				battery: types.BatteryState{
					Power: tc.battery,
				},
			},
		}

		totalPower := tc.grid + tc.pv + max(0, tc.battery)
		greenShareTotal := s.greenShare(0, totalPower)
		if greenShareTotal != tc.greenShareTotal {
			t.Errorf("greenShareTotal wanted %.3f, got %.3f", tc.greenShareTotal, greenShareTotal)
		}
		greenShareHome := s.greenShare(0, tc.home)
		if greenShareHome != tc.greenShareHome {
			t.Errorf("greenShareHome wanted %.3f, got %.3f", tc.greenShareHome, greenShareHome)
		}
		greenShareLoadpoints := s.greenShare(tc.home+max(0, -tc.battery), totalPower)
		if greenShareLoadpoints != tc.greenShareLoadpoints {
			t.Errorf("greenShareLoadpoints wanted %.3f, got %.3f", tc.greenShareLoadpoints, greenShareLoadpoints)
		}
	}
}

func TestRequiredBatteryMode(t *testing.T) {
	tc := []struct {
		gridChargeActive bool
		mode, res        api.BatteryMode
	}{
		{false, api.BatteryUnknown, api.BatteryUnknown}, // ignore
		{false, api.BatteryNormal, api.BatteryUnknown},  // ignore
		{false, api.BatteryHold, api.BatteryNormal},
		{false, api.BatteryCharge, api.BatteryNormal},

		{true, api.BatteryUnknown, api.BatteryCharge},
		{true, api.BatteryNormal, api.BatteryCharge},
		{true, api.BatteryHold, api.BatteryCharge},
		{true, api.BatteryCharge, api.BatteryUnknown}, // ignore
	}

	{
		// no battery
		res := new(Site).requiredBatteryMode(true, api.Rate{})
		assert.Equal(t, api.BatteryUnknown, res, "expected %s, got %s", api.BatteryUnknown, res)
	}

	for _, tc := range tc {
		t.Logf("%+v", tc)

		s := &Site{
			batteryMeters: []config.Device[api.Meter]{nil},
			batteryMode:   tc.mode,
		}

		res := s.requiredBatteryMode(tc.gridChargeActive, api.Rate{})
		assert.Equal(t, tc.res, res, "expected %s, got %s", tc.res, res)
	}
}

// TestCollectMetersFlagsFailedPower verifies collectMeters' failed slice: a real power read
// failure is flagged so callers can mark the resulting measurement incomplete (see
// updatePvMeters/updateBatteryMeters), while a meter that simply does not implement power
// (api.ErrNotAvailable, a permanent capability gap) is not - that is expected, not a failure.
func TestCollectMetersFlagsFailedPower(t *testing.T) {
	ctrl := gomock.NewController(t)
	site := &Site{log: util.NewLogger("foo")}

	ok := api.NewMockMeter(ctrl)
	ok.EXPECT().CurrentPower().Return(1000.0, nil).AnyTimes()

	failed := api.NewMockMeter(ctrl)
	failed.EXPECT().CurrentPower().Return(0.0, backoff.Permanent(errors.New("comm timeout"))).AnyTimes()

	notAvailable := api.NewMockMeter(ctrl)
	notAvailable.EXPECT().CurrentPower().Return(0.0, backoff.Permanent(api.ErrNotAvailable)).AnyTimes()

	mm, failedFlags := site.collectMeters("pv", []config.Device[api.Meter]{
		config.NewStaticDevice(config.Named{}, api.Meter(ok)),
		config.NewStaticDevice(config.Named{}, api.Meter(failed)),
		config.NewStaticDevice(config.Named{}, api.Meter(notAvailable)),
	})

	require.Len(t, mm, 3)
	require.Len(t, failedFlags, 3)

	assert.Equal(t, 1000.0, mm[0].Power)
	assert.False(t, failedFlags[0], "successful read must not be flagged")

	assert.Equal(t, 0.0, mm[1].Power, "failed read leaves power at its zero value")
	assert.True(t, failedFlags[1], "a real read failure must be flagged")

	assert.Equal(t, 0.0, mm[2].Power)
	assert.False(t, failedFlags[2], "ErrNotAvailable is a permanent capability gap, not a failure")
}

// testUpdater satisfies the updater interface: a loadpoint.MockAPI plus a stub Update that
// records the sitePower it was called with, and a settable gate() so tests can drive the
// optimizer suggestion updatePower reads.
type testUpdater struct {
	*loadpoint.MockAPI
	suggestion  *types.Suggestion
	sitePowerAt float64
}

func (u *testUpdater) gate() *types.Suggestion { return u.suggestion }

func (u *testUpdater) Update(sitePower, _ float64, _, _ api.Rates, _, _ bool, _ float64, _, _ *float64, _ *bool) {
	u.sitePowerAt = sitePower
}

// TestUpdatePowerOptimizerSurplusGate covers the second gate added on top of the #30541
// battery-boost carve-out: when the optimizer's current suggestion for this loadpoint is an
// active surplus-charge, the battery-priority adjustment is added back so pvMaxCurrent sees
// the surplus the optimizer already allocated, instead of prioritySoc hiding it a layer
// below. Any suggestion that is not an active surplus-charge - including the nil case
// clearSuggestions produces for a stale/absent/unsponsored optimizer - must leave today's
// static prioritySoc behaviour completely unchanged.
func TestUpdatePowerOptimizerSurplusGate(t *testing.T) {
	ctrl := gomock.NewController(t)

	meter := api.NewMockMeter(ctrl) // sitePower only checks len(batteryMeters) > 0

	const maxPower = 11000.0 // W

	newSite := func() *Site {
		return &Site{
			log:           util.NewLogger("foo"),
			batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{}, api.Meter(meter))},
			prioritySoc:   50,
			tariffs:       &tariff.Tariffs{},
		}
	}

	// battery charging below prioritySoc: sitePower() itself withholds -2100W
	// (priorityAdjustment) from the plain surplus of -2000W, leaving 100W - see
	// TestSitePowerPriorityAdjustment's "charging below prioritySoc" case.
	state := siteState{battery: types.BatteryState{Soc: 30, Power: -2000}}

	run := func(t *testing.T, suggestion *types.Suggestion) float64 {
		t.Helper()

		lp := &testUpdater{MockAPI: loadpoint.NewMockAPI(ctrl), suggestion: suggestion}
		lp.EXPECT().GetMode().Return(api.ModeNow).AnyTimes()
		lp.EXPECT().GetBatteryBoost().Return(boostDisabled).AnyTimes()
		lp.EXPECT().EffectiveMaxPower().Return(maxPower).AnyTimes()

		newSite().updatePower(lp, state, 0, nil, nil)

		return lp.sitePowerAt
	}

	t.Run("no suggestion: gate closed, static prioritySoc behaviour unchanged", func(t *testing.T) {
		assert.Equal(t, 100.0, run(t, nil))
	})

	t.Run("stop suggestion: gate closed", func(t *testing.T) {
		assert.Equal(t, 100.0, run(t, &types.Suggestion{Action: actionStop}))
	})

	t.Run("full-power charge: grid-fed by definition, not surplus, gate closed", func(t *testing.T) {
		assert.Equal(t, 100.0, run(t, &types.Suggestion{Action: actionCharge, Charge: maxPower}))
	})

	t.Run("charge suggestion with planned grid import: not surplus, gate closed", func(t *testing.T) {
		assert.Equal(t, 100.0, run(t, &types.Suggestion{Action: actionCharge, Charge: 5000, Grid: 500}))
	})

	t.Run("active surplus-charge: gate opens, unadjusted site power restored", func(t *testing.T) {
		assert.Equal(t, -2000.0, run(t, &types.Suggestion{Action: actionCharge, Charge: 5000, Grid: 0}))
	})
}
