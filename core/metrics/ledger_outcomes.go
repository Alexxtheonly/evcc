package metrics

import (
	"encoding/json"
	"math"
	"time"

	"github.com/evcc-io/evcc/tariff"
)

// DecisionOutcome compares one alternative under subsequent recorded controls, not reoptimized controls.
type DecisionOutcome struct {
	Status                 string     `json:"status"`
	Reason                 string     `json:"reason,omitempty"`
	Through                *time.Time `json:"through,omitempty"`
	Slots                  int        `json:"slots"`
	CashDeltaEUR           *float64   `json:"cashDeltaEur,omitempty"`
	WearDeltaEUR           *float64   `json:"wearDeltaEur,omitempty"`
	TerminalEnergyDeltaKWh *float64   `json:"terminalEnergyDeltaKWh,omitempty"`
	TerminalValueDeltaEUR  *float64   `json:"terminalValueDeltaEur,omitempty"`
	NetDeltaEUR            *float64   `json:"netDeltaEur,omitempty"`
	AssumptionsSource      string     `json:"assumptionsSource"`
}

func snapshotPhysics(s *OptimizerSnapshot, fallback batteryPhysics) (batteryPhysics, string, bool) {
	if s == nil {
		return fallback, "legacy_assumed_efficiency_and_observed_limits", true
	}
	var economics struct {
		Batteries []SnapshotBatteryEconomics `json:"batteries"`
	}
	if s.Version != 1 || json.Unmarshal(s.Economics, &economics) != nil || len(economics.Batteries) != 1 {
		return fallback, "unsupported_snapshot_economics", false
	}
	e := economics.Batteries[0]
	if e.Name == "" || !finite(e.CapacityKWh) || e.CapacityKWh <= 0 || !finite(e.EtaC) || e.EtaC <= 0 || e.EtaC > 1 || !finite(e.EtaD) || e.EtaD <= 0 || e.EtaD > 1 || !finite(e.FloorFrac) || e.FloorFrac < 0 || e.FloorFrac >= 1 || !finite(e.MaxChargeKWh) || e.MaxChargeKWh <= 0 || !finite(e.MaxDischargeKWh) || e.MaxDischargeKWh <= 0 {
		return fallback, "invalid_snapshot_economics", false
	}
	if e.WearPerKWh != nil && (!finite(*e.WearPerKWh) || *e.WearPerKWh < 0) {
		return fallback, "invalid_snapshot_wear", false
	}
	return batteryPhysics{CapacityKWh: e.CapacityKWh, CapacitySource: "snapshot", EtaC: e.EtaC, EtaD: e.EtaD, EtaSource: e.Source, FloorFrac: e.FloorFrac, FloorSource: "snapshot", MaxChargeKWh: e.MaxChargeKWh, MaxDischargeKWh: e.MaxDischargeKWh, HasChargeEvidence: true, HasDischargeEvidence: true, WearPerKWh: e.WearPerKWh}, "optimizer_snapshot", true
}

func replayOutcome(index int, rows []controlSlot, slots map[int64]slotData, phys batteryPhysics, source string, to time.Time) *DecisionOutcome {
	out := &DecisionOutcome{Status: "unpriced", AssumptionsSource: source}
	first := rows[index]
	s, ok := slots[first.Timestamp]
	if !ok || s.BatterySocFrac == nil {
		out.Reason = "missing_meter_or_price"
		return out
	}
	if first.ModeChanged {
		out.Reason = "unrecorded_mode_transition"
		return out
	}
	suggested := decodeSuggestedMode(first.SuggestedMode)
	if suggested == nil || effectiveMode(first.AppliedMode) == effectiveMode(*suggested) {
		out.Reason = "no_rejected_alternative"
		return out
	}
	applied, rejected := *s.BatterySocFrac*phys.CapacityKWh, *s.BatterySocFrac*phys.CapacityKWh
	cash, wear, inventory := 0., 0., 0.
	out.Status = "pending"
	for i := index; i < len(rows); i++ {
		r := rows[i]
		expected := first.Timestamp + int64(i-index)*int64(tariff.SlotDuration.Seconds())
		s, ok := slots[r.Timestamp]
		if r.Timestamp != expected || !ok || r.ModeChanged {
			out.Status = "interrupted"
			out.Reason = "gap_or_unrecorded_mode_transition"
			break
		}
		if i > index && !sameSnapshot(first.OptimizerSnapshotID, r.OptimizerSnapshotID) {
			// New snapshots may change capacity, efficiency or control semantics mid-trajectory.
			out.Status = "interrupted"
			out.Reason = "planning_assumptions_changed"
			break
		}
		aMode, rMode := effectiveMode(r.AppliedMode), effectiveMode(r.AppliedMode)
		if i == index {
			rMode = effectiveMode(*suggested)
		}
		aNext, aFlow, aOK := simulateSlotStep(aMode, s.modelledLoadKWh(), s.PVKWh, applied, phys)
		rNext, rFlow, rOK := simulateSlotStep(rMode, s.modelledLoadKWh(), s.PVKWh, rejected, phys)
		if !aOK || !rOK {
			out.Status = "interrupted"
			out.Reason = "unsupported_mode"
			break
		}
		cash += (aFlow.ImportKWh-rFlow.ImportKWh)*s.PriceGrid - (aFlow.ExportKWh-rFlow.ExportKWh)*s.PriceFeedIn
		if phys.WearPerKWh != nil {
			wear += (max(0, applied-aNext) - max(0, rejected-rNext)) * *phys.WearPerKWh
		}
		applied, rejected = aNext, rNext
		inventory = (applied - rejected) * phys.EtaD * max(0, s.PriceGrid)
		end := s.Start.Add(tariff.SlotDuration)
		out.Through = &end
		out.Slots++
		if math.Abs(applied-rejected) < 1e-6 {
			out.Status = "completed"
			break
		}
	}
	if out.Slots == 0 {
		out.Status = "unpriced"
		return out
	}
	if out.Status == "pending" && out.Through.Before(to.Truncate(tariff.SlotDuration)) {
		out.Status = "interrupted"
		out.Reason = "missing_subsequent_control"
	}
	energy := applied - rejected
	out.CashDeltaEUR = &cash
	out.TerminalEnergyDeltaKWh = &energy
	out.TerminalValueDeltaEUR = &inventory
	if phys.WearPerKWh != nil {
		out.WearDeltaEUR = &wear
	}
	net := cash + wear - inventory
	out.NetDeltaEUR = &net
	return out
}

func sameSnapshot(a, b *uint64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
