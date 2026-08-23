package metrics

// Savings ledger per-slot decision replay (ADR-011 item 2/4): for each past slot,
// what was applied, what was suggested, why they differ, and what the rejected
// alternative would have cost.
//
// Scoped to battery-mode decisions only, per ADR-011's own scoping note: control_slots
// only ever records a battery mode (AppliedMode/SuggestedMode), so there is no
// loadpoint/vehicle counterfactual to build from this table without inventing data
// this table doesn't hold. A vehicle-side decision replay is a follow-up, not
// something to approximate here.

import (
	"context"
	"time"

	"github.com/evcc-io/evcc/server/db"
)

// DecisionRow is one control_slots row, plus (when computable) the euro cost of the
// veto: what the applied mode cost minus what the rejected suggestion would have
// cost, at that slot's realised price and measured home/PV/SoC.
//
// HindsightDeltaEUR is nil - never 0 - when it isn't computable: no veto happened
// (nothing to compare), the slot fell outside the ledger's valid slot set (see
// Coverage), or the site has no battery to simulate against. ADR-011 rule 3: absence
// is never a sentinel.
type DecisionRow struct {
	Ts                time.Time `json:"ts"`
	AppliedMode       string    `json:"appliedMode"`
	SuggestedMode     string    `json:"suggestedMode"`
	VetoReason        string    `json:"vetoReason,omitempty"`
	HealthOk          bool      `json:"healthOk"`
	HindsightDeltaEUR *float64  `json:"hindsightDeltaEur,omitempty"`
}

// DecisionDeltas replays every control_slots row in [from,to). set and phys should
// come from the same request's ComputeChain/buildLedgerSlots call so the replay uses
// the identical slot data and battery assumptions the chain was priced with; phys may
// be nil (no battery, or physics unavailable), in which case every row is still
// returned but HindsightDeltaEUR is always nil.
func DecisionDeltas(ctx context.Context, from, to time.Time, set *ledgerSlotSet, phys *batteryPhysics) ([]DecisionRow, error) {
	var rows []controlSlot
	if err := db.Instance.WithContext(ctx).Where("ts >= ? AND ts < ?", from.Unix(), to.Unix()).Order("ts").Find(&rows).Error; err != nil {
		return nil, err
	}

	bySlot := make(map[int64]slotData, len(set.Slots))
	for _, s := range set.Slots {
		bySlot[s.Start.Unix()] = s
	}

	out := make([]DecisionRow, 0, len(rows))
	for _, r := range rows {
		dr := DecisionRow{
			Ts:            time.Unix(r.Timestamp, 0),
			AppliedMode:   r.AppliedMode,
			SuggestedMode: r.SuggestedMode,
			VetoReason:    r.VetoReason,
			HealthOk:      r.HealthOk,
		}

		if phys != nil && r.AppliedMode != r.SuggestedMode {
			if s, ok := bySlot[r.Timestamp]; ok && s.BatterySocFrac != nil {
				socKWh := *s.BatterySocFrac * phys.CapacityKWh
				load := s.modelledLoadKWh()

				_, appliedFlow, _, _ := simulateSlotStep(r.AppliedMode, load, s.PVKWh, socKWh, *phys)
				_, rejectedFlow, _, _ := simulateSlotStep(r.SuggestedMode, load, s.PVKWh, socKWh, *phys)

				appliedCost := appliedFlow.ImportKWh*s.PriceGrid - appliedFlow.ExportKWh*s.PriceFeedIn
				rejectedCost := rejectedFlow.ImportKWh*s.PriceGrid - rejectedFlow.ExportKWh*s.PriceFeedIn

				// positive: the applied mode cost more than the rejected
				// alternative would have - i.e. the veto looks wrong in hindsight
				delta := appliedCost - rejectedCost
				dr.HindsightDeltaEUR = &delta
			}
		}

		out = append(out, dr)
	}

	return out, nil
}
