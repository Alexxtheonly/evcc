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
// SlotFlowDeltaEUR is nil - never 0 - when it isn't computable: no veto happened
// (nothing to compare), the slot fell outside the ledger's valid slot set (see
// Coverage), the site has no battery to simulate against, or either mode on the row is
// one simulateSlotStep does not model. ADR-011 rule 3: absence is never a sentinel.
//
// This field was named HindsightDeltaEUR and is not true hindsight: it simulates
// applied vs. suggested for the ONE vetoed slot only, at that slot's own starting
// SoC, and stops there - it does not follow either trajectory forward to see what
// actually happened next. A charge vetoed at a very cheap price specifically because
// it would pay off in a LATER, more expensive slot has that payoff priced nowhere;
// the sign this field reports is determined by the direction of the vetoed decision
// (charging always reads as a cost, discharging always reads as a saving in the same
// slot it happened), not by whether the veto was actually right. See
// TestSlotFlowDeltaIsSlotLocalNotForwardHindsight for a worked example where this
// reads "veto vindicated" on a veto that cost roughly EUR 1. A true hindsight figure
// needs to price the rejected alternative forward until its simulated SoC rejoins the
// applied trajectory (or a full oracle replay, per ADR-011's "Potential" section,
// which doesn't exist yet) - out of scope here; renamed instead so this field cannot
// be mistaken for that.
type DecisionRow struct {
	Ts          time.Time `json:"ts"`
	AppliedMode string    `json:"appliedMode"`
	// SuggestedMode is nil - and omitted from the JSON - when no optimizer run
	// produced a suggestion for this slot, so a caller can report "none recorded"
	// instead of silently skipping the row or rendering a mode that was never
	// suggested. Legacy rows spelling that absence as "unknown" arrive here as nil
	// too - see decodeSuggestedMode.
	SuggestedMode *string `json:"suggestedMode,omitempty"`
	VetoReason    string  `json:"vetoReason,omitempty"`
	HealthOk      bool    `json:"healthOk"`
	// ModeChanged carries controlSlot's own flag forward: AppliedMode is a single
	// point sample taken seconds into the slot, and Phase A's doc comment on that
	// field says a reader must not assume it held for the whole 15 minutes when this
	// is true. DecisionDeltas' replay simulates the FULL slot under AppliedMode
	// regardless - dropping this flag would let a reader trust that simulation's
	// precision more than the source data supports.
	ModeChanged      bool     `json:"modeChanged"`
	SlotFlowDeltaEUR *float64 `json:"slotFlowDeltaEur,omitempty"`
}

// effectiveMode folds the two spellings of "evcc held no override this slot" into
// one. api.BatteryUnknown at site level means "no change required", which on a site
// with a battery is the same fact as api.BatteryNormal - persistControlSlot records
// Normal for such a site since core/site_optimizer.go's fix, but every row written
// before it does not, and an empty column is the same absence again.
//
// Folding here only ever REMOVES a SlotFlowDeltaEUR that would otherwise have been
// computed for a slot where the two modes are the same fact spelled differently - it
// can never invent one, because two genuinely different modes never fold together.
// The row's own AppliedMode/SuggestedMode are still emitted verbatim: on a site with
// no battery "unknown" means "there is no battery", and rewriting that to "normal"
// would assert a battery mode for a battery that does not exist.
func effectiveMode(mode string) string {
	if mode == "" || mode == batteryModeUnknown {
		return batteryModeNormal
	}
	return mode
}

// decodeSuggestedMode reads a stored suggested mode back as the presence or absence it
// actually recorded. Rows written before the column became nullable spell "no suggestion"
// as api.BatteryUnknown's "unknown" (an empty column is the same absence again), and on
// the write path that token is never a decision: batteryModeCandidate only leaves the mode
// at api.BatteryUnknown when no controllable battery produced a suggestion at all, and
// clearSuggestions/ResetOptimizerBatteryMode leave it after a failed run or an
// automatic-mode toggle. Decoding it here is recovering what the row meant, not inventing
// it - and doing it once, on the read path, keeps the stored bytes untouched and leaves
// the Go and TypeScript sides with a single rule instead of two.
//
// Note the asymmetry with effectiveMode above, which is deliberate: a stored APPLIED
// "unknown" means "evcc held no override", which on a site with a battery is normal
// operation, so it folds to normal rather than to absence.
func decodeSuggestedMode(mode *string) *string {
	if mode == nil || *mode == "" || *mode == batteryModeUnknown {
		return nil
	}
	return mode
}

// DecisionDeltas replays every control_slots row in [from,to). set and phys should
// come from the same request's ComputeChain/buildLedgerSlots call so the replay uses
// the identical slot data and battery assumptions the chain was priced with; phys may
// be nil (no battery, or physics unavailable), in which case every row is still
// returned but SlotFlowDeltaEUR is always nil.
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
		suggested := decodeSuggestedMode(r.SuggestedMode)

		dr := DecisionRow{
			Ts:            time.Unix(r.Timestamp, 0),
			AppliedMode:   r.AppliedMode,
			SuggestedMode: suggested,
			VetoReason:    r.VetoReason,
			HealthOk:      r.HealthOk,
			ModeChanged:   r.ModeChanged,
		}

		// no suggestion recorded means there is no rejected alternative to price -
		// distinct from a suggestion that happened to match what was applied, which
		// folds to no delta below.
		//
		// The comparison and the two simulations run on the SAME folded strings.
		// They used to differ - gate on effectiveMode, simulate on the raw column -
		// which only agreed because simulateSlotStep's old default branch happened to
		// replay an unrecognised mode as normal, the exact silent pricing its ok
		// return now refuses.
		if phys != nil && suggested != nil {
			applied, rejected := effectiveMode(r.AppliedMode), effectiveMode(*suggested)

			if s, found := bySlot[r.Timestamp]; found && applied != rejected && s.BatterySocFrac != nil {
				socKWh := *s.BatterySocFrac * phys.CapacityKWh
				load := s.modelledLoadKWh()

				_, appliedFlow, appliedOk := simulateSlotStep(applied, load, s.PVKWh, socKWh, *phys)
				_, rejectedFlow, rejectedOk := simulateSlotStep(rejected, load, s.PVKWh, socKWh, *phys)

				// a mode neither this replay nor anything else in the package
				// models leaves SlotFlowDeltaEUR nil: "not understood" is an
				// absence, and ADR-011 rule 3 forbids spelling it as a figure.
				if appliedOk && rejectedOk {
					appliedCost := appliedFlow.ImportKWh*s.PriceGrid - appliedFlow.ExportKWh*s.PriceFeedIn
					rejectedCost := rejectedFlow.ImportKWh*s.PriceGrid - rejectedFlow.ExportKWh*s.PriceFeedIn

					// positive: the applied mode cost more than the rejected
					// alternative would have, WITHIN THIS SLOT ONLY - see
					// SlotFlowDeltaEUR's doc comment for why this is not hindsight.
					delta := appliedCost - rejectedCost
					dr.SlotFlowDeltaEUR = &delta
				}
			}
		}

		out = append(out, dr)
	}

	return out, nil
}
