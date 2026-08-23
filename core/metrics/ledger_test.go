package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/require"
)

// TestComputeLedgerHappyPath is a basic end-to-end sanity check: a normal
// battery-configured period returns a realised-cost figure, a chain, and decision
// rows all agreeing on the same underlying data.
func TestComputeLedgerHappyPath(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	bat := mustCreateEntity(t, Battery, "bat1")
	loc := time.Now().Location()
	seedBatteryCalibration(t, bat, time.Date(2026, 7, 1, 0, 0, 0, 0, loc))

	base := time.Date(2026, 8, 15, 0, 0, 0, 0, loc)
	require.NoError(t, persist(grid, base, 2.0, 0, nil, false, false))
	require.NoError(t, persist(home, base, 2.0, 0, nil, false, false))
	soc := 50.0
	require.NoError(t, persist(bat, base, 0, 0, &soc, false, false))
	g, f := 0.30, 0.05
	require.NoError(t, PersistTariffs(base, &g, &f, nil, nil))
	require.NoError(t, PersistControlSlot(base, batteryModeNormal, batteryModeNormal, "", true, nil))

	ledger, err := ComputeLedger(context.Background(), base, base.Add(15*time.Minute))
	require.NoError(t, err)

	require.NotNil(t, ledger.Chain)
	require.Empty(t, ledger.ChainUnavailable)
	require.InDelta(t, ledger.Chain.Worlds[3].Settled.PerSlot, ledger.Realised.Settled.PerSlot, 1e-9,
		"Realised and the chain's W3/actual entry price the identical measured series and must agree")
	require.Len(t, ledger.Decisions, 1)
}

// TestChainNotesCarryLabellingCaveats covers ADR-011 rule 7: estimated figures and
// their caveats must be labelled in the payload itself, not left to a code comment or
// an unwritten UI convention. This checks the three caveats a battery-configured,
// partial-coverage period must surface: invoice comparability, that Routing includes
// round-trip conversion losses, that Timing is only real money under per-slot
// settlement, and that the periodAverage figures are a mean over valid slots only.
func TestChainNotesCarryLabellingCaveats(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	bat := mustCreateEntity(t, Battery, "bat1")
	loc := time.Now().Location()
	seedBatteryCalibration(t, bat, time.Date(2026, 7, 1, 0, 0, 0, 0, loc))

	base := time.Date(2026, 8, 16, 0, 0, 0, 0, loc)
	g, f := 0.30, 0.05
	// slot 0: clean. slot 1: no battery SoC reading, so it's dropped -> coverage < 1.
	require.NoError(t, persist(grid, base, 1.0, 0, nil, false, false))
	require.NoError(t, persist(home, base, 1.0, 0, nil, false, false))
	soc := 50.0
	require.NoError(t, persist(bat, base, 0, 0, &soc, false, false))
	require.NoError(t, PersistTariffs(base, &g, &f, nil, nil))

	slot1 := base.Add(15 * time.Minute)
	require.NoError(t, persist(grid, slot1, 1.0, 0, nil, false, false))
	require.NoError(t, persist(home, slot1, 1.0, 0, nil, false, false))
	require.NoError(t, PersistTariffs(slot1, &g, &f, nil, nil))
	// no battery row for slot1

	chain, err := ComputeChain(context.Background(), base, base.Add(30*time.Minute))
	require.NoError(t, err)

	require.Contains(t, chain.Notes, noteInvoiceComparability)
	require.Contains(t, chain.Notes, noteRoutingIncludesLosses)
	require.Contains(t, chain.Notes, noteTimingSettlement)
	require.Contains(t, chain.Notes, notePeriodAverageCoverage(chain.Coverage))
}

// TestComputeLedgerDegradesOnBatteryPhysicsRefusal covers the Priority-5 fix: a
// battery-physics refusal (here, a configured battery with a valid SoC reading but no
// charge/discharge history to derive a capacity from, and no persisted device
// capacity) must not take the realised-cost figure down with it - RealisedCost's own
// doc comment says it's deliberately independent of the chain.
func TestComputeLedgerDegradesOnBatteryPhysicsRefusal(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	bat := mustCreateEntity(t, Battery, "bat1")

	loc := time.Now().Location()
	base := time.Date(2026, 8, 15, 0, 0, 0, 0, loc)

	require.NoError(t, persist(grid, base, 2.0, 0, nil, false, false))
	require.NoError(t, persist(home, base, 2.0, 0, nil, false, false))
	soc := 50.0
	require.NoError(t, persist(bat, base, 0, 0, &soc, false, false)) // one reading, no charge/discharge evidence
	g, f := 0.30, 0.05
	require.NoError(t, PersistTariffs(base, &g, &f, nil, nil))

	ledger, err := ComputeLedger(context.Background(), base, base.Add(15*time.Minute))
	require.NoError(t, err, "a battery-physics refusal must degrade, not fail the whole request")

	require.Nil(t, ledger.Chain)
	require.NotEmpty(t, ledger.ChainUnavailable)
	require.InDelta(t, 2.0*0.30, ledger.Realised.Settled.PerSlot, 1e-9,
		"the realised-cost figure must survive a chain-only refusal")
}
