package metrics

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/samber/lo"
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
	require.NoError(t, PersistControlSlot(base, batteryModeNormal, lo.ToPtr(batteryModeNormal), "", true, nil))

	ledger, err := ComputeLedger(context.Background(), base, base.Add(15*time.Minute), nil)
	require.NoError(t, err)

	require.NotNil(t, ledger.Chain)
	require.Empty(t, ledger.ChainUnavailable)
	require.InDelta(t, ledger.Chain.Worlds[3].Settled.PerSlot, ledger.Realised.Settled.PerSlot, 1e-9,
		"Realised and the chain's W3/actual entry price the identical measured series and must agree")
	require.Len(t, ledger.Decisions, 1)
}

// TestChainNotesCarryLabellingCaveats: estimated figures and
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

	chain, err := ComputeChain(context.Background(), base, base.Add(30*time.Minute), nil)
	require.NoError(t, err)

	require.Contains(t, chain.Notes, noteInvoiceComparability)
	require.Contains(t, chain.Notes, noteRoutingIncludesLosses)
	require.Contains(t, chain.Notes, noteTimingSettlement)
	require.Contains(t, chain.Notes, notePeriodAverageCoverage(chain.Coverage))
}

// TestComputeLedgerDegradesOnBatteryPhysicsRefusal: a
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

	ledger, err := ComputeLedger(context.Background(), base, base.Add(15*time.Minute), nil)
	require.NoError(t, err, "a battery-physics refusal must degrade, not fail the whole request")

	require.Nil(t, ledger.Chain)
	require.NotEmpty(t, ledger.ChainUnavailable)
	require.InDelta(t, 2.0*0.30, ledger.Realised.Settled.PerSlot, 1e-9,
		"the realised-cost figure must survive a chain-only refusal")
}

// TestComputeLedgerDegradesOnRateCeilingRefusal covers the adversarial finding behind
// ErrBatteryRateCeilingUnavailable: MaxChargeKWh/MaxDischargeKWh are the p99 of
// OBSERVED per-slot energy (see rateLimitPercentile's doc comment), and Percentile
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

	ledger, err := ComputeLedger(context.Background(), base, base.Add(15*time.Minute), nil)
	require.NoError(t, err, "a rate-ceiling refusal must degrade, not fail the whole request")

	require.Nil(t, ledger.Chain)
	require.NotEmpty(t, ledger.ChainUnavailable)
	require.InDelta(t, 2.0*0.30, ledger.Realised.Settled.PerSlot, 1e-9,
		"the realised-cost figure must survive a chain-only refusal")

	_, err = ComputeChain(context.Background(), base, base.Add(15*time.Minute), nil)
	require.ErrorIs(t, err, ErrBatteryRateCeilingUnavailable)
}

// TestChainNotesEVTimingUnattributed covers A14: W0/W1/W2 all take a loadpoint's
// charge at its REALISED timestamp (see slotData.modelledLoadKWh), so shifting when a
// car charges produces exactly EUR 0 of attributed value no matter how much the
// timing actually saved - Contributions[2] ("Control") is arithmetically correct at
// 0 for a site with no battery and one loadpoint, but nothing in the payload says
// EV charge timing isn't attributed to any measure.
func TestChainNotesEVTimingUnattributed(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	lp := mustCreateEntity(t, Loadpoint, "lp-1")

	loc := time.Now().Location()
	base := time.Date(2026, 8, 10, 3, 0, 0, 0, loc)

	// no PV, no battery: EV charges 5kWh at the cheap overnight price, all bought
	// from the grid, exactly what actually happened.
	require.NoError(t, persist(grid, base, 5.4, 0, nil, false, false))
	require.NoError(t, persist(home, base, 0.4, 0, nil, false, false))
	require.NoError(t, persist(lp, base, 5.0, 0, nil, false, false))
	g, f := 0.18, 0.05
	require.NoError(t, PersistTariffs(base, &g, &f, nil, nil))

	chain, err := ComputeChain(context.Background(), base, base.Add(15*time.Minute), nil)
	require.NoError(t, err)

	require.InDelta(t, 0.0, chain.Contributions[2].Settled.PerSlot, 1e-9,
		"arithmetically correct: the counterfactual buys the identical EV energy the real site did")

	found := slices.ContainsFunc(chain.Notes, func(n string) bool {
		return strings.Contains(n, "EV") && strings.Contains(n, "not attributed")
	})
	require.True(t, found, "chain.Notes must say EV charge timing is not attributed to any measure")
}

// TestChainNotesFeedInZeroExplained: when every slot's feed-in price is
// EUR 0 (a fixed placeholder tariff, common until a real feed-in rate is wired up),
// every export line in the payload reads exactly EUR 0.00 - correct, but with nothing
// in the payload saying why, indistinguishable from a computation that silently lost
// every export.
func TestChainNotesFeedInZeroExplained(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	pv := mustCreateEntity(t, PV, "pv1")

	loc := time.Now().Location()
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, loc)

	// PV surplus is exported this slot, so the export line is genuinely exercised,
	// not merely absent.
	require.NoError(t, persist(grid, base, 0, 1.0, nil, false, false))
	require.NoError(t, persist(home, base, 0.5, 0, nil, false, false))
	require.NoError(t, persist(pv, base, 1.5, 0, nil, false, false))
	g, f := 0.30, 0.0
	require.NoError(t, PersistTariffs(base, &g, &f, nil, nil))

	chain, err := ComputeChain(context.Background(), base, base.Add(15*time.Minute), nil)
	require.NoError(t, err)

	require.InDelta(t, 0.0, chain.Worlds[3].Settled.PerSlot-0.0, 1e-9) // export priced at 0 either way, sanity check

	found := slices.ContainsFunc(chain.Notes, func(n string) bool {
		return strings.Contains(n, "feed-in") && strings.Contains(n, "0.00")
	})
	require.True(t, found, "chain.Notes must explain that feed-in is configured at EUR 0 for this period, not silently zeroed")
}

// TestChainPublishesMeterResidual: R = grid_import - grid_export + pv +
// battery_discharge - battery_charge - home - loadpoint is NOT an identity on real
// data, even though HomeKWh is itself defined as this same residual at the power
// level - grid/home are integrated from instantaneous power while PV/battery/
// loadpoint come from device-register deltas on their own polling cadence, booked
// into whichever slot the read landed in. This fixture deliberately seeds one such
// mismatch (PV/loadpoint readings that don't quite balance against grid/home) and
// asserts the residual is computed and published, not silently absorbed.
func TestChainPublishesMeterResidual(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	pv := mustCreateEntity(t, PV, "pv1")
	lp := mustCreateEntity(t, Loadpoint, "lp-1")

	loc := time.Now().Location()
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, loc)

	// R = grid_import(1.0) - grid_export(0) + pv(0.6) + 0 - 0 - home(0.5) - lp(1.0)
	//   = 1.0 + 0.6 - 0.5 - 1.0 = 0.1kWh - a PV read that landed a touch late/early
	// relative to the grid/home slot boundary, exactly the class this diagnostic
	// exists to surface.
	require.NoError(t, persist(grid, base, 1.0, 0, nil, false, false))
	require.NoError(t, persist(home, base, 0.5, 0, nil, false, false))
	require.NoError(t, persist(pv, base, 0.6, 0, nil, false, false))
	require.NoError(t, persist(lp, base, 1.0, 0, nil, false, false))
	g, f := 0.30, 0.05
	require.NoError(t, PersistTariffs(base, &g, &f, nil, nil))

	chain, err := ComputeChain(context.Background(), base, base.Add(15*time.Minute), nil)
	require.NoError(t, err)

	require.Equal(t, 1, chain.MeterResidual.Slots)
	require.InDelta(t, 0.1, chain.MeterResidual.SumKWh, 1e-9)
	require.InDelta(t, 0.1, chain.MeterResidual.AbsSumKWh, 1e-9)

	found := slices.ContainsFunc(chain.Notes, func(n string) bool {
		return strings.Contains(n, "meterResidual")
	})
	require.True(t, found, "chain.Notes must point a reader at meterResidual")
}

// TestRealisedNoteDisclosesItsOwnFallbackCount: ComputeRealisedCost and the chain
// build DIFFERENT slot sets - the realised figure keeps slots the chain drops for a
// missing battery SoC (see buildLedgerSlots' includeBattery gate) - so they have
// different feed-in-fallback counts, often by several times over. Emitting only the
// chain's count discloses the realised euros against a slot set they were not computed
// on. Each figure must quote the imputation its own slot set actually used.
func TestRealisedNoteDisclosesItsOwnFallbackCount(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	bat := mustCreateEntity(t, Battery, "bat1")
	loc := time.Now().Location()
	seedBatteryCalibration(t, bat, time.Date(2026, 7, 1, 0, 0, 0, 0, loc))

	base := time.Date(2026, 8, 15, 0, 0, 0, 0, loc)
	g := 0.30
	soc := 50.0

	// both slots are priced from the fallback; only slot 0 has a battery SoC, so the
	// chain keeps one slot and the realised figure keeps two
	require.NoError(t, persist(grid, base, 2.0, 0, nil, false, false))
	require.NoError(t, persist(home, base, 2.0, 0, nil, false, false))
	require.NoError(t, persist(bat, base, 0, 0, &soc, false, false))
	require.NoError(t, PersistTariffs(base, &g, nil, nil, nil))

	slot1 := base.Add(15 * time.Minute)
	require.NoError(t, persist(grid, slot1, 2.0, 0, nil, false, false))
	require.NoError(t, persist(home, slot1, 2.0, 0, nil, false, false))
	require.NoError(t, PersistTariffs(slot1, &g, nil, nil, nil))

	seedFeedInWitnesses(t, base.Add(24*time.Hour), 0, minFeedInWitnessSlots)

	static := 0.0
	ledger, err := ComputeLedger(context.Background(), base, base.Add(30*time.Minute), &static)
	require.NoError(t, err)

	require.Equal(t, 2, ledger.Realised.Coverage.ValidSlots)
	require.NotNil(t, ledger.Chain)
	require.Equal(t, 1, ledger.Chain.Coverage.ValidSlots)

	require.Contains(t, ledger.Realised.Note, "no feed-in price was recorded for 2 of the slots behind this figure",
		"the realised figure must disclose the imputation ITS OWN slot set used, not the chain's smaller count")

	var chainNote string
	for _, n := range ledger.Chain.Notes {
		if strings.HasPrefix(n, "no feed-in price was recorded") {
			chainNote = n
		}
	}
	require.Contains(t, chainNote, "for 1 of the slots behind this figure",
		"the chain must keep quoting its own count")
}

// TestRealisedFallbackNoteSurvivesChainRefusal: a battery-physics refusal leaves Chain
// nil, and with it every note the chain carries. RealisedCost is documented as
// independent of the chain and is still returned - so if the fallback disclosure only
// lived on the chain, the one figure the ledger exists to publish would be priced with
// imputed values and say nothing about it. ErrBatteryRateCeilingUnavailable is derived
// from the battery's whole history, so a freshly commissioned battery makes this the
// DEFAULT state for every window, not an edge case.
func TestRealisedFallbackNoteSurvivesChainRefusal(t *testing.T) {
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
	require.NoError(t, persist(bat, base, 0, 0, &soc, false, false)) // no charge/discharge evidence
	g := 0.30
	require.NoError(t, PersistTariffs(base, &g, nil, nil, nil))

	seedFeedInWitnesses(t, base.Add(24*time.Hour), 0, minFeedInWitnessSlots)

	static := 0.0
	ledger, err := ComputeLedger(context.Background(), base, base.Add(15*time.Minute), &static)
	require.NoError(t, err)

	require.Nil(t, ledger.Chain)
	require.NotEmpty(t, ledger.ChainUnavailable)
	require.Equal(t, 1, ledger.Realised.Coverage.ValidSlots)
	require.Contains(t, ledger.Realised.Note, "no feed-in price was recorded for 1 of the slots behind this figure",
		"the disclosure must not disappear with the chain that used to carry it")
}
