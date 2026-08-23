package metrics

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/require"
)

// seedBatteryCalibration writes a clean charge-only run followed by a clean
// discharge-only run, both at 1.0kWh AC per 15min slot, against a true capacity of
// 10kWh and batteryEta (0.9). It exists purely so deriveBatteryPhysics has enough
// single-direction SoC evidence to derive a capacity in tests, without depending on
// the package's real accumulator/collector machinery.
func seedBatteryCalibration(t *testing.T, bat entity, start time.Time) {
	t.Helper()

	soc := 20.0
	ts := start
	require.NoError(t, persist(bat, ts, 0, 0, &soc, false, false))

	for range 3 {
		ts = ts.Add(15 * time.Minute)
		soc += 9.0 // 1.0kWh * eta(0.9) / capacity(10kWh) = 9 percentage points
		s := soc
		require.NoError(t, persist(bat, ts, 1.0, 0, &s, false, false))
	}

	for range 3 {
		ts = ts.Add(15 * time.Minute)
		soc -= 100.0 / 9.0 // (1.0kWh / eta(0.9)) / capacity(10kWh)
		s := soc
		require.NoError(t, persist(bat, ts, 0, 1.0, &s, false, false))
	}
}

func TestDeriveBatteryPhysics(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	bat := mustCreateEntity(t, Battery, "bat1")

	loc := time.Now().Location()
	seedBatteryCalibration(t, bat, time.Date(2026, 7, 1, 0, 0, 0, 0, loc))

	phys, err := deriveBatteryPhysics()
	require.NoError(t, err)

	require.InDelta(t, 10.0, phys.CapacityKWh, 1e-6)
	require.Equal(t, batteryEta, phys.EtaC)
	require.Equal(t, batteryEta, phys.EtaD)
	require.InDelta(t, 1.0, phys.MaxChargeKWh, 1e-9)
	require.InDelta(t, 1.0, phys.MaxDischargeKWh, 1e-9)
	require.NotEmpty(t, phys.CapacitySource)
}

func TestDeriveBatteryPhysicsRefusesWithoutEnoughHistory(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	bat := mustCreateEntity(t, Battery, "bat1")

	loc := time.Now().Location()
	// a single, noisy 1pp movement - well under minSocDeltaFrac, let alone
	// minCapacityEvidenceFrac
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, loc)
	soc0, soc1 := 50.0, 51.0
	require.NoError(t, persist(bat, base, 0, 0, &soc0, false, false))
	require.NoError(t, persist(bat, base.Add(15*time.Minute), 0.1, 0, &soc1, false, false))

	_, err := deriveBatteryPhysics()
	require.ErrorIs(t, err, ErrBatteryPhysicsUnavailable)
}

func TestSimulateSlotStepModes(t *testing.T) {
	phys := batteryPhysics{CapacityKWh: 10, EtaC: 0.9, EtaD: 0.9, FloorFrac: 0, MaxChargeKWh: 100, MaxDischargeKWh: 100}

	t.Run("hold never moves energy", func(t *testing.T) {
		soc, flow, charge, discharge := simulateSlotStep(batteryModeHold, 2, 5, 5, phys)
		require.InDelta(t, 5, soc, 1e-9)
		require.InDelta(t, 0, flow.ImportKWh, 1e-9)
		require.InDelta(t, 3, flow.ExportKWh, 1e-9) // 5-2 surplus goes straight to export
		require.Zero(t, charge)
		require.Zero(t, discharge)
	})

	t.Run("normal charges from surplus only", func(t *testing.T) {
		soc, flow, charge, discharge := simulateSlotStep(batteryModeNormal, 1, 3, 5, phys)
		require.InDelta(t, 5+2*0.9, soc, 1e-9) // 2kWh surplus, all absorbed (headroom is 5kWh)
		require.InDelta(t, 0, flow.ImportKWh, 1e-9)
		require.InDelta(t, 0, flow.ExportKWh, 1e-9)
		require.InDelta(t, 2, charge, 1e-9)
		require.Zero(t, discharge)
	})

	t.Run("normal discharges to cover a deficit only", func(t *testing.T) {
		soc, flow, charge, discharge := simulateSlotStep(batteryModeNormal, 4, 1, 5, phys)
		// deficit 3kWh, available 5kWh*0.9=4.5kWh AC deliverable, so fully covered
		require.InDelta(t, 5-3/0.9, soc, 1e-9)
		require.InDelta(t, 0, flow.ImportKWh, 1e-9)
		require.Zero(t, charge)
		require.InDelta(t, 3, discharge, 1e-9)
	})

	t.Run("charge forces grid import beyond surplus", func(t *testing.T) {
		soc, flow, charge, discharge := simulateSlotStep(batteryModeCharge, 1, 0, 0, phys)
		// no surplus at all, but charge mode still fills headroom (10kWh) from grid -
		// the AC-side charge is headroom/eta so that, after eta, the DC store lands
		// exactly at capacity; the 1kWh deficit is still bought separately
		wantChargeAC := 10.0 / phys.EtaC
		require.InDelta(t, 10.0, soc, 1e-9)
		require.InDelta(t, 1+wantChargeAC, flow.ImportKWh, 1e-6)
		require.InDelta(t, 0, flow.ExportKWh, 1e-9)
		require.InDelta(t, wantChargeAC, charge, 1e-6)
		require.Zero(t, discharge)
	})

	t.Run("holdcharge never draws from the grid", func(t *testing.T) {
		_, flow, charge, discharge := simulateSlotStep(batteryModeHoldCharge, 1, 0, 0, phys)
		require.InDelta(t, 1, flow.ImportKWh, 1e-9) // the deficit is bought, nothing more
		require.Zero(t, charge)
		require.Zero(t, discharge)
	})
}

// TestComputeW2DailyReanchor covers ADR-011 rule 5: the counterfactual battery resets
// to the measured SoC at the start of each calendar day. A free-running simulation
// that carried day 1's simulated (near-full) ending SoC into day 2 would discharge
// freely against a 2kWh deficit and show near-zero grid import; re-anchoring to a
// measured, nearly-empty SoC instead forces almost all of it to be bought.
func TestComputeW2DailyReanchor(t *testing.T) {
	phys := batteryPhysics{CapacityKWh: 10, EtaC: 0.9, EtaD: 0.9, FloorFrac: 0, MaxChargeKWh: 100, MaxDischargeKWh: 100}

	loc := time.Now().Location()
	day1 := time.Date(2026, 8, 4, 23, 0, 0, 0, loc)
	day2 := time.Date(2026, 8, 5, 0, 0, 0, 0, loc)

	highSoc := 0.5 // day 1's own measured SoC - simulation still charges further from here
	lowSoc := 0.05 // day 2's measured SoC is nearly empty

	slots := []slotData{
		{Start: day1, HomeKWh: 0, PVKWh: 5, BatterySocFrac: &highSoc, PriceGrid: 0.30, PriceFeedIn: 0.05},
		{Start: day2, HomeKWh: 2, PVKWh: 0, BatterySocFrac: &lowSoc, PriceGrid: 0.30, PriceFeedIn: 0.05},
	}

	flows, err := computeW2(slots, phys)
	require.NoError(t, err)
	require.Len(t, flows, 2)

	// re-anchored: soc starts day 2 at 0.5kWh, delivering at most 0.45kWh AC, leaving
	// 1.55kWh of the 2kWh deficit to be bought. Without the re-anchor, day 1's
	// simulated ending SoC (~9.5kWh) would cover the whole deficit and import would
	// be 0.
	require.InDelta(t, 2-0.5*phys.EtaD, flows[1].ImportKWh, 1e-9)
}

func TestComputeW2RefusesOnMissingSoc(t *testing.T) {
	phys := batteryPhysics{CapacityKWh: 10, EtaC: 0.9, EtaD: 0.9}

	slots := []slotData{
		{Start: time.Now(), HomeKWh: 1, PVKWh: 0, BatterySocFrac: nil, PriceGrid: 0.3, PriceFeedIn: 0.05},
	}

	_, err := computeW2(slots, phys)
	require.ErrorIs(t, err, ErrSocGap)
}

// TestChainContributionsSumToWhole is ADR-011's central defence: no matter how the
// chain is decomposed, the parts must sum to the whole under both settlement modes.
func TestChainContributionsSumToWhole(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	pv := mustCreateEntity(t, PV, "pv1")
	bat := mustCreateEntity(t, Battery, "bat1")

	loc := time.Now().Location()
	seedBatteryCalibration(t, bat, time.Date(2026, 7, 1, 0, 0, 0, 0, loc))

	base := time.Date(2026, 8, 1, 0, 0, 0, 0, loc)

	type seed struct {
		home, pv, gridImport, gridExport, soc, priceGrid, priceFeedIn float64
	}
	seeds := []seed{
		{home: 2.0, pv: 0.0, gridImport: 2.0, gridExport: 0.0, soc: 50, priceGrid: 0.30, priceFeedIn: 0.08},
		{home: 0.5, pv: 3.0, gridImport: 0.0, gridExport: 1.0, soc: 55, priceGrid: 0.25, priceFeedIn: 0.07},
		{home: 1.5, pv: 0.2, gridImport: 0.8, gridExport: 0.0, soc: 60, priceGrid: 0.35, priceFeedIn: 0.06},
		{home: 0.8, pv: 0.8, gridImport: 0.1, gridExport: 0.1, soc: 58, priceGrid: 0.20, priceFeedIn: 0.05},
	}

	for i, s := range seeds {
		ts := base.Add(time.Duration(i) * 15 * time.Minute)
		require.NoError(t, persist(grid, ts, s.gridImport, s.gridExport, nil, false, false))
		require.NoError(t, persist(home, ts, s.home, 0, nil, false, false))
		require.NoError(t, persist(pv, ts, s.pv, 0, nil, false, false))
		soc := s.soc
		require.NoError(t, persist(bat, ts, 0, 0, &soc, false, false))
		g, f := s.priceGrid, s.priceFeedIn
		require.NoError(t, PersistTariffs(ts, &g, &f, nil, nil))
	}

	from := base
	to := base.Add(time.Duration(len(seeds)) * 15 * time.Minute)

	chain, err := ComputeChain(from, to)
	require.NoError(t, err)
	require.Len(t, chain.Worlds, 4)
	require.Len(t, chain.Contributions, 3)
	require.NotNil(t, chain.BatteryPhysics)
	require.NotNil(t, chain.Control)

	var sumPerSlot, sumAvg float64
	for _, c := range chain.Contributions {
		sumPerSlot += c.Settled.PerSlot
		sumAvg += c.Settled.PeriodAverage
	}

	expectedPerSlot := chain.Worlds[0].Settled.PerSlot - chain.Worlds[3].Settled.PerSlot
	expectedAvg := chain.Worlds[0].Settled.PeriodAverage - chain.Worlds[3].Settled.PeriodAverage

	require.InDelta(t, expectedPerSlot, sumPerSlot, 1e-9)
	require.InDelta(t, expectedAvg, sumAvg, 1e-9)
}

// TestChainReportsLossWithoutClamp covers ADR-011 rule 1: a period that performed
// worse than the baseline reports a negative contribution, not zero.
func TestChainReportsLossWithoutClamp(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)

	loc := time.Now().Location()
	base := time.Date(2026, 8, 2, 0, 0, 0, 0, loc)

	// no PV, no battery configured: W1=W2=W0, so any gap between W0 and actual
	// lands entirely on the "Control" contribution. The real system bought far more
	// than the load needed (synthetic worst case, not meant to be physically
	// plausible) - the ledger must say so plainly.
	require.NoError(t, persist(grid, base, 5.0, 0, nil, false, false))
	require.NoError(t, persist(home, base, 1.0, 0, nil, false, false))
	g, f := 0.30, 0.05
	require.NoError(t, PersistTariffs(base, &g, &f, nil, nil))

	chain, err := ComputeChain(base, base.Add(15*time.Minute))
	require.NoError(t, err)

	require.InDelta(t, 0.30, chain.Worlds[0].Settled.PerSlot, 1e-9)
	require.InDelta(t, 1.50, chain.Worlds[3].Settled.PerSlot, 1e-9)

	total := chain.Worlds[0].Settled.PerSlot - chain.Worlds[3].Settled.PerSlot
	require.Less(t, total, 0.0)

	control := chain.Contributions[2]
	require.InDelta(t, total, control.Settled.PerSlot, 1e-9)
	require.Less(t, control.Settled.PerSlot, 0.0, "a loss-making period must be reported as a loss, not clamped to zero")
}
