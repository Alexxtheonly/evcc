package metrics

// Savings ledger per-slot decision replay: for each past slot, what was applied, what
// was suggested, why they differ, and what the rejected alternative would have cost.
//
// Scoped to battery-mode decisions only: control_slots only ever records a battery
// mode (AppliedMode/SuggestedMode), so there is no
// loadpoint/vehicle counterfactual to build from this table without inventing data
// this table doesn't hold. A vehicle-side decision replay is a follow-up, not
// something to approximate here.

import (
	"context"
	"errors"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"gorm.io/gorm"
)

// DecisionRow records the immediate cash difference and the subsequent conditional outcome.
// SlotFlowDeltaEUR covers only this slot; Outcome follows later recorded controls.
type DecisionRow struct {
	OptimizerSnapshotID *uint64          `json:"optimizerSnapshotId,omitempty"`
	Outcome             *DecisionOutcome `json:"outcome,omitempty"`
	Ts                  time.Time        `json:"ts"`
	AppliedMode         string           `json:"appliedMode"`
	// SuggestedMode is nil - and omitted from the JSON - when no optimizer run
	// produced a suggestion for this slot, so a caller can report "none recorded"
	// instead of silently skipping the row or rendering a mode that was never
	// suggested. Legacy rows spelling that absence as "unknown" arrive here as nil
	// too - see decodeSuggestedMode.
	SuggestedMode *string `json:"suggestedMode,omitempty"`
	VetoReason    string  `json:"vetoReason,omitempty"`
	HealthOk      bool    `json:"healthOk"`
	// ModeChanged carries controlSlot's own flag forward: AppliedMode is a single
	// point sample taken seconds into the slot, and a reader must not assume it held
	// for the whole 15 minutes when this is true. DecisionDeltas' replay simulates
	// the FULL slot under AppliedMode
	// regardless - dropping this flag would let a reader trust that simulation's
	// precision more than the source data supports.
	ModeChanged      bool     `json:"modeChanged"`
	SlotFlowDeltaEUR *float64 `json:"slotFlowDeltaEur,omitempty"`
}

// effectiveMode folds the two spellings of "evcc held no override this slot" into
// one. api.BatteryUnknown at site level means "no change required", which on a site
// with a battery is the same fact as api.BatteryNormal - persistControlSlot records
// Normal for such a site, but older rows do not, and an empty column is the same
// absence again.
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

// DecisionDeltas returns decisions in [from,to), following completed evidence beyond the view.
func DecisionDeltas(ctx context.Context, from, to time.Time, set *ledgerSlotSet, phys *batteryPhysics, feedInStatic ...*float64) ([]DecisionRow, error) {
	completedThrough := time.Now().Truncate(tariff.SlotDuration)
	evidenceEnd := to.Add(48*time.Hour - tariff.SlotDuration)
	if completedThrough.Before(evidenceEnd) {
		evidenceEnd = completedThrough
	}
	queryEnd := to
	if evidenceEnd.After(queryEnd) {
		queryEnd = evidenceEnd
	}
	var rows []controlSlot
	if err := db.Instance.WithContext(ctx).Where("ts >= ? AND ts < ?", from.Unix(), queryEnd.Unix()).Order("ts").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 || rows[0].Timestamp >= to.Unix() {
		return []DecisionRow{}, nil
	}

	bySlot := make(map[int64]slotData, len(set.Slots))
	for _, s := range set.Slots {
		if !s.Start.Add(tariff.SlotDuration).After(completedThrough) {
			bySlot[s.Start.Unix()] = s
		}
	}
	if len(rows) > 0 && rows[len(rows)-1].Timestamp >= to.Unix() && evidenceEnd.After(to) {
		var feedIn *float64
		if len(feedInStatic) > 0 {
			feedIn = feedInStatic[0]
		}
		future, err := buildLedgerSlots(ctx, to, evidenceEnd, true, true, feedIn)
		if err != nil {
			return nil, err
		}
		for _, s := range future.Slots {
			bySlot[s.Start.Unix()] = s
		}
	}
	snapshots := make(map[uint64]*OptimizerSnapshot)
	for _, r := range rows {
		if r.OptimizerSnapshotID != nil {
			id := *r.OptimizerSnapshotID
			if _, ok := snapshots[id]; !ok {
				s, err := GetOptimizerSnapshot(id)
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, err
				}
				snapshots[id] = s
			}
		}
	}

	out := make([]DecisionRow, 0, len(rows))
	for i, r := range rows {
		if r.Timestamp >= to.Unix() {
			break
		}
		suggested := decodeSuggestedMode(r.SuggestedMode)
		p, source, valid := batteryPhysics{}, "battery_physics_unavailable", false
		if phys != nil {
			p, source, valid = *phys, "legacy_assumed_efficiency_and_observed_limits", true
		}
		if r.OptimizerSnapshotID != nil {
			p, source, valid = snapshotPhysics(snapshots[*r.OptimizerSnapshotID], p)
		}
		if r.SnapshotUnavailable {
			valid = false
			source = "historical_snapshot_unavailable_or_assumptions_changed"
		}

		dr := DecisionRow{
			OptimizerSnapshotID: r.OptimizerSnapshotID,
			Ts:                  time.Unix(r.Timestamp, 0),
			AppliedMode:         r.AppliedMode,
			SuggestedMode:       suggested,
			VetoReason:          r.VetoReason,
			HealthOk:            r.HealthOk,
			ModeChanged:         r.ModeChanged,
		}

		// no suggestion recorded means there is no rejected alternative to price -
		// distinct from a suggestion that happened to match what was applied, which
		// folds to no delta below.
		//
		// The comparison and the two simulations must run on the SAME folded strings.
		// Gating on effectiveMode while simulating the raw column silently replays an
		// unrecognised mode as normal - exactly the pricing simulateSlotStep's ok
		// return exists to refuse.
		if valid && suggested != nil {
			applied, rejected := effectiveMode(r.AppliedMode), effectiveMode(*suggested)

			if s, found := bySlot[r.Timestamp]; found && applied != rejected && s.BatterySocFrac != nil {
				socKWh := *s.BatterySocFrac * p.CapacityKWh
				load := s.modelledLoadKWh()

				_, appliedFlow, appliedOk := simulateSlotStep(applied, load, s.PVKWh, socKWh, p)
				_, rejectedFlow, rejectedOk := simulateSlotStep(rejected, load, s.PVKWh, socKWh, p)

				// a mode neither this replay nor anything else in the package
				// models leaves SlotFlowDeltaEUR nil: "not understood" is an
				// absence, and must not be spelled as a figure.
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

		if suggested != nil && effectiveMode(r.AppliedMode) != effectiveMode(*suggested) {
			if valid {
				dr.Outcome = replayOutcome(i, rows, bySlot, p, source, evidenceEnd, snapshots)
			} else {
				dr.Outcome = &DecisionOutcome{Status: "unpriced", Reason: source, AssumptionsSource: source}
			}
		}
		out = append(out, dr)
	}

	return out, nil
}
