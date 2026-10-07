package core

import (
	"github.com/evcc-io/evcc/api"
)

// executedEVCharge is the first-slot energy (AC Wh) a vehicle loadpoint actually executes for
// the plan's charge, which robust dispatch fixes while it compares battery modes. It mirrors
// the loadpoint's decision order (core/loadpoint.go Update, loadpoint_optimizer.go):
//
//   - no charge, or full power: unchanged, full power reconstructed from the request limit;
//   - below minSoc the floor fast-charges and in mode Now the loadpoint charges at full
//     power, whatever the plan says: full power, capped to what still fits;
//   - a band vehicle in a slot at or below its reference: full power (cheapBandFullPower);
//   - a charge below suggestionThreshold is translated to stop: nothing;
//   - any other partial charge is executed as planned: either the pv loop tracks the
//     surplus it was planned from (surplusCharge) or the loadpoint sets that power as a
//     limited setpoint. It is never rounded up, so surplus never becomes grid power.
func executedEVCharge(detail batteryDetail, req energyRequest, i int, planned float32, seconds float64, wire func(float32) float64) float64 {
	bat := req.Batteries[i]
	full := wire(bat.CMax) * seconds / 3600
	switch {
	case planned <= .1:
		return 0
	case planned == float32(full):
		return full
	case bat.SInitial < bat.SMin, detail.mode == api.ModeNow, cheapBandFullPower(detail.band, req.TimeSeries.PN[0]):
		return storableCharge(full, bat, wire(req.EtaC), wire)
	case float64(planned)*3600/seconds <= suggestionThreshold:
		return 0
	}
	charge := float64(planned)
	// float32 response rounding must not put the fixed action just below c_min
	if minimum := wire(bat.CMin) * seconds / 3600; charge < minimum && minimum-charge < 1 {
		charge = minimum
	}
	return min(charge, storableCharge(full, bat, wire(req.EtaC), wire))
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
