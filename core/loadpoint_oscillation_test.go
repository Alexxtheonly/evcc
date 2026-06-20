package core

import (
	"testing"

	evbus "github.com/asaskevich/EventBus"
	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/coordinator"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/util"
	"go.uber.org/mock/gomock"
)

// oscChargeStateVehicle is a vehicle that also reports a charge state (so it is
// eligible for status-based detection by the coordinator) and carries no RFID
// identifiers - mirroring the teslamate vehicles in the live report.
type oscChargeStateVehicle struct {
	*api.MockVehicle
	*api.MockChargeState
}

func newOscVehicle(ctrl *gomock.Controller, title string, status api.ChargeStatus) *oscChargeStateVehicle {
	v := api.NewMockVehicle(ctrl)
	v.EXPECT().GetTitle().Return(title).AnyTimes()
	v.EXPECT().Icon().Return("").AnyTimes()
	v.EXPECT().Capacity().AnyTimes()
	v.EXPECT().Phases().AnyTimes()
	v.EXPECT().Features().Return(nil).AnyTimes()
	v.EXPECT().Identifiers().Return(nil).AnyTimes()
	v.EXPECT().OnIdentified().Return(api.ActionConfig{}).AnyTimes()
	v.EXPECT().Soc().Return(0.0, nil).AnyTimes()

	cs := api.NewMockChargeState(ctrl)
	cs.EXPECT().Status().Return(status, nil).AnyTimes()

	return &oscChargeStateVehicle{v, cs}
}

// oscCharger is a charger reporting a constant status.
type oscCharger struct{ status api.ChargeStatus }

func (c *oscCharger) Status() (api.ChargeStatus, error) { return c.status, nil }
func (c *oscCharger) Enabled() (bool, error)            { return true, nil }
func (c *oscCharger) Enable(bool) error                 { return nil }
func (c *oscCharger) MaxCurrent(int64) error            { return nil }

func newOscLoadpoint(t *testing.T, clck clock.Clock, charger api.Charger, dflt api.Vehicle, status api.ChargeStatus) *Loadpoint {
	t.Helper()

	lp := &Loadpoint{
		log:            util.NewLogger("lp"),
		bus:            evbus.New(),
		clock:          clck,
		charger:        charger,
		chargeMeter:    &Null{},
		chargeRater:    &Null{},
		chargeTimer:    &Null{},
		wakeUpTimer:    NewTimer(),
		minCurrent:     minA,
		maxCurrent:     maxA,
		phases:         1,
		mode:           api.ModeNow,
		status:         status, // already connected (not StatusNone)
		defaultVehicle: dflt,
	}

	x, y, z := createChannels(t)
	attachChannels(lp, x, y, z)

	return lp
}

// TestOscillationPerCycleLoopIsStable confirms the user's guard analysis: the
// per-cycle update-loop block (core/loadpoint.go ~2080-2089) does NOT reassign a
// vehicle when each loadpoint has a configured default. With vehicle != nil,
// vehicleUnidentified() is false, so identifyVehicleByStatus() never runs, and
// the loop alone cannot oscillate. The live oscillation therefore does not
// originate in this block - it requires the coordinator to first nil out a
// loadpoint's vehicle (see TestTwoDefaultsStableAfterTransientTransfer).
func TestOscillationPerCycleLoopIsStable(t *testing.T) {
	ctrl := gomock.NewController(t)
	clck := clock.NewMock()

	luna := newOscVehicle(ctrl, "Luna", api.StatusC)
	calypso := newOscVehicle(ctrl, "Calypso", api.StatusB)

	coord := coordinator.New(util.NewLogger("coord"), []api.Vehicle{luna, calypso})

	lp1 := newOscLoadpoint(t, clck, &oscCharger{status: api.StatusC}, luna, api.StatusC)
	lp2 := newOscLoadpoint(t, clck, &oscCharger{status: api.StatusB}, calypso, api.StatusB)
	lp1.coordinator = coordinator.NewAdapter(lp1, coord)
	lp2.coordinator = coordinator.NewAdapter(lp2, coord)

	lp1.vehicleDefaultOrDetect()
	lp2.vehicleDefaultOrDetect()

	perCycle := func(lp *Loadpoint) {
		lp.identifyVehicle() // no-op without api.Identifier charger (no RFID)
		if lp.vehicleUnidentified() {
			lp.identifyVehicleByStatus()
		}
	}

	transitions := 0
	prev1, prev2 := lp1.GetVehicle(), lp2.GetVehicle()
	for cycle := 0; cycle < 8; cycle++ {
		perCycle(lp1)
		perCycle(lp2)
		if lp1.GetVehicle() != prev1 {
			transitions++
		}
		if lp2.GetVehicle() != prev2 {
			transitions++
		}
		prev1, prev2 = lp1.GetVehicle(), lp2.GetVehicle()
	}

	if transitions != 0 {
		t.Errorf("per-cycle loop oscillated (%d transitions); expected stable defaults", transitions)
	}
}

// TestTwoDefaultsStableAfterTransientTransfer is the deterministic reproduction
// of the live vehicle-assignment bug and encodes the correct contract.
//
// Setup mirrors the live two-wallbox / two-Tesla report:
//
//	LP1: charger status C, configured default Luna    (Luna charging)
//	LP2: charger status B, configured default Calypso  (Calypso idle)
//
// Sequence (matches the live log exactly):
//
//  1. defaults assigned on connect       -> "LP1: unknown -> Luna", "LP2: unknown -> Calypso"
//  2. LP2 transiently picks up Luna via   -> "LP2: Calypso -> Luna" (+ coordinator
//     status detection                       defers LP1.SetVehicle(nil): "LP1: Luna -> unknown")
//  3. LP1 re-asserts its default Luna      -> "LP1: unknown -> Luna" (+ coordinator
//     each control cycle                      defers LP2.SetVehicle(nil): "LP2: Luna -> unknown")
//
// After step 3 LP2 is left with no vehicle, and because the coordinator's
// SetVehicle(nil) round-trip calls stopVehicleDetection(), LP2.vehicleUnidentified()
// returns false and LP2 never re-detects its own default. LP2 settles at <nil>:
// Calypso is abandoned and its vehicleSoc stays 0 - exactly the live API steady
// state (LP1 vehicle=Luna, LP2 vehicle=”, vehicleDetectionActive=false on both).
//
// CONTRACT: two loadpoints, two ChargeState vehicles, asymmetric C/B status, each
// with its own configured default => each loadpoint stably keeps its OWN default,
// with no oscillation and no abandonment.
//
// This test FAILS on the current branch (LP2 ends <nil>). On stock upstream/master
// the same scenario fails differently - via double-assignment (Luna active on both
// loadpoints) - so #31069 changed the failure mode from a stuck double-assignment
// into abandonment/oscillation; it did not make the two-default scenario stable.
func TestTwoDefaultsStableAfterTransientTransfer(t *testing.T) {
	ctrl := gomock.NewController(t)
	clck := clock.NewMock()

	luna := newOscVehicle(ctrl, "Luna", api.StatusC)
	calypso := newOscVehicle(ctrl, "Calypso", api.StatusB)

	coord := coordinator.New(util.NewLogger("coord"), []api.Vehicle{luna, calypso})

	lp1 := newOscLoadpoint(t, clck, &oscCharger{status: api.StatusC}, luna, api.StatusC)
	lp2 := newOscLoadpoint(t, clck, &oscCharger{status: api.StatusB}, calypso, api.StatusB)
	lp1.coordinator = coordinator.NewAdapter(lp1, coord)
	lp2.coordinator = coordinator.NewAdapter(lp2, coord)

	name := func(v api.Vehicle) string {
		switch v {
		case nil:
			return "<nil>"
		case api.Vehicle(luna):
			return "Luna"
		case api.Vehicle(calypso):
			return "Calypso"
		}
		return "?"
	}

	// (1) defaults assigned on connect
	lp1.setActiveVehicle(luna)
	lp2.setActiveVehicle(calypso)

	// (2) transient: LP2 picks up Luna via status detection
	lp2.setActiveVehicle(luna)

	// (3) control loop: LP1 re-asserts its configured default each cycle; LP2 runs
	// the exact guarded per-cycle identification block (identifyVehicleByStatus only
	// when vehicleUnidentified()). This faithfully reproduces the branch's settled
	// state - LP2 never recovers its default.
	for cycle := 0; cycle < 4; cycle++ {
		lp1.setActiveVehicle(lp1.defaultVehicle)

		lp2.identifyVehicle() // no-op without api.Identifier charger
		if lp2.vehicleUnidentified() {
			lp2.identifyVehicleByStatus()
		}
	}

	t.Logf("FINAL: LP1=%s LP2=%s", name(lp1.GetVehicle()), name(lp2.GetVehicle()))

	if v := lp1.GetVehicle(); v != api.Vehicle(luna) {
		t.Errorf("LP1 must keep its default Luna, got %s", name(v))
	}
	if v := lp2.GetVehicle(); v != api.Vehicle(calypso) {
		t.Errorf("LP2 must restore its default Calypso, got %s (abandoned/oscillating)", name(v))
	}
	// invariant: no vehicle owned by two loadpoints at once
	if v := lp1.GetVehicle(); v != nil && v == lp2.GetVehicle() {
		t.Errorf("%s active on both loadpoints at once", name(v))
	}
	// invariant: every held vehicle is owned by the holding loadpoint
	if v := lp2.GetVehicle(); v != nil && coord.Owner(v) != loadpoint.API(lp2) {
		t.Errorf("LP2 holds %s but coordinator owner mismatch", name(v))
	}
}
