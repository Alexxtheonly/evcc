package metrics

import (
	"context"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/require"
)

// seedBatteryCalibration writes a clean charge-only run followed by a clean
// discharge-only run, both at 1.0kWh AC per 15min slot, against a true capacity of
// 10kWh and BatteryEta (0.9). It exists purely so deriveBatteryPhysics has enough
// single-direction SoC evidence to derive a capacity in tests, without depending on
// the package's real accumulator/collector machinery.
//
// Each row's SocTemp is the SoC at the START of that row's own slot - matching the
// production writer (see meter.SocTemp's doc comment in db.go, and
// Accumulator.setSocTemp) - so a row's own Energy/ReturnEnergy is what causes the NEXT
// row's SoC, not its own. A trailing zero-energy marker row supplies the final SoC
// reading so the last discharge slot has a "next" delta to be derived from.
func seedBatteryCalibration(t *testing.T, bat entity, start time.Time) {
	t.Helper()

	soc := 20.0
	ts := start

	for range 3 {
		s := soc
		require.NoError(t, persist(bat, ts, 1.0, 0, &s, false, false))
		soc += 9.0 // 1.0kWh * eta(0.9) / capacity(10kWh) = 9 percentage points
		ts = ts.Add(15 * time.Minute)
	}

	for range 3 {
		s := soc
		require.NoError(t, persist(bat, ts, 0, 1.0, &s, false, false))
		soc -= 100.0 / 9.0 // (1.0kWh / eta(0.9)) / capacity(10kWh)
		ts = ts.Add(15 * time.Minute)
	}

	s := soc
	require.NoError(t, persist(bat, ts, 0, 0, &s, false, false))
}

func TestDeriveBatteryPhysics(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	bat := mustCreateEntity(t, Battery, "bat1")

	loc := time.Now().Location()
	seedBatteryCalibration(t, bat, time.Date(2026, 7, 1, 0, 0, 0, 0, loc))

	phys, err := deriveBatteryPhysics(context.Background())
	require.NoError(t, err)

	require.InDelta(t, 10.0, phys.CapacityKWh, 1e-6)
	require.Equal(t, BatteryEta, phys.EtaC)
	require.Equal(t, BatteryEta, phys.EtaD)
	require.InDelta(t, 1.0, phys.MaxChargeKWh, 1e-9)
	require.InDelta(t, 1.0, phys.MaxDischargeKWh, 1e-9)
	require.NotEmpty(t, phys.CapacitySource)
}

// TestDeriveBatteryPhysicsCacheIsKeyedToTheDatabase covers the CACHED entry point -
// every other test here calls deriveBatteryPhysicsUncached or happens to run first, so
// the cache itself had no coverage at all. util.Cached has no cache key of its own, so
// the db.Instance a value was derived from is tracked alongside it; without that, the
// second site here would be served the first one's battery for five minutes.
func TestDeriveBatteryPhysicsCacheIsKeyedToTheDatabase(t *testing.T) {
	loc := time.Now().Location()
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, loc)

	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	seedBatteryCalibration(t, mustCreateEntity(t, Battery, "bat1"), start)

	first, err := deriveBatteryPhysics(context.Background())
	require.NoError(t, err)
	require.InDelta(t, 10.0, first.CapacityKWh, 1e-6)

	// same database, immediately: served from the cache, same answer
	again, err := deriveBatteryPhysics(context.Background())
	require.NoError(t, err)
	require.Equal(t, first, again)

	// a different database with a different battery, well inside the TTL
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	bat := mustCreateEntity(t, Battery, "bat1")
	capacity := 42.0
	require.NoError(t, db.Instance.Model(new(entity)).Where("id = ?", bat.Id).Update("capacity_kwh", capacity).Error)
	seedBatteryCalibration(t, bat, start)

	second, err := deriveBatteryPhysics(context.Background())
	require.NoError(t, err)
	require.InDelta(t, 42.0, second.CapacityKWh, 1e-6, "the previous database's battery must not be served to this one")
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

	_, err := deriveBatteryPhysics(context.Background())
	require.ErrorIs(t, err, ErrBatteryPhysicsUnavailable)
}

// TestPercentileIgnoresSingleOutlier covers the Priority-4 fix directly: a single
// glitched slot must not become the p99 rate ceiling the way it would a raw max().
func TestPercentileIgnoresSingleOutlier(t *testing.T) {
	values := make([]float64, 0, 100)
	for range 99 {
		values = append(values, 2.0) // a normal, physically plausible charge rate
	}
	values = append(values, 40.0) // one glitched/grid-forced slot

	require.InDelta(t, 2.0, Percentile(values, rateLimitPercentile), 1e-9,
		"the p99 ceiling must come from the 99 normal readings, not the single outlier")
	require.InDelta(t, 40.0, Percentile(values, 1.0), 1e-9, "p100 (the max) should still surface the outlier")
}

// TestDeriveBatteryPhysicsRateLimitIgnoresOutlier is the same fix at the
// deriveBatteryPhysics level: MaxChargeKWh used to be max() over all history, so one
// glitched slot (a meter spike, or a one-off grid-forced test charge) collapsed the
// rate ceiling to whatever that single slot happened to be - unphysically letting the
// model "charge" from empty to full in a single 15-minute slot, and always in the
// direction that flatters the real controller's hindsight comparisons. Capacity is
// persisted directly so this test doesn't also need to satisfy the derivation
// fallback's evidence requirements - MaxChargeKWh is computed from the raw per-row
// readings regardless of capacity source.
func TestDeriveBatteryPhysicsRateLimitIgnoresOutlier(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	bat := mustCreateEntity(t, Battery, "bat1")
	require.NoError(t, bat.updateCapacity(10.0))

	loc := time.Now().Location()
	ts := time.Date(2026, 7, 1, 0, 0, 0, 0, loc)
	for range 100 {
		require.NoError(t, persist(bat, ts, 1.0, 0, nil, false, false))
		ts = ts.Add(15 * time.Minute)
	}
	require.NoError(t, persist(bat, ts, 40.0, 0, nil, false, false)) // one glitched slot

	phys, err := deriveBatteryPhysics(context.Background())
	require.NoError(t, err)
	require.Less(t, phys.MaxChargeKWh, 5.0, "one glitched 40kWh slot must not become the rate ceiling")
}

// TestDeriveBatteryPhysicsPrefersPersistedCapacity covers the Priority-2 decision: a
// device-reported capacity (persisted via Collector.SetCapacity, mirroring what
// core/site.go does with api.BatteryCapacity) must win over derivation, even when the
// battery's history alone would derive a different number - a hardware fact evcc
// already knows beats an estimate.
func TestDeriveBatteryPhysicsPrefersPersistedCapacity(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	bat := mustCreateEntity(t, Battery, "bat1")
	loc := time.Now().Location()
	seedBatteryCalibration(t, bat, time.Date(2026, 7, 1, 0, 0, 0, 0, loc)) // would derive 10.0kWh

	require.NoError(t, bat.updateCapacity(13.5))

	phys, err := deriveBatteryPhysics(context.Background())
	require.NoError(t, err)
	require.InDelta(t, 13.5, phys.CapacityKWh, 1e-9)
	require.Contains(t, phys.CapacitySource, "device-reported")
}

// TestBatteryHistoryRowsIncludesPreMigrationNullRows covers the NULL-handling fix:
// recovered/incomplete were added by AutoMigrate with no DEFAULT, so every
// pre-migration meters row has them NULL. A plain "recovered = false" comparison
// excludes a NULL via SQL's three-valued logic, which would have silently discarded a
// site's entire pre-migration battery history from the capacity derivation.
func TestBatteryHistoryRowsIncludesPreMigrationNullRows(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	bat := mustCreateEntity(t, Battery, "bat1")

	// bypass persist() (which always writes explicit 0/1) to simulate a genuine
	// pre-migration row with NULL recovered/incomplete columns.
	require.NoError(t, db.Instance.Exec(
		"INSERT INTO meters (meter, ts, energy, return_energy, soc_temp, recovered, incomplete) VALUES (?, ?, ?, ?, ?, NULL, NULL)",
		bat.Id, time.Now().Unix(), 1.5, 0.0, 42.0,
	).Error)

	rows, err := batteryHistoryRows(context.Background(), []int{bat.Id})
	require.NoError(t, err)
	require.Len(t, rows, 1, "a NULL recovered/incomplete row must be treated as valid data, not silently dropped")
	require.InDelta(t, 1.5, rows[0].ChargeKWh, 1e-9)
}

// TestDeriveBatteryCapacityFromHistoryRampThenStop is the alignment-bug regression:
// soc_temp is recorded at slot START, so a ΔSoC between two consecutive readings was
// caused by the EARLIER row's energy. A charge ramp (two slots of unequal power)
// immediately followed by a stop (zero energy, flat SoC) is exactly the shape that
// breaks the off-by-one pairing - it would attribute the smaller slot's tiny SoC
// movement to the larger slot's energy, and silently drop the transition the larger
// slot actually caused because the following (stopped) row shows zero energy.
func TestDeriveBatteryCapacityFromHistoryRampThenStop(t *testing.T) {
	const capacity = 10.0 // true capacity, kWh

	// row0: 0.5kWh charge -> causes a 4.5pp rise by row1 (0.5*0.9/10)
	// row1: 2.5kWh charge -> causes a 22.5pp rise by row2 (2.5*0.9/10)
	// row2: stop, no further movement
	soc0 := 0.20
	soc1 := soc0 + 0.5*BatteryEta/capacity
	soc2 := soc1 + 2.5*BatteryEta/capacity

	base := time.Unix(1_700_000_000, 0)
	rows := []batteryHistoryRow{
		{Ts: base.Unix(), ChargeKWh: 0.5, SocFrac: &soc0},
		{Ts: base.Add(15 * time.Minute).Unix(), ChargeKWh: 2.5, SocFrac: &soc1},
		{Ts: base.Add(30 * time.Minute).Unix(), SocFrac: &soc2},
	}

	got, source, err := deriveBatteryCapacityFromHistory(rows)
	require.NoError(t, err)
	require.Contains(t, source, "fallback")
	// the off-by-one pairing (cur's energy instead of prev's) would attribute row1's
	// 2.5kWh to row0's 4.5pp delta alone (dropping row1's own much larger 22.5pp
	// delta entirely, since row2 shows zero energy), deriving 2.5*0.9/0.045 = 50kWh -
	// 5x the true capacity. The fix must land within rounding of the true 10kWh.
	require.InDelta(t, capacity, got, 1e-6)
}

// TestDeriveBatteryCapacityFromHistoryRefusesOnDisagreement covers the "don't average
// two numbers that don't agree" fix: a charge-derived and discharge-derived estimate
// more than capacityDisagreementFrac apart must refuse, not blend into a number
// neither side supports (e.g. averaging 10kWh and 20kWh into a fabricated 15kWh).
func TestDeriveBatteryCapacityFromHistoryRefusesOnDisagreement(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)

	// 3 charge windows of 1.0kWh each imply capacity 10 (chargeSocSum 0.27, over the
	// 0.20 evidence floor). 4 discharge windows of 1.0kWh each imply capacity 20
	// (dischargeSocSum ~0.222, also over the floor) - same energy magnitude, opposite
	// direction, evidently the same battery, and yet an honest 2x disagreement.
	soc := []float64{0.20}
	for range 3 {
		soc = append(soc, soc[len(soc)-1]+1.0*BatteryEta/10.0)
	}
	for range 4 {
		soc = append(soc, soc[len(soc)-1]-1.0/BatteryEta/20.0)
	}

	rows := make([]batteryHistoryRow, len(soc))
	for i := range soc {
		rows[i] = batteryHistoryRow{Ts: base.Add(time.Duration(i) * 15 * time.Minute).Unix(), SocFrac: &soc[i]}
		switch {
		case i < 3:
			rows[i].ChargeKWh = 1.0
		case i < 7:
			rows[i].DischargeKWh = 1.0
		}
	}

	_, _, err := deriveBatteryCapacityFromHistory(rows)
	require.ErrorIs(t, err, ErrBatteryPhysicsUnavailable)
}

func TestSimulateSlotStepModes(t *testing.T) {
	phys := batteryPhysics{CapacityKWh: 10, EtaC: 0.9, EtaD: 0.9, FloorFrac: 0, MaxChargeKWh: 100, MaxDischargeKWh: 100}

	t.Run("hold never moves energy", func(t *testing.T) {
		soc, flow, charge, discharge, ok := simulateSlotStep(batteryModeHold, 2, 5, 5, phys)
		require.True(t, ok)
		require.InDelta(t, 5, soc, 1e-9)
		require.InDelta(t, 0, flow.ImportKWh, 1e-9)
		require.InDelta(t, 3, flow.ExportKWh, 1e-9) // 5-2 surplus goes straight to export
		require.Zero(t, charge)
		require.Zero(t, discharge)
	})

	t.Run("normal charges from surplus only", func(t *testing.T) {
		soc, flow, charge, discharge, ok := simulateSlotStep(batteryModeNormal, 1, 3, 5, phys)
		require.True(t, ok)
		require.InDelta(t, 5+2*0.9, soc, 1e-9) // 2kWh surplus, all absorbed (headroom is 5kWh)
		require.InDelta(t, 0, flow.ImportKWh, 1e-9)
		require.InDelta(t, 0, flow.ExportKWh, 1e-9)
		require.InDelta(t, 2, charge, 1e-9)
		require.Zero(t, discharge)
	})

	t.Run("normal discharges to cover a deficit only", func(t *testing.T) {
		soc, flow, charge, discharge, ok := simulateSlotStep(batteryModeNormal, 4, 1, 5, phys)
		require.True(t, ok)
		// deficit 3kWh, available 5kWh*0.9=4.5kWh AC deliverable, so fully covered
		require.InDelta(t, 5-3/0.9, soc, 1e-9)
		require.InDelta(t, 0, flow.ImportKWh, 1e-9)
		require.Zero(t, charge)
		require.InDelta(t, 3, discharge, 1e-9)
	})

	t.Run("charge forces grid import beyond surplus", func(t *testing.T) {
		soc, flow, charge, discharge, ok := simulateSlotStep(batteryModeCharge, 1, 0, 0, phys)
		require.True(t, ok)
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
		_, flow, charge, discharge, ok := simulateSlotStep(batteryModeHoldCharge, 1, 0, 0, phys)
		require.True(t, ok)
		require.InDelta(t, 1, flow.ImportKWh, 1e-9) // the deficit is bought, nothing more
		require.Zero(t, charge)
		require.Zero(t, discharge)
	})

	// an unrecognised mode used to land in the same branch as normal and be priced as
	// if it were normal. Two DIFFERENT unrecognised modes therefore produced identical
	// flows and a delta of exactly zero - "we do not understand this decision" rendered
	// as "this decision cost nothing". Refusing is the only honest answer.
	t.Run("an unmodelled mode is refused, not replayed as normal", func(t *testing.T) {
		soc, flow, charge, discharge, ok := simulateSlotStep("supercharge", 1, 3, 5, phys)
		require.False(t, ok)
		require.Zero(t, soc)
		require.Equal(t, worldFlow{}, flow)
		require.Zero(t, charge)
		require.Zero(t, discharge)

		// and it must NOT coincide with what normal would have produced, which is
		// exactly how the old default branch hid itself. An empty pack with a 4kWh
		// deficit is the clearest separator: normal buys the deficit, the refusal
		// returns a zero flow that must never reach settleFlows.
		_, normalFlow, _, _, normalOk := simulateSlotStep(batteryModeNormal, 4, 0, 0, phys)
		require.True(t, normalOk)
		require.InDelta(t, 4, normalFlow.ImportKWh, 1e-9)

		_, refusedFlow, _, _, refusedOk := simulateSlotStep("supercharge", 4, 0, 0, phys)
		require.False(t, refusedOk)
		require.NotEqual(t, normalFlow, refusedFlow)
	})

	// callers fold ""/"unknown" to normal before calling (see effectiveMode); this
	// function itself models the four real modes and nothing else
	t.Run("the absence spellings are the callers' job to fold, not this function's", func(t *testing.T) {
		for _, mode := range []string{"", batteryModeUnknown} {
			_, _, _, _, ok := simulateSlotStep(mode, 1, 3, 5, phys)
			require.False(t, ok, mode)
		}
		require.Equal(t, batteryModeNormal, effectiveMode(""))
		require.Equal(t, batteryModeNormal, effectiveMode(batteryModeUnknown))
	})
}

// TestComputeW2DoesNotResetAtCalendarBoundary is the narrowest test of the N1 fix's
// first half: a calendar-day boundary is not a measurement event, so crossing midnight
// must not reset the simulated SoC to whatever the real battery happened to hold.
//
// Both slots are contiguous (23:45 -> 00:00), so the only thing that could reset the
// simulation here is the calendar. Day 1 charges the counterfactual to 9.5kWh; the
// measured SoC at 00:00 is nearly empty (0.5kWh) because the REAL controller ran the
// pack down overnight, which is exactly the divergence the counterfactual exists to
// express. Resetting to it handed W2 the controller's own state for free: the 2kWh
// deficit then cost 1.55kWh of grid import instead of nothing.
func TestComputeW2DoesNotResetAtCalendarBoundary(t *testing.T) {
	phys := batteryPhysics{CapacityKWh: 10, EtaC: 0.9, EtaD: 0.9, FloorFrac: 0, MaxChargeKWh: 100, MaxDischargeKWh: 100}

	loc := time.Now().Location()
	beforeMidnight := time.Date(2026, 8, 4, 23, 45, 0, 0, loc)
	afterMidnight := beforeMidnight.Add(15 * time.Minute)

	highSoc := 0.5 // day 1's own measured SoC - simulation still charges further from here
	lowSoc := 0.05 // the real battery is nearly empty by 00:00

	slots := []slotData{
		{Start: beforeMidnight, HomeKWh: 0, PVKWh: 5, BatterySocFrac: &highSoc, PriceGrid: 0.30, PriceFeedIn: 0.05},
		{Start: afterMidnight, HomeKWh: 2, PVKWh: 0, BatterySocFrac: &lowSoc, PriceGrid: 0.30, PriceFeedIn: 0.05},
	}

	flows, drift, err := computeW2(slots, phys)
	require.NoError(t, err)
	require.Len(t, flows, 2)

	// slot 0 charges 5kWh of surplus into the pack at EtaC: 5 + 4.5 = 9.5kWh carried
	// into slot 1, which covers the whole 2kWh deficit. With the old calendar reset
	// this was 2 - 0.5*EtaD = 1.55kWh of import.
	require.InDelta(t, 0.0, flows[1].ImportKWh, 1e-9)
	require.Zero(t, drift.Gaps)
	require.Zero(t, drift.CarriedKWh)
}

// TestComputeW2CarriesMeasuredMovementAcrossGap is the N1 fix's second half: at a gap
// the counterfactual is handed the measured pack's OWN movement across the unmeasured
// stretch - not the measured pack's SoC. Those slots are excluded from W3 too, but W3 is
// a meter reading and so receives that energy anyway (its post-gap import is lower
// because the real pack was charged during hours nobody priced); giving W2 the same
// movement keeps the two comparable, while resetting to the measured level would also
// wipe out the simulation's own divergence.
//
// The fixture separates all three candidate behaviours. Simulated SoC entering the gap
// is 5.5kWh (1.0 measured + 5kWh surplus at EtaC), measured moves 1.0 -> 3.0kWh across
// it, and the slot after the gap has an 8kWh deficit:
//
//	carry (this fix): 5.5 + 2.0 = 7.5kWh -> 6.75kWh delivered -> 1.25kWh imported
//	reset (before):   3.0kWh          -> 2.70kWh delivered -> 5.30kWh imported
//	free-running:     5.5kWh          -> 4.95kWh delivered -> 3.05kWh imported
func TestComputeW2CarriesMeasuredMovementAcrossGap(t *testing.T) {
	phys := batteryPhysics{CapacityKWh: 10, EtaC: 0.9, EtaD: 0.9, FloorFrac: 0, MaxChargeKWh: 100, MaxDischargeKWh: 100}

	loc := time.Now().Location()
	slot0 := time.Date(2026, 8, 10, 9, 0, 0, 0, loc)
	slot1 := slot0.Add(30 * time.Minute) // the 09:15 slot was dropped upstream - not contiguous

	socAtSlot0 := 0.10 // measured, 1.0kWh
	socAtSlot1 := 0.30 // measured, 3.0kWh - the real pack gained 2kWh while unmeasured

	slots := []slotData{
		{Start: slot0, HomeKWh: 0, PVKWh: 5.0, BatterySocFrac: &socAtSlot0, PriceGrid: 0.30, PriceFeedIn: 0.05},
		{Start: slot1, HomeKWh: 8.0, PVKWh: 0, BatterySocFrac: &socAtSlot1, PriceGrid: 0.30, PriceFeedIn: 0.05},
	}

	flows, drift, err := computeW2(slots, phys)
	require.NoError(t, err)
	require.Len(t, flows, 2)

	require.InDelta(t, 1.25, flows[1].ImportKWh, 1e-9)
	require.Equal(t, 1, drift.Gaps)
	require.InDelta(t, 2.0, drift.CarriedKWh, 1e-9, "exactly the measured pack's movement across the gap, nothing else")
}

// TestComputeW2BoundsCarriedStateToFloorAndCapacity pins the min(max(...)) on the
// gap carry. It is the one part of the carry that can absorb energy silently: a carry
// that would drive the simulated pack below its floor or above its capacity is clamped,
// and whatever the clamp absorbs is what W2 never has to buy.
//
// The fixture needs THREE gaps because the first one's carry lands inside
// [floor, capacity] and so pins nothing - gap two drives it below the floor
// (simulated 1.22kWh, carry -2.17kWh) and gap three above capacity (0.5 + 9.6kWh), so
// both directions are exercised. Unbounded, the three carries would be -2.583, -3.194
// and +9.600 (sum +3.823); bounded at [0.5, 10] they are -2.583, -0.720 and +9.500.
//
// Ported verbatim from the LEDGER_DB harness's TestW2VariantMatchesProduction, which
// asserted this against production computeW2 alongside a parameterised twin of it. The
// twin and its sync test are gone; this half was real coverage and is kept.
func TestComputeW2BoundsCarriedStateToFloorAndCapacity(t *testing.T) {
	phys := batteryPhysics{CapacityKWh: 10, EtaC: 0.9, EtaD: 0.9, FloorFrac: 0.05, MaxChargeKWh: 2, MaxDischargeKWh: 2}

	loc := time.Now().Location()
	base := time.Date(2026, 8, 4, 23, 30, 0, 0, loc)
	socs := []float64{0.40, 0.55, 0.50, 0.20, 0.30, 0.02, 0.98}
	starts := []time.Time{base, base.Add(15 * time.Minute), base.Add(30 * time.Minute), base.Add(90 * time.Minute), base.Add(105 * time.Minute), base.Add(165 * time.Minute), base.Add(225 * time.Minute)}
	loads := []float64{0.5, 0, 1.5, 3.0, 0.2, 0, 0}
	pvs := []float64{0, 2.5, 0, 0, 1.0, 0, 0}

	slots := make([]slotData, len(socs))
	for i := range socs {
		soc := socs[i]
		slots[i] = slotData{
			Start: starts[i], HomeKWh: loads[i], PVKWh: pvs[i], BatterySocFrac: &soc,
			BatteryChargeKWh: pvs[i] / 2, BatteryDischargeKWh: loads[i] / 4,
			PriceGrid: 0.30, PriceFeedIn: 0.05,
		}
	}

	_, drift, err := computeW2(slots, phys)
	require.NoError(t, err)

	require.Equal(t, 3, drift.Gaps)
	require.InDelta(t, 6.197, drift.CarriedKWh, 1e-3, "unbounded this is +3.823 - the bound must still bind in both directions")
}

func TestComputeW2RefusesOnMissingSoc(t *testing.T) {
	phys := batteryPhysics{CapacityKWh: 10, EtaC: 0.9, EtaD: 0.9}

	slots := []slotData{
		{Start: time.Now(), HomeKWh: 1, PVKWh: 0, BatterySocFrac: nil, PriceGrid: 0.3, PriceFeedIn: 0.05},
	}

	_, _, err := computeW2(slots, phys)
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

	chain, err := ComputeChain(context.Background(), from, to, nil)
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

// TestChainOraclePerSlotEuros is the check TestChainContributionsSumToWhole cannot
// be: that test's Σ(W_{k-1}-W_k) == W0-W3 assertion is a telescoping identity, true
// of any four numbers whether or not a single world is computed correctly - it would
// have passed throughout the feed-in binding bug this test was written to catch (see
// TestQueryTariffSlotsBindsFeedIn). This asserts each world's absolute PerSlot euro
// figure, computed by hand below, and is deliberately built on top of the same
// four-slot fixture plus a loadpoint (so W0/W1/W2 have EV load to price) and a
// battery physics setup pinned to round numbers (FloorFrac 0, capacity 10kWh from
// seedBatteryCalibration, rate ceilings raised above anything this fixture's flows
// ever need) so the counterfactual battery's arithmetic is checkable by hand too.
//
// Hand computation (home/pv/import/export/price in kWh and EUR/kWh):
//
//	slot  home  lp   pv   grid-imp  grid-exp  p_grid  p_feedin
//	0     2.0   0.0  0.0  2.0       0.0       0.30    0.08
//	1     0.5   0.0  3.0  0.0       1.0       0.25    0.07
//	2     1.5   1.0  0.2  0.8       0.0       0.35    0.06
//	3     0.8   0.0  0.8  0.1       0.1       0.20    0.05
//
// W0 (no PV, no battery): import = home+lp every slot.
//
//	0.30*2.0 + 0.25*0.5 + 0.35*2.5 + 0.20*0.8 = 0.60+0.125+0.875+0.16 = 1.76
//
// W1 (PV self-consumption only): slot0 import2.0, slot1 export2.5, slot2
// import2.3 (load2.5-pv0.2), slot3 balanced (load0.8==pv0.8).
//
//	0.30*2.0 - 0.07*2.5 + 0.35*2.3 - 0 = 0.60-0.175+0.805+0 = 1.23
//
// W2 (dumb-rule battery, floor 0, capacity 10kWh, starts at measured 50% = 5.0kWh,
// rate ceilings non-binding): slot0 discharges the full 2.0kWh deficit (available
// 5.0*0.9=4.5kWh headroom), landing at 5.0-2.0/0.9=2.7778kWh; slot1 charges the full
// 2.5kWh surplus, landing at 2.7778+2.5*0.9=5.0278kWh; slot2 discharges the full
// 2.3kWh deficit, landing at 5.0278-2.3/0.9=2.4722kWh; slot3's load and PV are
// exactly equal (0.8==0.8), so neither charges nor discharges. Every slot's grid
// flow is therefore exactly zero:
//
//	W2 PerSlot = 0
//
// W3 (actual, measured grid meter):
//
//	0.30*2.0 - 0.07*1.0 + 0.35*0.8 - 0 + (0.20*0.1 - 0.05*0.1) = 0.60-0.07+0.28+0.015 = 0.825
func TestChainOraclePerSlotEuros(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	pv := mustCreateEntity(t, PV, "pv1")
	bat := mustCreateEntity(t, Battery, "bat1")
	lp := mustCreateEntity(t, Loadpoint, "lp-1")

	loc := time.Now().Location()
	seedBatteryCalibration(t, bat, time.Date(2026, 7, 1, 0, 0, 0, 0, loc))

	// force FloorFrac to exactly 0: an isolated history row (far from any other
	// timestamp, so it never enters a contiguous charge/discharge window) with the
	// lowest SoC on record.
	zero := 0.0
	require.NoError(t, persist(bat, time.Date(2026, 6, 1, 0, 0, 0, 0, loc), 0, 0, &zero, false, false))

	// raise MaxChargeKWh/MaxDischargeKWh (p99 of observed per-slot energy) well above
	// anything this fixture's flows ever ask for, so W2's rate ceiling never binds and
	// the hand computation above doesn't have to account for it. Isolated timestamps,
	// no SoC reading, so they're excluded from both the capacity-window and
	// FloorFrac(haveSoc) calculations - see batteryHistoryRows/deriveBatteryCapacityFromHistory.
	require.NoError(t, persist(bat, time.Date(2026, 6, 2, 0, 0, 0, 0, loc), 3.5, 0, nil, false, false))
	require.NoError(t, persist(bat, time.Date(2026, 6, 3, 0, 0, 0, 0, loc), 0, 3.5, nil, false, false))

	base := time.Date(2026, 8, 1, 0, 0, 0, 0, loc)

	type seed struct {
		home, lp, pv, gridImport, gridExport, soc, priceGrid, priceFeedIn float64
	}
	seeds := []seed{
		{home: 2.0, lp: 0.0, pv: 0.0, gridImport: 2.0, gridExport: 0.0, soc: 50, priceGrid: 0.30, priceFeedIn: 0.08},
		{home: 0.5, lp: 0.0, pv: 3.0, gridImport: 0.0, gridExport: 1.0, soc: 55, priceGrid: 0.25, priceFeedIn: 0.07},
		{home: 1.5, lp: 1.0, pv: 0.2, gridImport: 0.8, gridExport: 0.0, soc: 60, priceGrid: 0.35, priceFeedIn: 0.06},
		{home: 0.8, lp: 0.0, pv: 0.8, gridImport: 0.1, gridExport: 0.1, soc: 58, priceGrid: 0.20, priceFeedIn: 0.05},
	}

	for i, s := range seeds {
		ts := base.Add(time.Duration(i) * 15 * time.Minute)
		require.NoError(t, persist(grid, ts, s.gridImport, s.gridExport, nil, false, false))
		require.NoError(t, persist(home, ts, s.home, 0, nil, false, false))
		require.NoError(t, persist(pv, ts, s.pv, 0, nil, false, false))
		require.NoError(t, persist(lp, ts, s.lp, 0, nil, false, false))
		soc := s.soc
		require.NoError(t, persist(bat, ts, 0, 0, &soc, false, false))
		g, f := s.priceGrid, s.priceFeedIn
		require.NoError(t, PersistTariffs(ts, &g, &f, nil, nil))
	}

	from := base
	to := base.Add(time.Duration(len(seeds)) * 15 * time.Minute)

	chain, err := ComputeChain(context.Background(), from, to, nil)
	require.NoError(t, err)
	require.Len(t, chain.Worlds, 4)
	require.NotNil(t, chain.BatteryPhysics)
	require.InDelta(t, 0, chain.BatteryPhysics.FloorFrac, 1e-9)
	require.GreaterOrEqual(t, chain.BatteryPhysics.MaxChargeKWh, 3.0)
	require.GreaterOrEqual(t, chain.BatteryPhysics.MaxDischargeKWh, 3.0)

	require.InDelta(t, 1.76, chain.Worlds[0].Settled.PerSlot, 1e-9, "W0")
	require.InDelta(t, 1.23, chain.Worlds[1].Settled.PerSlot, 1e-9, "W1")
	require.InDelta(t, 0.0, chain.Worlds[2].Settled.PerSlot, 1e-9, "W2")
	require.InDelta(t, 0.825, chain.Worlds[3].Settled.PerSlot, 1e-9, "W3")

	require.Len(t, chain.Contributions, 3)
	require.NotEqual(t, 0.0, chain.Contributions[1].Settled.PerSlot, `"Battery" contribution must not be zero - a zero here means W2 collapsed to W1`)
}

// TestFloorFracSensitivityIsLabelled covers the adversarial finding behind B20:
// FloorFrac is "the lowest observed SoC anywhere in this battery's history" (see
// deriveBatteryPhysicsUncached), an arbitrary statistic that directly throttles how
// much the W2 counterfactual battery is allowed to discharge - one extra low-SoC row,
// with nothing else about the period changed, can materially move (and even flip the
// sign of) the headline Control figure. There is no configured-floor alternative to
// fall back to (grep -rn "floorFrac|floorSource|capacitySource|batteryPhysics"
// assets/ finds nothing - no renderer exists yet), so this asserts the figure and its
// source are carried in chain.Notes - the one field every caller already renders
// unconditionally - not left sitting unused inside batteryPhysics.
func TestFloorFracSensitivityIsLabelled(t *testing.T) {
	loc := time.Now().Location()
	period := time.Date(2026, 8, 15, 0, 0, 0, 0, loc)

	run := func(extraLowSocRow bool) *Chain {
		require.NoError(t, db.NewInstance("sqlite", ":memory:"))
		require.NoError(t, SetupSchema())

		grid := mustCreateEntity(t, Grid, Grid)
		home := mustCreateEntity(t, Home, Home)
		bat := mustCreateEntity(t, Battery, "bat1")

		seedBatteryCalibration(t, bat, time.Date(2026, 7, 1, 0, 0, 0, 0, loc)) // capacity 10kWh, min observed SoC ~13.67%

		if extraLowSocRow {
			soc2 := 2.0
			require.NoError(t, persist(bat, time.Date(2026, 6, 1, 0, 0, 0, 0, loc), 0, 0, &soc2, false, false))
		}

		require.NoError(t, persist(grid, period, 0.2, 0, nil, false, false))
		require.NoError(t, persist(home, period, 1.0, 0, nil, false, false))
		soc := 20.0
		require.NoError(t, persist(bat, period, 0, 0, &soc, false, false))
		g, f := 0.30, 0.05
		require.NoError(t, PersistTariffs(period, &g, &f, nil, nil))

		chain, err := ComputeChain(context.Background(), period, period.Add(15*time.Minute), nil)
		require.NoError(t, err)
		return chain
	}

	chainHigherFloor := run(false)
	chainLowerFloor := run(true)

	require.NotNil(t, chainHigherFloor.BatteryPhysics)
	require.NotNil(t, chainLowerFloor.BatteryPhysics)
	require.Greater(t, chainHigherFloor.BatteryPhysics.FloorFrac, chainLowerFloor.BatteryPhysics.FloorFrac,
		"the extra low-SoC row must lower FloorFrac")

	delta := chainHigherFloor.Control.Full - chainLowerFloor.Control.Full
	t.Logf("measured Control.Full delta from one extra history row: %.6f (higher floor=%.6f, lower floor=%.6f)",
		delta, chainHigherFloor.Control.Full, chainLowerFloor.Control.Full)
	require.Greater(t, math.Abs(delta), 0.05, "FloorFrac must materially move Control.Full - see the hand-computed swing in this test's doc comment")

	for _, chain := range []*Chain{chainHigherFloor, chainLowerFloor} {
		found := slices.ContainsFunc(chain.Notes, func(n string) bool {
			return strings.Contains(n, "floor")
		})
		require.True(t, found, "chain.Notes must carry the battery floor and its source - no renderer exists yet to read it out of batteryPhysics")
	}
}

// TestComputeW0W1IncludeLoadpointLoad is a narrow unit test on the world functions
// directly (no DB): HomeKWh alone is a derived residual with loadpoint charge power
// already subtracted out by core/site.go's updatePower, so W0/W1 must price
// HomeKWh+LoadpointKWh, not HomeKWh alone, or they silently model a household with no
// EV while W3 (the real grid meter) paid for it.
func TestComputeW0W1IncludeLoadpointLoad(t *testing.T) {
	slots := []slotData{
		{HomeKWh: 1.0, LoadpointKWh: 2.0, PVKWh: 0.5},
	}

	w0 := computeW0(slots)
	require.InDelta(t, 3.0, w0[0].ImportKWh, 1e-9, "W0 must buy household load plus EV charging, not household load alone")

	w1 := computeW1(slots)
	require.InDelta(t, 2.5, w1[0].ImportKWh, 1e-9) // 3.0 load - 0.5 PV
	require.InDelta(t, 0, w1[0].ExportKWh, 1e-9)
}

// TestChainAccountsForEVChargingAgainstControlAndPV reproduces, at unit scale, the
// bug behind ADR-011's Priority-1 rework: EV charging landed in W3 (the real grid
// meter) but nowhere in W0-W2, so the "Control" contribution absorbed the EV's own
// grid cost as if the controller had wastefully bought energy the household never
// needed, and any PV that actually charged the car was booked in W1 as export revenue
// instead of as load served.
func TestChainAccountsForEVChargingAgainstControlAndPV(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	lp := mustCreateEntity(t, Loadpoint, "lp-1")

	loc := time.Now().Location()
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, loc)

	// no PV, no battery: a 1kWh household plus a 2kWh EV charge, all bought from the
	// grid - exactly what actually happened (grid import 3.0kWh).
	require.NoError(t, persist(grid, base, 3.0, 0, nil, false, false))
	require.NoError(t, persist(home, base, 1.0, 0, nil, false, false))
	require.NoError(t, persist(lp, base, 2.0, 0, nil, false, false))
	g, f := 0.30, 0.05
	require.NoError(t, PersistTariffs(base, &g, &f, nil, nil))

	chain, err := ComputeChain(context.Background(), base, base.Add(15*time.Minute), nil)
	require.NoError(t, err)

	control := chain.Contributions[2]
	// before the fix, W0/W1/W2 all priced HomeKWh alone (1.0kWh @ 0.30 = 0.30) against
	// an actual cost of 3.0kWh @ 0.30 = 0.90, reporting Control = -0.60 - a large loss
	// purely because the EV's own consumption wasn't modelled anywhere upstream of W3.
	// With the EV load included, the counterfactual buys the same 3.0kWh the real site
	// did, so a controller doing nothing wrong reports zero, not a loss.
	require.InDelta(t, 0.0, control.Settled.PerSlot, 1e-9,
		"the EV's own grid cost must not be attributed to Control as if it were a wasteful decision")
}

// TestChainAttributesPVToEVChargingNotPhantomExport covers the other half of the same
// bug: PV that actually charged a car must be credited to the PV contribution as load
// served, not booked in W1 as feed-in export revenue that was never earned.
func TestChainAttributesPVToEVChargingNotPhantomExport(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	pv := mustCreateEntity(t, PV, "pv1")
	lp := mustCreateEntity(t, Loadpoint, "lp-1")

	loc := time.Now().Location()
	base := time.Date(2026, 8, 10, 13, 0, 0, 0, loc)

	// 2kWh of PV entirely charges the car; no household load, nothing bought or sold
	// on the grid - this is what actually happened.
	require.NoError(t, persist(grid, base, 0, 0, nil, false, false))
	require.NoError(t, persist(home, base, 0, 0, nil, false, false))
	require.NoError(t, persist(pv, base, 2.0, 0, nil, false, false))
	require.NoError(t, persist(lp, base, 2.0, 0, nil, false, false))
	g, f := 0.30, 0.05
	require.NoError(t, PersistTariffs(base, &g, &f, nil, nil))

	chain, err := ComputeChain(context.Background(), base, base.Add(15*time.Minute), nil)
	require.NoError(t, err)

	pvContribution := chain.Contributions[0]
	control := chain.Contributions[2]

	// before the fix: W0 (HomeKWh=0) cost 0, W1 modelled 0 load against 2kWh PV as a
	// pure export (2kWh * 0.05 feedin = -0.10), crediting PV with only 0.10 - a
	// fraction of what the PV was actually worth - while Control absorbed a matching
	// -0.10 "loss" for the export revenue W1 claimed but W3 never earned.
	require.InDelta(t, 0.60, pvContribution.Settled.PerSlot, 1e-9,
		"PV that charged the EV must be credited at the full avoided grid cost (2kWh * 0.30), not the feedin price for a phantom export")
	require.InDelta(t, 0.0, control.Settled.PerSlot, 1e-9)
}

// TestBuildLedgerSlotsRefusesLoadpointWithoutChargeMeter covers ADR-011 rule 4 as
// applied to the Priority-1 fix: a loadpoint entity that exists (core/loadpoint.go
// always registers one) but has never written a single meters row is indistinguishable
// from "this loadpoint has no charge meter configured" (lp.chargeEnergy stays nil, so
// AddEnergy is never called at all - not even a zero-energy row). Modelling the
// household as car-free in that case would repeat exactly the bug this rework fixes,
// so buildLedgerSlots must refuse.
func TestBuildLedgerSlotsRefusesLoadpointWithoutChargeMeter(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	mustCreateEntity(t, Loadpoint, "lp-1") // registered, but never persist()'d

	loc := time.Now().Location()
	base := time.Date(2026, 8, 10, 14, 0, 0, 0, loc)

	require.NoError(t, persist(grid, base, 1.0, 0, nil, false, false))
	require.NoError(t, persist(home, base, 1.0, 0, nil, false, false))
	g, f := 0.30, 0.05
	require.NoError(t, PersistTariffs(base, &g, &f, nil, nil))

	_, err := ComputeRealisedCost(context.Background(), base, base.Add(15*time.Minute), nil)
	require.NoError(t, err, "ComputeRealisedCost reads only the grid meter and tariffs - it must not be affected by a loadpoint's missing charge meter")

	_, err = ComputeChain(context.Background(), base, base.Add(15*time.Minute), nil)
	require.ErrorIs(t, err, ErrLoadpointNoChargeMeter)
}

// TestChainControlSplitIdentities pins the algebra ControlSplit's doc comment
// promises: Full is the same number as the Control contribution's per-slot settlement
// (two different code paths computing the same thing must actually agree), and Routing
// + Timing must reconstitute Full exactly, not approximately.
func TestChainControlSplitIdentities(t *testing.T) {
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

	chain, err := ComputeChain(context.Background(), from, to, nil)
	require.NoError(t, err)
	require.NotNil(t, chain.Control)

	require.InDelta(t, chain.Contributions[2].Settled.PerSlot, chain.Control.Full, 1e-9,
		"ControlSplit.Full and the Control contribution's per-slot settlement price the identical W2->W3 gap and must agree exactly")
	require.InDelta(t, chain.Control.Full, chain.Control.Routing+chain.Control.Timing, 1e-9,
		"Routing + Timing must reconstitute Full exactly - Timing is defined as Full - Routing")
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

	chain, err := ComputeChain(context.Background(), base, base.Add(15*time.Minute), nil)
	require.NoError(t, err)

	require.InDelta(t, 0.30, chain.Worlds[0].Settled.PerSlot, 1e-9)
	require.InDelta(t, 1.50, chain.Worlds[3].Settled.PerSlot, 1e-9)

	total := chain.Worlds[0].Settled.PerSlot - chain.Worlds[3].Settled.PerSlot
	require.Less(t, total, 0.0)

	control := chain.Contributions[2]
	require.InDelta(t, total, control.Settled.PerSlot, 1e-9)
	require.Less(t, control.Settled.PerSlot, 0.0, "a loss-making period must be reported as a loss, not clamped to zero")
}

// TestChainNotesFeedInStaticFallback: an imputed feed-in price must never be
// indistinguishable from an observed one. The coverage figure beside it depends on the
// substitution, so the payload has to say how many slots it applied to and why it was
// allowed (ADR-011 rule 7).
func TestChainNotesFeedInStaticFallback(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)

	base := time.Date(2026, 8, 21, 12, 30, 0, 0, time.Now().Location())
	g, f := 0.25, 0.0
	for i := range 2 {
		ts := base.Add(time.Duration(i) * 15 * time.Minute)
		require.NoError(t, persist(grid, ts, 1.0, 0, nil, false, false))
		require.NoError(t, persist(home, ts, 1.0, 0, nil, false, false))
		if i == 0 {
			require.NoError(t, PersistTariffs(ts, &g, &f, nil, nil))
		} else {
			require.NoError(t, PersistTariffs(ts, &g, nil, nil, nil))
		}
	}

	seedFeedInWitnesses(t, base.Add(24*time.Hour), 0, minFeedInWitnessSlots)

	static := 0.0
	chain, err := ComputeChain(context.Background(), base, base.Add(30*time.Minute), &static)
	require.NoError(t, err)
	require.Equal(t, 2, chain.Coverage.ValidSlots)

	var found string
	for _, n := range chain.Notes {
		if strings.HasPrefix(n, "no feed-in price was recorded for 1 of the slots behind this figure") {
			found = n
		}
	}
	require.NotEmpty(t, found, "notes must disclose the substituted feed-in price, got %v", chain.Notes)
}

// TestBatteryFloorPrefersConfiguredMinimum is the N3 fix: the counterfactual battery's
// discharge floor must come from the installation's own configured minimum SoC
// (api.BatterySocLimiter, persisted by Collector.SetMinSoc) rather than from the lowest
// SoC the audited controller ever ran the pack down to.
//
// The seeded history's own minimum is ~13.67% (see seedBatteryCalibration); the
// configured minimum here is 5%. Preferring the observed statistic makes the
// counterfactual battery less able to discharge, which makes it more expensive, which
// flatters the controller it is being compared against - a self-flattering metric in the
// strict sense, and retroactive on top: one new low row restates every past figure.
func TestBatteryFloorPrefersConfiguredMinimum(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	bat := mustCreateEntity(t, Battery, "bat1")
	loc := time.Now().Location()
	seedBatteryCalibration(t, bat, time.Date(2026, 7, 1, 0, 0, 0, 0, loc))

	observed, err := deriveBatteryPhysicsUncached(context.Background())
	require.NoError(t, err)
	require.Greater(t, observed.FloorFrac, 0.10, "seeded history's own minimum")
	require.Contains(t, observed.FloorSource, "fallback")
	require.Contains(t, observed.floorNote(), "behaviour of the controller being measured")

	// capacity as well as the floor: persistedBatteryFloorFrac needs both to have a
	// defensible weight for the aggregate pack (see its doc comment).
	require.NoError(t, bat.updateCapacity(10))
	require.NoError(t, bat.updateMinSoc(0.05))

	configured, err := deriveBatteryPhysicsUncached(context.Background())
	require.NoError(t, err)
	require.InDelta(t, 0.05, configured.FloorFrac, 1e-9)
	require.Equal(t, floorSourceConfigured, configured.FloorSource)
	require.Contains(t, configured.floorNote(), "configured minimum")
}

// TestBatteryFloorFallsBackWhenOnlyOneBatteryReportsALimit covers
// persistedBatteryFloorFrac's all-or-nothing rule: two batteries where only one reports a
// configured minimum have no honest aggregate floor, so the observed-minimum fallback -
// clearly labelled - is the right answer rather than a partial figure.
func TestBatteryFloorFallsBackWhenOnlyOneBatteryReportsALimit(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	loc := time.Now().Location()
	bat1 := mustCreateEntity(t, Battery, "bat1")
	bat2 := mustCreateEntity(t, Battery, "bat2")
	seedBatteryCalibration(t, bat1, time.Date(2026, 7, 1, 0, 0, 0, 0, loc))

	require.NoError(t, bat1.updateCapacity(10))
	require.NoError(t, bat2.updateCapacity(10))
	require.NoError(t, bat1.updateMinSoc(0.05))

	phys, err := deriveBatteryPhysicsUncached(context.Background())
	require.NoError(t, err)
	require.Contains(t, phys.FloorSource, "fallback")

	// once the second battery reports one too, the aggregate is the capacity-weighted
	// mean of the two - not either one on its own.
	require.NoError(t, bat2.updateMinSoc(0.15))
	phys, err = deriveBatteryPhysicsUncached(context.Background())
	require.NoError(t, err)
	require.Equal(t, floorSourceConfigured, phys.FloorSource)
	require.InDelta(t, 0.10, phys.FloorFrac, 1e-9)
}
