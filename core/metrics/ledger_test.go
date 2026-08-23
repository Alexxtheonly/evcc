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

// TestComputeLedgerDegradesOnRateCeilingRefusal covers the adversarial finding behind
// ErrBatteryRateCeilingUnavailable: MaxChargeKWh/MaxDischargeKWh are the p99 of
// OBSERVED per-slot energy (see rateLimitPercentile's doc comment), and percentile()
// returns 0 for an empty slice. On a site where the controller has been holding the
// battery, or where it has simply never discharged, the discharge sample list is
// empty - before this fix, computeW2 silently used a 0 rate ceiling, the dumb-rule
// battery could never discharge, W2 collapsed towards W1, and the battery's entire
// real value was booked to Control with no refusal and no note. This seeds a battery
// with clean CHARGE-only history (capacity still derives fine via the charge-only
// fallback) and zero discharge evidence, then a period whose battery must discharge
// to cover a deficit - the chain must refuse rather than report a misleadingly small
// Battery contribution.
func TestComputeLedgerDegradesOnRateCeilingRefusal(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	bat := mustCreateEntity(t, Battery, "bat1")

	loc := time.Now().Location()

	// charge-only calibration: 3 clean 1.0kWh charge windows, capacity derives to
	// 10.0kWh via the charge-only fallback (see deriveBatteryCapacityFromHistory) -
	// no discharge sample is ever recorded.
	soc := 20.0
	ts := time.Date(2026, 7, 1, 0, 0, 0, 0, loc)
	for range 3 {
		s := soc
		require.NoError(t, persist(bat, ts, 1.0, 0, &s, false, false))
		soc += 9.0
		ts = ts.Add(15 * time.Minute)
	}
	sFinal := soc
	require.NoError(t, persist(bat, ts, 0, 0, &sFinal, false, false))

	base := time.Date(2026, 8, 15, 0, 0, 0, 0, loc)
	require.NoError(t, persist(grid, base, 2.0, 0, nil, false, false))
	require.NoError(t, persist(home, base, 2.0, 0, nil, false, false)) // pure deficit: W2 must discharge to model it
	socNow := 50.0
	require.NoError(t, persist(bat, base, 0, 0, &socNow, false, false))
	g, f := 0.30, 0.05
	require.NoError(t, PersistTariffs(base, &g, &f, nil, nil))

	ledger, err := ComputeLedger(context.Background(), base, base.Add(15*time.Minute))
	require.NoError(t, err, "a rate-ceiling refusal must degrade, not fail the whole request")

	require.Nil(t, ledger.Chain)
	require.NotEmpty(t, ledger.ChainUnavailable)
	require.InDelta(t, 2.0*0.30, ledger.Realised.Settled.PerSlot, 1e-9,
		"the realised-cost figure must survive a chain-only refusal")

	_, err = ComputeChain(context.Background(), base, base.Add(15*time.Minute))
	require.ErrorIs(t, err, ErrBatteryRateCeilingUnavailable)
}
