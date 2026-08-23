package metrics

import (
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

	set, err := buildLedgerSlots(from, to, true)
	require.NoError(t, err)

	phys, err := deriveBatteryPhysics()
	require.NoError(t, err)

	rows, err := DecisionDeltas(from, to, set, &phys)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	require.Equal(t, "payback", rows[0].VetoReason)
	require.NotNil(t, rows[0].HindsightDeltaEUR)

	// applied (hold): import=1kWh -> cost 0.40. rejected (charge): forces a grid
	// charge to fill headroom too, so it costs strictly more - the veto was correct,
	// so the hindsight delta must be negative (applied cost less than the rejected
	// alternative would have).
	require.Less(t, *rows[0].HindsightDeltaEUR, 0.0)

	// no veto on the second slot -> nothing to compare
	require.Nil(t, rows[1].HindsightDeltaEUR)
}

func TestDecisionDeltasWithoutBatteryPhysics(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	loc := time.Now().Location()
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, loc)

	require.NoError(t, PersistControlSlot(base, batteryModeHold, batteryModeCharge, "payback", true, nil))

	set := &ledgerSlotSet{}
	rows, err := DecisionDeltas(base, base.Add(15*time.Minute), set, nil)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].HindsightDeltaEUR, "no battery physics available - must not fabricate a delta")
}
