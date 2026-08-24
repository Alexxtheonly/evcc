package metrics

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/require"
)

// TestDecisionDeltasHindsight covers item 4: a vetoed suggestion carries the euro
// cost of the road not taken, computed against that slot's realised price and
// measured home/PV/SoC.
func TestDecisionDeltasHindsight(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	bat := mustCreateEntity(t, Battery, "bat1")

	loc := time.Now().Location()
	seedBatteryCalibration(t, bat, time.Date(2026, 7, 1, 0, 0, 0, 0, loc))

	base := time.Date(2026, 8, 6, 12, 0, 0, 0, loc)

	// one slot: 1kWh deficit, battery half full, an expensive grid price. The
	// suggestion ("charge", forcing a grid-charge on top of the load) was vetoed in
	// favour of "hold" - in hindsight hold was cheaper, since charging would have
	// bought even more at the same high price.
	soc := 0.5
	require.NoError(t, persist(grid, base, 1.0, 0, nil, false, false))
	require.NoError(t, persist(home, base, 1.0, 0, nil, false, false))
	socPct := soc * 100
	require.NoError(t, persist(bat, base, 0, 0, &socPct, false, false))
	g, f := 0.40, 0.05
	require.NoError(t, PersistTariffs(base, &g, &f, nil, nil))

	require.NoError(t, PersistControlSlot(base, batteryModeHold, batteryModeCharge, "payback", true, nil))

	// a second, un-vetoed slot: nothing to compare
	second := base.Add(15 * time.Minute)
	require.NoError(t, persist(grid, second, 1.0, 0, nil, false, false))
	require.NoError(t, persist(home, second, 1.0, 0, nil, false, false))
	require.NoError(t, persist(bat, second, 0, 0, &socPct, false, false))
	require.NoError(t, PersistTariffs(second, &g, &f, nil, nil))
	require.NoError(t, PersistControlSlot(second, batteryModeNormal, batteryModeNormal, "", true, nil))

	from := base
	to := second.Add(15 * time.Minute)

	set, err := buildLedgerSlots(context.Background(), from, to, true, true, nil)
	require.NoError(t, err)

	phys, err := deriveBatteryPhysics(context.Background())
	require.NoError(t, err)

	rows, err := DecisionDeltas(context.Background(), from, to, set, &phys)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	require.Equal(t, "payback", rows[0].VetoReason)
	require.NotNil(t, rows[0].SlotFlowDeltaEUR)

	// applied (hold): import=1kWh -> cost 0.40. rejected (charge): forces a grid
	// charge to fill headroom too, so it costs strictly more - the veto was correct,
	// so the hindsight delta must be negative (applied cost less than the rejected
	// alternative would have).
	require.Less(t, *rows[0].SlotFlowDeltaEUR, 0.0)

	// no veto on the second slot -> nothing to compare
	require.Nil(t, rows[1].SlotFlowDeltaEUR)
}

// TestDecisionDeltasCarriesModeChanged covers the Priority-5 finding: AppliedMode is
// a single point sample taken seconds into the slot (controlSlot's own doc comment),
// and ModeChanged flags when it's known to not have held for the whole 15 minutes -
// DecisionDeltas simulates the full slot under AppliedMode regardless, so dropping
// this flag would understate how much to trust that simulation.
func TestDecisionDeltasCarriesModeChanged(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	loc := time.Now().Location()
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, loc)

	require.NoError(t, PersistControlSlot(base, batteryModeNormal, batteryModeNormal, "", true, nil))
	require.NoError(t, MarkControlSlotModeChanged(base))

	set := &ledgerSlotSet{}
	rows, err := DecisionDeltas(context.Background(), base, base.Add(15*time.Minute), set, nil)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].ModeChanged)
}

func TestDecisionDeltasWithoutBatteryPhysics(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	loc := time.Now().Location()
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, loc)

	require.NoError(t, PersistControlSlot(base, batteryModeHold, batteryModeCharge, "payback", true, nil))

	set := &ledgerSlotSet{}
	rows, err := DecisionDeltas(context.Background(), base, base.Add(15*time.Minute), set, nil)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].SlotFlowDeltaEUR, "no battery physics available - must not fabricate a delta")
}

// TestSlotFlowDeltaIsSlotLocalNotForwardHindsight covers the A11 finding: what was
// HindsightDeltaEUR (renamed - see SlotFlowDeltaEUR's doc comment) prices only the
// vetoed slot itself, simulating applied vs. suggested for ONE slot with
// simulateSlotStep and stopping there. A charge that was vetoed at a very cheap price
// specifically because it would pay off in a LATER, expensive slot has that payoff
// priced nowhere - the sign this field reports is determined by the direction of the
// vetoed decision (charging always looks like a cost, discharging always looks like a
// saving), not by whether the veto was actually right.
//
// Fixture: slot 1 at 0.05 EUR/kWh, 0.2kWh deficit, veto="payback" (suggested charge,
// applied normal/hold). Slot 2 at 0.50 EUR/kWh, 3.0kWh deficit - the price the vetoed
// charge would have displaced, at 20x the slot-1 price. A human reading "the veto
// looks wrong" from a positive SlotFlowDeltaEUR would expect that reasoning to hold
// here; instead this field reports -0.135 ("veto vindicated") because it never prices
// slot 2 at all. This is documented, not silently left as a surprise: the field is
// named for what it actually is, and chain.Notes carries the caveat in the payload
// (ADR-011 rule 7) rather than only in a code comment.
func TestSlotFlowDeltaIsSlotLocalNotForwardHindsight(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	bat := mustCreateEntity(t, Battery, "bat1")
	require.NoError(t, bat.updateCapacity(19.32))

	loc := time.Now().Location()

	// isolated history rows purely to establish charge/discharge evidence (see
	// ErrBatteryRateCeilingUnavailable) and a 2.5kWh rate ceiling in both directions -
	// no SoC reading, so they don't participate in FloorFrac or capacity derivation.
	require.NoError(t, persist(bat, time.Date(2026, 6, 1, 0, 0, 0, 0, loc), 2.5, 0, nil, false, false))
	require.NoError(t, persist(bat, time.Date(2026, 6, 2, 0, 0, 0, 0, loc), 0, 2.5, nil, false, false))

	// a 5% floor reading, well below the period's own 20% - without it FloorFrac would
	// equal the period's own SoC (the only other reading on record) and the applied
	// "normal" mode would find zero headroom to discharge, changing the numbers below.
	lowSoc := 5.0
	require.NoError(t, persist(bat, time.Date(2026, 6, 3, 0, 0, 0, 0, loc), 0, 0, &lowSoc, false, false))

	slot1 := time.Date(2026, 8, 6, 12, 0, 0, 0, loc)
	slot2 := slot1.Add(15 * time.Minute)

	soc := 20.0
	require.NoError(t, persist(grid, slot1, 0.2, 0, nil, false, false))
	require.NoError(t, persist(home, slot1, 0.2, 0, nil, false, false))
	require.NoError(t, persist(bat, slot1, 0, 0, &soc, false, false))
	g1, f1 := 0.05, 0.0
	require.NoError(t, PersistTariffs(slot1, &g1, &f1, nil, nil))
	require.NoError(t, PersistControlSlot(slot1, batteryModeNormal, batteryModeCharge, "payback", true, nil))

	require.NoError(t, persist(grid, slot2, 3.0, 0, nil, false, false))
	require.NoError(t, persist(home, slot2, 3.0, 0, nil, false, false))
	require.NoError(t, persist(bat, slot2, 0, 0, &soc, false, false))
	g2, f2 := 0.50, 0.0
	require.NoError(t, PersistTariffs(slot2, &g2, &f2, nil, nil))
	require.NoError(t, PersistControlSlot(slot2, batteryModeNormal, batteryModeNormal, "", true, nil))

	from, to := slot1, slot2.Add(15*time.Minute)

	ledger, err := ComputeLedger(context.Background(), from, to, nil)
	require.NoError(t, err)
	require.NotNil(t, ledger.Chain, "chain must be available: %s", ledger.ChainUnavailable)
	require.Len(t, ledger.Decisions, 2)

	require.NotNil(t, ledger.Decisions[0].SlotFlowDeltaEUR)
	require.InDelta(t, -0.135, *ledger.Decisions[0].SlotFlowDeltaEUR, 1e-9,
		"documents the known slot-local limitation: a charge that would have paid off next slot still reads as vindicated")

	found := slices.ContainsFunc(ledger.Chain.Notes, func(n string) bool {
		return strings.Contains(n, "slotFlowDeltaEur") || strings.Contains(n, "SlotFlowDeltaEUR") || strings.Contains(n, "later slot")
	})
	require.True(t, found, "chain.Notes must disclose that the per-decision delta only prices the vetoed slot itself, not any later slot the decision would have affected")
}

// TestDecisionDeltasFoldsUnknownAgainstNormal covers the F3 finding: the 11 rows the
// live database had at the time carried applied "unknown" against suggested "normal"
// - two spellings of "evcc held no override", not a veto - and the raw string
// comparison priced every one of them as a veto worth EUR 0.00. Absence must not
// serialise as a figure (ADR-011 rule 3), and every consumer of /api/savingsledger
// sees this field, not only the one Vue component that knew to fold the modes itself.
func TestDecisionDeltasFoldsUnknownAgainstNormal(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	bat := mustCreateEntity(t, Battery, "bat1")

	loc := time.Now().Location()
	seedBatteryCalibration(t, bat, time.Date(2026, 7, 1, 0, 0, 0, 0, loc))

	base := time.Date(2026, 8, 6, 12, 0, 0, 0, loc)
	g, f := 0.40, 0.05
	socPct := 50.0

	// three slots, all fully measured so a delta IS computable if anything asks for
	// one: a legacy row ("unknown" vs "normal"), an empty applied mode (the same
	// absence spelled a third way), and a real veto as the control.
	modes := [][2]string{
		{batteryModeUnknown, batteryModeNormal},
		{"", batteryModeNormal},
		{batteryModeHold, batteryModeCharge},
	}
	for i, m := range modes {
		ts := base.Add(time.Duration(i) * 15 * time.Minute)
		require.NoError(t, persist(grid, ts, 1.0, 0, nil, false, false))
		require.NoError(t, persist(home, ts, 1.0, 0, nil, false, false))
		require.NoError(t, persist(bat, ts, 0, 0, &socPct, false, false))
		require.NoError(t, PersistTariffs(ts, &g, &f, nil, nil))
		require.NoError(t, PersistControlSlot(ts, m[0], m[1], "", true, nil))
	}

	from, to := base, base.Add(45*time.Minute)
	set, err := buildLedgerSlots(context.Background(), from, to, true, true, nil)
	require.NoError(t, err)
	phys, err := deriveBatteryPhysics(context.Background())
	require.NoError(t, err)

	rows, err := DecisionDeltas(context.Background(), from, to, set, &phys)
	require.NoError(t, err)
	require.Len(t, rows, 3)

	require.Nil(t, rows[0].SlotFlowDeltaEUR, `applied "unknown" against suggested "normal" is not a veto - must carry no delta`)
	require.Nil(t, rows[1].SlotFlowDeltaEUR, `an empty applied mode against "normal" is not a veto either`)

	// the raw wire words are still what was recorded: on a site with no battery
	// "unknown" means "there is no battery", so the fold stays a comparison rule.
	require.Equal(t, batteryModeUnknown, rows[0].AppliedMode)
	require.Equal(t, batteryModeNormal, rows[0].SuggestedMode)

	// control: a genuine veto is untouched by the fold
	require.NotNil(t, rows[2].SlotFlowDeltaEUR)
}
