package core

import (
	"github.com/evcc-io/evcc/api"
)

// executedEVCharge is the first-slot energy (AC Wh) a vehicle loadpoint actually executes for
// the plan's charge, which robust dispatch fixes while it compares battery modes. It mirrors
// the loadpoint's decision order (core/loadpoint.go Update, loadpoint_optimizer.go):
//
//   - no charge: nothing;
//   - full power by the loadpoint's own tolerance (optimizerCharging: within
//     suggestionThreshold of the maximum power): full power, reconstructed from the limit;
//   - below minSoc the floor fast-charges until minSoc, then the loadpoint follows the plan:
//     full power up to the floor's energy, or the planned charge if that is more;
//   - in mode Now the loadpoint charges at full power, whatever the plan says;
//   - a charge below suggestionThreshold is translated to stop: nothing;
//   - any other partial charge, band vehicles included, is executed as planned: either the
//     pv loop tracks the surplus it was planned from (surplusCharge) or the loadpoint sets
//     that power as a limited setpoint. It is never rounded up, so surplus never becomes
//     grid power. A setpoint below minimum current at the active phases makes minCharging
//     switch to one phase at minimum current, the c_min the plan respects; until that
//     switch is possible it runs three phases at minimum current, which is not modelled.
//
// Every result is capped to what still fits below the ceiling.
func executedEVCharge(detail batteryDetail, req energyRequest, i int, planned float32, seconds float64, wire func(float32) float64) float64 {
	bat := req.Batteries[i]
	full := wire(bat.CMax) * seconds / 3600
	etaC := wire(req.EtaC)
	plannedPower := float64(planned) * 3600 / seconds
	charge := float64(planned)
	switch {
	case planned <= .1:
		return 0
	case plannedPower >= wire(bat.CMax)-suggestionThreshold, detail.mode == api.ModeNow:
		charge = full
	case bat.SInitial < bat.SMin:
		floor := (wire(bat.SMin) - wire(bat.SInitial)) / etaC
		charge = max(charge, min(full, floor))
	case plannedPower <= suggestionThreshold:
		return 0
	default:
		// float32 response rounding must not put the fixed action just below c_min
		if minimum := wire(bat.CMin) * seconds / 3600; charge < minimum && minimum-charge < 1 {
			charge = minimum
		}
	}
	return storableCharge(charge, bat, etaC, wire)
}

// storableCharge caps a first-slot charge (AC Wh) to the room left below the ceiling: the
// loadpoint stops at its limit, the model must too.
func storableCharge(charge float64, bat energyBattery, etaC float64, wire func(float32) float64) float64 {
	if bat.EtaC != nil {
		etaC = *bat.EtaC
	}
	ceiling := wire(bat.SMax)
	if bat.SCapacity > 0 {
		ceiling = wire(bat.SCapacity)
	}
	return max(0, min(charge, (ceiling-wire(bat.SInitial))/etaC))
}
