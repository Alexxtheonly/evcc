package core

import (
	"errors"
	"testing"
	"time"

	evbus "github.com/asaskevich/EventBus"
	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/coordinator"
	"github.com/evcc-io/evcc/core/settings"
	"github.com/evcc-io/evcc/core/soc"
	"github.com/evcc-io/evcc/core/vehicle"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func expectVehiclePublish(vehicle *api.MockVehicle) {
	vehicle.EXPECT().GetTitle().Return("target").AnyTimes()
	vehicle.EXPECT().Capacity().AnyTimes()
	vehicle.EXPECT().Icon().AnyTimes()
	vehicle.EXPECT().Features().AnyTimes()
	vehicle.EXPECT().Phases().AnyTimes()
	vehicle.EXPECT().OnIdentified().AnyTimes()
}

func TestPublishSocAndRange(t *testing.T) {
	ctrl := gomock.NewController(t)
	clck := clock.NewMock()

	charger := api.NewMockCharger(ctrl)

	vehicle := api.NewMockVehicle(ctrl)
	expectVehiclePublish(vehicle)

	log := util.NewLogger("foo")
	lp := &Loadpoint{
		log:          log,
		bus:          evbus.New(),
		clock:        clck,
		charger:      charger,
		vehicle:      vehicle,
		chargeMeter:  &Null{}, // silence nil panics
		chargeRater:  &Null{}, // silence nil panics
		chargeTimer:  &Null{}, // silence nil panics
		socEstimator: soc.NewEstimator(log, vehicle),
		minCurrent:   minA,
		maxCurrent:   maxA,
		phases:       1,
		mode:         api.ModeNow,
	}

	// populate channels
	x, y, z := createChannels(t)
	attachChannels(lp, x, y, z)

	assert.Empty(t, lp.socUpdated)

	tc := []api.ChargeStatus{api.StatusB, api.StatusC}

	for _, tc := range tc {
		lp.status = tc

		assert.True(t, lp.vehicleSocPollAllowed())
		vehicle.EXPECT().Soc().Return(0.0, errors.New("foo"))
		lp.publishSocAndRange()

		clck.Add(time.Second)

		allowed := tc == api.StatusC
		assert.Equal(t, allowed, lp.vehicleSocPollAllowed())
		if allowed {
			vehicle.EXPECT().Soc().Return(0.0, errors.New("foo"))
		}
		lp.publishSocAndRange()
	}
}

func TestPublishSocAndRangeVehiclesAndChargers(t *testing.T) {
	ctrl := gomock.NewController(t)
	clck := clock.NewMock()

	socVehicle := 70.0
	socCharger := 80.0

	vehicle := api.NewMockVehicle(ctrl)
	vehicle.EXPECT().Soc().Return(socVehicle, nil).AnyTimes()
	vehicle.EXPECT().Capacity().Return(8.5).AnyTimes() // enable soc-based planning
	vehicle.EXPECT().Features().AnyTimes()

	offlineVehicle := api.NewMockVehicle(ctrl)
	offlineVehicle.EXPECT().Soc().AnyTimes()
	offlineVehicle.EXPECT().Capacity().Return(8.5).AnyTimes() // enable soc-based planning
	offlineVehicle.EXPECT().Features().Return([]api.Feature{api.Offline}).AnyTimes()

	charger := api.NewMockCharger(ctrl)

	chargerSoc := api.NewMockBattery(ctrl)
	chargerSoc.EXPECT().Soc().Return(socCharger, nil).AnyTimes()

	isoCharger := struct {
		*api.MockCharger
		*api.MockBattery
	}{
		charger, chargerSoc,
	}

	log := util.NewLogger("foo")

	tc := []struct {
		name     string
		charger  api.Charger
		vehicle  api.Vehicle
		soc      float64
		socBased bool // soc based planning
		socPoll  bool // may poll vehicle
	}{
		{
			name:     "offline vehicle",
			charger:  charger,
			vehicle:  offlineVehicle,
			soc:      0.0,
			socBased: false,
			socPoll:  false,
		},
		{
			name:     "regular vehicle",
			charger:  charger,
			vehicle:  vehicle,
			soc:      socVehicle,
			socBased: true,
			socPoll:  true,
		},
		{
			name:     "offline vehicle with iso charger",
			charger:  isoCharger,
			vehicle:  offlineVehicle,
			soc:      socCharger,
			socBased: true,
			socPoll:  false,
		},
		{
			name:     "regular vehicle with iso charger",
			charger:  isoCharger,
			vehicle:  vehicle,
			soc:      socCharger,
			socBased: true,
			socPoll:  true,
		},
	}

	for _, tc := range tc {
		lp := &Loadpoint{
			log:         log,
			bus:         evbus.New(),
			clock:       clck,
			charger:     tc.charger,
			vehicle:     tc.vehicle,
			chargeMeter: &Null{}, // silence nil panics
			chargeRater: &Null{}, // silence nil panics
			chargeTimer: &Null{}, // silence nil panics
			minCurrent:  minA,
			maxCurrent:  maxA,
			phases:      1,
			status:      api.StatusC,
			mode:        api.ModeNow,
		}

		// populate channels
		x, y, z := createChannels(t)
		attachChannels(lp, x, y, z)

		test := func(t *testing.T) {
			assert.Equal(t, tc.socPoll, lp.vehicleSocPollAllowed())
			lp.publishSocAndRange()
			assert.Equal(t, tc.soc, lp.vehicleSoc)

			// planner assumptions
			assert.Equal(t, tc.socBased, lp.socBasedPlanning())

			if tc.soc > 0 {
				d := time.Duration((1 - tc.soc/100) * float64(time.Hour))
				t.Log("d", d)
				assert.True(t, d < lp.GetPlanRequiredDuration(100, 10e3))
			}
		}

		t.Run(tc.name+" wo/estimator", test)

		lp.socEstimator = soc.NewEstimator(log, tc.vehicle)
		t.Run(tc.name+" w/estimator", test)
	}
}

func TestVehicleDetectByID(t *testing.T) {
	ctrl := gomock.NewController(t)

	v1 := api.NewMockVehicle(ctrl)
	v2 := api.NewMockVehicle(ctrl)

	type testcase struct {
		string
		ids     []string
		i1, i2  string
		res     api.Vehicle
		match   string
		prepare func(testcase)
	}
	tc := []testcase{
		{"1/_/_->0", []string{"1"}, "", "", nil, "", func(tc testcase) {
			v1.EXPECT().Identifiers().Return(nil)
			v2.EXPECT().Identifiers().Return(nil)
			v1.EXPECT().Identifiers().Return(nil)
			v2.EXPECT().Identifiers().Return(nil)
		}},
		{"1/1/2->1", []string{"1"}, "1", "2", v1, "1", func(tc testcase) {
			v1.EXPECT().Identifiers().Return([]string{tc.i1})
		}},
		{"2/1/2->2", []string{"2"}, "1", "2", v2, "2", func(tc testcase) {
			v1.EXPECT().Identifiers().Return([]string{tc.i1})
			v2.EXPECT().Identifiers().Return([]string{tc.i2})
		}},
		{"11/1*/2->1", []string{"11"}, "1*", "2", v1, "11", func(tc testcase) {
			v1.EXPECT().Identifiers().Return([]string{tc.i1})
			v2.EXPECT().Identifiers().Return([]string{tc.i2})
			v1.EXPECT().Identifiers().Return([]string{tc.i1})
			// v2.EXPECT().Identifiers().Return([]string{tc.i2})
		}},
		{"22/1*/2*->2", []string{"22"}, "1*", "2*", v2, "22", func(tc testcase) {
			v1.EXPECT().Identifiers().Return([]string{tc.i1})
			v2.EXPECT().Identifiers().Return([]string{tc.i2})
			v1.EXPECT().Identifiers().Return([]string{tc.i1})
			v2.EXPECT().Identifiers().Return([]string{tc.i2})
		}},
		{"2/_/*->2", []string{"2"}, "", "*", v2, "2", func(tc testcase) {
			v1.EXPECT().Identifiers().Return(nil)
			v2.EXPECT().Identifiers().Return([]string{tc.i2})
			v1.EXPECT().Identifiers().Return(nil)
			v2.EXPECT().Identifiers().Return([]string{tc.i2})
		}},
		// second identity matches, e.g. rfid tag and vehicle id
		{"x,2/1/2->2", []string{"x", "2"}, "1", "2", v2, "2", func(tc testcase) {
			v1.EXPECT().Identifiers().Return([]string{tc.i1})
			v2.EXPECT().Identifiers().Return([]string{tc.i2})
			v1.EXPECT().Identifiers().Return([]string{tc.i1})
			v2.EXPECT().Identifiers().Return([]string{tc.i2})
		}},
		// wildcard matches the second identity
		{"x,22/1/2*->2", []string{"x", "22"}, "1", "2*", v2, "22", func(tc testcase) {
			v1.EXPECT().Identifiers().Return([]string{tc.i1})
			v2.EXPECT().Identifiers().Return([]string{tc.i2})
			v1.EXPECT().Identifiers().Return([]string{tc.i1})
			v2.EXPECT().Identifiers().Return([]string{tc.i2})
			v1.EXPECT().Identifiers().Return([]string{tc.i1})
			v2.EXPECT().Identifiers().Return([]string{tc.i2})
			v1.EXPECT().Identifiers().Return([]string{tc.i1})
			v2.EXPECT().Identifiers().Return([]string{tc.i2})
		}},
	}

	for _, tc := range tc {
		t.Logf("%+v", tc)

		lp := &Loadpoint{
			log: util.NewLogger("foo"),
		}

		lp.coordinator = coordinator.NewAdapter(lp, coordinator.New(util.NewLogger("foo"), []api.Vehicle{v1, v2}))

		if tc.prepare != nil {
			tc.prepare(tc)
		}

		res, match := lp.selectVehicleByID(tc.ids...)
		if tc.res != res {
			t.Errorf("expected %v, got %v", tc.res, res)
		}
		if tc.match != match {
			t.Errorf("expected match %q, got %q", tc.match, match)
		}
	}
}

func TestDefaultVehicle(t *testing.T) {
	ctrl := gomock.NewController(t)

	mode := api.ModePV
	current := 66.6

	dflt := api.NewMockVehicle(ctrl)
	dflt.EXPECT().GetTitle().Return("default").AnyTimes()
	dflt.EXPECT().Icon().Return("").AnyTimes()
	dflt.EXPECT().Capacity().AnyTimes()
	dflt.EXPECT().Phases().AnyTimes()
	dflt.EXPECT().OnIdentified().Return(api.ActionConfig{
		Mode:       mode,
		MinCurrent: current,
	}).AnyTimes()

	vehicle := api.NewMockVehicle(ctrl)
	vehicle.EXPECT().GetTitle().Return("target").AnyTimes()
	vehicle.EXPECT().Icon().Return("").AnyTimes()
	vehicle.EXPECT().Capacity().AnyTimes()
	vehicle.EXPECT().Phases().AnyTimes()
	vehicle.EXPECT().OnIdentified().AnyTimes()

	lp := NewLoadpoint(util.NewLogger("foo"), settings.NewDatabaseSettingsAdapter("foo"))
	lp.DefaultMode = api.ModeOff // ondisconnect
	lp.defaultVehicle = dflt

	// populate channels
	x, y, z := createChannels(t)
	attachChannels(lp, x, y, z)

	title := func(v api.Vehicle) string {
		if v == nil {
			return "<nil>"
		}
		return v.GetTitle()
	}

	// non-default vehicle identified
	lp.setActiveVehicle(vehicle)
	assert.Equal(t, vehicle, lp.vehicle, "expected vehicle "+title(vehicle))
	assert.Equal(t, 6.0, lp.effectiveMinCurrent(), "current")

	// non-default vehicle disconnected
	lp.evVehicleDisconnectHandler()
	assert.Equal(t, dflt, lp.vehicle, "expected default vehicle")
	assert.Equal(t, mode, lp.GetMode(), "mode")
	assert.Equal(t, current, lp.effectiveMinCurrent(), "current")

	// default vehicle disconnected and reconnected
	lp.evVehicleDisconnectHandler()
	assert.Equal(t, mode, lp.GetMode(), "mode")
	assert.Equal(t, current, lp.effectiveMinCurrent(), "current")

	// set non-default vehicle during disconnect - should be default on connect
	lp.tasks.Clear()
	lp.evVehicleConnectHandler()
	assert.Equal(t, dflt, lp.vehicle, "expected default vehicle")
	assert.Equal(t, 1, lp.tasks.Size(), "task queue length")

	// guest connected
	lp.setActiveVehicle(nil)
	assert.Nil(t, lp.vehicle, "expected no vehicle")
}

// idCharger is a minimal charger implementing api.Identifier for identifyVehicle tests.
type idCharger struct {
	id string
}

func (c *idCharger) Status() (api.ChargeStatus, error) { return api.StatusB, nil }
func (c *idCharger) Enabled() (bool, error)            { return false, nil }
func (c *idCharger) Enable(bool) error                 { return nil }
func (c *idCharger) MaxCurrent(int64) error            { return nil }
func (c *idCharger) Identify() ([]string, error)       { return []string{c.id}, nil }

// TestReidentifyActiveVehicleKeepsMode is a regression test for #31499:
// re-identifying the already-active vehicle must not reapply its default mode.
func TestReidentifyActiveVehicleKeepsMode(t *testing.T) {
	ctrl := gomock.NewController(t)

	vehicle := api.NewMockVehicle(ctrl)
	vehicle.EXPECT().GetTitle().Return("target").AnyTimes()
	vehicle.EXPECT().Icon().Return("").AnyTimes()
	vehicle.EXPECT().Capacity().AnyTimes()
	vehicle.EXPECT().Phases().AnyTimes()
	vehicle.EXPECT().Identifiers().Return([]string{"rfid-1"}).AnyTimes()
	vehicle.EXPECT().OnIdentified().Return(api.ActionConfig{
		Mode: api.ModePV,
	}).AnyTimes()

	lp := NewLoadpoint(util.NewLogger("foo"), settings.NewDatabaseSettingsAdapter("foo"))
	lp.charger = &idCharger{id: "rfid-1"}
	lp.coordinator = coordinator.NewAdapter(lp, coordinator.New(util.NewLogger("foo"), []api.Vehicle{vehicle}))

	x, y, z := createChannels(t)
	attachChannels(lp, x, y, z)

	// vehicle already active via a different detection path (e.g. SoC poll)
	lp.setActiveVehicle(vehicle)
	assert.Equal(t, api.ModePV, lp.GetMode(), "mode after first identification")

	// user/system escalates to now in between
	lp.SetMode(api.ModeNow)

	// charger reports the same vehicle's RFID id - must not reapply default mode
	lp.identifyVehicle()
	assert.Equal(t, api.ModeNow, lp.GetMode(), "mode must not be reapplied for already-active vehicle")
}

// TestReassignActiveVehicleKeepsSoc is a regression test for #31063:
// re-assigning the already-active default vehicle on reconnect must not wipe a
// known soc. Only a genuine vehicle change (or disconnect) clears it.
func TestReassignActiveVehicleKeepsSoc(t *testing.T) {
	ctrl := gomock.NewController(t)

	vehicle := api.NewMockVehicle(ctrl)
	vehicle.EXPECT().GetTitle().Return("target").AnyTimes()
	vehicle.EXPECT().Icon().Return("").AnyTimes()
	vehicle.EXPECT().Capacity().AnyTimes()
	vehicle.EXPECT().Phases().AnyTimes()
	vehicle.EXPECT().OnIdentified().AnyTimes()

	lp := NewLoadpoint(util.NewLogger("foo"), settings.NewDatabaseSettingsAdapter("foo"))

	x, y, z := createChannels(t)
	attachChannels(lp, x, y, z)

	// vehicle active, soc read from a prior cycle
	lp.setActiveVehicle(vehicle)
	lp.vehicleSoc = 71

	// re-assign the same vehicle (reconnect churn) - soc must survive
	lp.setActiveVehicle(vehicle)
	assert.Equal(t, 71.0, lp.vehicleSoc, "soc must survive same-vehicle re-assign")

	// switching to no vehicle still clears it
	lp.setActiveVehicle(nil)
	assert.Equal(t, 0.0, lp.vehicleSoc, "soc must clear on vehicle change")
}

// TestActiveVehicleChangeTriggersOptimizer ensures the optimizer is re-run when
// the detected vehicle changes, as the loadpoint profile depends on it.
func TestActiveVehicleChangeTriggersOptimizer(t *testing.T) {
	ctrl := gomock.NewController(t)

	vehicle := api.NewMockVehicle(ctrl)
	vehicle.EXPECT().GetTitle().Return("target").AnyTimes()
	vehicle.EXPECT().Icon().Return("").AnyTimes()
	vehicle.EXPECT().Capacity().AnyTimes()
	vehicle.EXPECT().Phases().AnyTimes()
	vehicle.EXPECT().OnIdentified().AnyTimes()

	lp := NewLoadpoint(util.NewLogger("foo"), settings.NewDatabaseSettingsAdapter("foo"))
	s := new(mockSite)
	lp.site = s

	x, y, z := createChannels(t)
	attachChannels(lp, x, y, z)

	lp.setActiveVehicle(vehicle)
	assert.Equal(t, 1, s.optimized, "vehicle detected")

	// re-assigning the same vehicle is not a change
	lp.setActiveVehicle(vehicle)
	assert.Equal(t, 1, s.optimized, "same vehicle re-assigned")

	lp.setActiveVehicle(nil)
	assert.Equal(t, 2, s.optimized, "vehicle removed")
}

// integratedDeviceCharger is a minimal charger advertising the IntegratedDevice feature.
type integratedDeviceCharger struct{}

func (c *integratedDeviceCharger) Status() (api.ChargeStatus, error) { return api.StatusA, nil }
func (c *integratedDeviceCharger) Enabled() (bool, error)            { return false, nil }
func (c *integratedDeviceCharger) Enable(bool) error                 { return nil }
func (c *integratedDeviceCharger) MaxCurrent(int64) error            { return nil }
func (c *integratedDeviceCharger) Features() []api.Feature {
	return []api.Feature{api.IntegratedDevice}
}

// TestDisconnectIntegratedDeviceKeepsMode is a regression test for #30187:
// switching an integrated-device loadpoint to "off" makes a switch socket report
// StatusA (disconnect). The disconnect handler must NOT reset the mode to the
// configured DefaultMode in that case, otherwise the loadpoint immediately flips
// back to pv and the socket re-enables.
func TestDisconnectIntegratedDeviceKeepsMode(t *testing.T) {
	lp := NewLoadpoint(util.NewLogger("foo"), settings.NewDatabaseSettingsAdapter("foo"))
	lp.charger = &integratedDeviceCharger{}
	lp.DefaultMode = api.ModePV
	lp.setMode(api.ModeOff)

	x, y, z := createChannels(t)
	attachChannels(lp, x, y, z)

	lp.evVehicleDisconnectHandler()

	assert.Equal(t, api.ModeOff, lp.GetMode(), "integrated device disconnect must not reset mode")
}

func TestStartWakeUpTimerDisabled(t *testing.T) {
	for _, tc := range []struct {
		name        string
		features    []api.Feature
		wantRunning bool
	}{
		{"enabled", nil, true},
		{"disabled", []api.Feature{api.WakeUpDisabled}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)

			vehicle := api.NewMockVehicle(ctrl)
			vehicle.EXPECT().Features().Return(tc.features).AnyTimes()

			lp := &Loadpoint{
				log:         util.NewLogger("foo"),
				vehicle:     vehicle,
				wakeUpTimer: NewTimer(),
			}

			lp.startWakeUpTimer()

			assert.Equal(t, tc.wantRunning, lp.wakeUpTimer.Running())
		})
	}
}

func TestReconnectVehicle(t *testing.T) {
	tc := []struct {
		name      string
		vehicleId []string
	}{
		{"without vehicle id", nil},
		{"with vehicle id", []string{"foo"}},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			clck := clock.NewMock()

			type vehicleT struct {
				*api.MockVehicle
				*api.MockChargeState
			}

			v := api.NewMockVehicle(ctrl)
			vehicle := &vehicleT{v, api.NewMockChargeState(ctrl)}
			expectVehiclePublish(v)
			vehicle.MockVehicle.EXPECT().Identifiers().AnyTimes().Return(tc.vehicleId)
			vehicle.MockVehicle.EXPECT().Soc().Return(0.0, nil).AnyTimes()

			charger := api.NewMockCharger(ctrl)
			charger.EXPECT().Status().Return(api.StatusB, nil).AnyTimes()

			lp := &Loadpoint{
				log:         util.NewLogger("foo"),
				bus:         evbus.New(),
				clock:       clck,
				charger:     charger,
				chargeMeter: &Null{}, // silence nil panics
				chargeRater: &Null{}, // silence nil panics
				chargeTimer: &Null{}, // silence nil panics
				wakeUpTimer: NewTimer(),
				minCurrent:  minA,
				maxCurrent:  maxA,
				phases:      1,
				mode:        api.ModeNow,
			}

			lp.coordinator = coordinator.NewAdapter(lp, coordinator.New(util.NewLogger("foo"), []api.Vehicle{vehicle}))

			attachListeners(t, lp)

			// mode now
			charger.EXPECT().MaxCurrent(int64(maxA))
			// sync charger
			charger.EXPECT().Enabled().Return(true, nil)

			// vehicle not updated yet
			vehicle.MockChargeState.EXPECT().Status().Return(api.StatusA, nil)

			lp.Update(0, 0, nil, nil, false, false, 0, nil, nil, nil)
			ctrl.Finish()

			// detection started
			assert.Equal(t, lp.clock.Now(), lp.vehicleDetect, "vehicle detection not started")

			// vehicle not detected yet
			assert.Nil(t, lp.vehicle, "vehicle should be <nil>")

			// sync charger
			charger.EXPECT().Enabled().Return(true, nil)
			// vehicle not updated yet
			vehicle.MockChargeState.EXPECT().Status().Return(api.StatusB, nil)

			lp.Update(0, 0, nil, nil, false, false, 0, nil, nil, nil)
			ctrl.Finish()

			// vehicle detected
			assert.Equal(t, vehicle, lp.vehicle, "vehicle should be detected")
		})
	}
}

// TestPersistSocGradient verifies that an estimator's learned energy-per-soc-step survives
// past the estimator's own lifetime: persistSocGradient must store it against the vehicle,
// and a fresh estimator seeded via seedSocGradient must pick it back up - closing the loop
// that setActiveVehicle otherwise breaks on every unplug (a fresh soc.Estimator per attach).
func TestPersistSocGradient(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)

	ctrl := gomock.NewController(t)
	v := api.NewMockVehicle(ctrl)
	v.EXPECT().Capacity().Return(50.0).AnyTimes() // 50 kWh -> 50000 Wh
	v.EXPECT().GetTitle().Return("vehicle").AnyTimes()

	const name = "vehicle"
	require.NoError(t, config.Vehicles().Add(
		config.NewStaticDevice(config.Named{Name: name}, api.Vehicle(v)),
	))

	lp := NewLoadpoint(util.NewLogger("foo"), nil)

	// nothing learned yet: persisting is a no-op
	lp.socEstimator = soc.NewEstimator(lp.log, v)
	lp.persistSocGradient(v)
	_, ok := vehicle.Settings(lp.log, v).GetSocGradient()
	assert.False(t, ok, "untouched estimator must not be persisted")

	// drive the estimator through a real soc swing so it learns a gradient
	s := 20.0
	lp.socEstimator.Soc(&s, 0)
	s = 40.0 // socDiff 20 > 10: energyPerSocStep = 6000/20 = 300 Wh/%
	lp.socEstimator.Soc(&s, 6000)
	require.True(t, lp.socEstimator.Learned())

	lp.persistSocGradient(v)

	stored, ok := vehicle.Settings(lp.log, v).GetSocGradient()
	require.True(t, ok)
	assert.InDelta(t, 300.0, stored, 1e-9, "first-ever session is stored outright, nothing to blend with")

	// a second session blends with the stored value rather than overwriting it
	lp.socEstimator = soc.NewEstimator(lp.log, v)
	s = 20.0
	lp.socEstimator.Soc(&s, 0)
	s = 40.0
	lp.socEstimator.Soc(&s, 8000) // this session alone would learn 400 Wh/%

	lp.persistSocGradient(v)

	blended, ok := vehicle.Settings(lp.log, v).GetSocGradient()
	require.True(t, ok)
	assert.InDelta(t, 330.0, blended, 1e-9, "0.7*300 + 0.3*400")
	assert.NotEqual(t, 400.0, blended, "second session must not fully overwrite the first")

	// a fresh estimator picks the persisted gradient back up instead of starting over
	fresh := soc.NewEstimator(lp.log, v)
	require.NotEqual(t, blended, fresh.EnergyPerSocStep())
	lp.socEstimator = fresh
	lp.seedSocGradient(v)
	assert.Equal(t, blended, lp.socEstimator.EnergyPerSocStep())
}

// TestPersistSocGradientUnknownVehicle verifies persistSocGradient/seedSocGradient tolerate a
// vehicle that isn't a registered config device (vehicle.Settings falls back to a no-op
// dummy) and a nil db.Instance (no session history available) without panicking.
func TestPersistSocGradientUnknownVehicle(t *testing.T) {
	ctrl := gomock.NewController(t)
	v := api.NewMockVehicle(ctrl)
	v.EXPECT().Capacity().Return(50.0).AnyTimes()
	v.EXPECT().GetTitle().Return("unregistered").AnyTimes()

	lp := NewLoadpoint(util.NewLogger("foo"), nil)
	lp.socEstimator = soc.NewEstimator(lp.log, v)

	s := 20.0
	lp.socEstimator.Soc(&s, 0)
	s = 40.0
	lp.socEstimator.Soc(&s, 6000)
	require.True(t, lp.socEstimator.Learned())

	assert.NotPanics(t, func() { lp.persistSocGradient(v) })

	fresh := soc.NewEstimator(lp.log, v)
	lp.socEstimator = fresh
	assert.NotPanics(t, func() { lp.seedSocGradient(v) })
	assert.Equal(t, fresh.EnergyPerSocStep(), lp.socEstimator.EnergyPerSocStep(), "no db, no stored value: default is kept")
}
