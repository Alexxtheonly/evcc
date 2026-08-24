package metrics

// Savings-ledger read-out harness: loads a copy of a real evcc database and prints
// the world chain for a period, one line per variant, so a change to the
// counterfactual's assumptions can be measured in euros instead of argued about.
//
// It is skipped unless LEDGER_DB names a database file. Run it with:
//
//	LEDGER_DB=/path/to/evcc.db go test ./core/metrics/ -run TestLedgerHarness -v -count=1
//
// Optional: LEDGER_FROM / LEDGER_TO (RFC3339, must be slot-aligned) to narrow the
// window; by default it uses every slot from the battery's first recorded reading to
// the last recorded meter slot, which is the widest window the chain can be computed
// over. LEDGER_FEEDIN_STATIC sets the site's configured static feed-in price (the
// value server/http_savings_ledger_handler.go's staticFeedInPrice would pass), since
// a live site's config isn't reachable from a test - omit it to price only slots with
// a recorded feed-in value.
//
// The database is copied into t.TempDir() before it is opened: SetupSchema runs
// migrations, so pointing this at a file that matters would write to it.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"github.com/stretchr/testify/require"
)

// openLedgerDBCopy copies the database at src into t.TempDir() and makes it
// db.Instance. Returns the copy's path.
func openLedgerDBCopy(t *testing.T, src string) {
	t.Helper()

	buf, err := os.ReadFile(src)
	require.NoError(t, err)

	dst := filepath.Join(t.TempDir(), "evcc.db")
	require.NoError(t, os.WriteFile(dst, buf, 0o600))

	require.NoError(t, db.NewInstance("sqlite", dst))
	require.NoError(t, SetupSchema())
	// deriveBatteryPhysics caches per db.Instance, but a second harness run in the
	// same process gets a fresh *gorm.DB, so the cache key changes with it.
}

// harnessWindow resolves the period to report on, defaulting to the widest window the
// chain could possibly cover: the battery's first recorded slot (before it there is no
// SoC, so buildLedgerSlots drops everything) to the last recorded meter slot.
func harnessWindow(t *testing.T) (time.Time, time.Time) {
	t.Helper()

	if f, tt := os.Getenv("LEDGER_FROM"), os.Getenv("LEDGER_TO"); f != "" && tt != "" {
		from, err := time.Parse(time.RFC3339, f)
		require.NoError(t, err)
		to, err := time.Parse(time.RFC3339, tt)
		require.NoError(t, err)
		return from.Local(), to.Local()
	}

	ids, err := batteryEntityIDs(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, ids, "harness needs a battery-configured site")

	var first, last int64
	require.NoError(t, db.Instance.Table("meters").Where("meter IN ?", ids).Select("MIN(ts)").Scan(&first).Error)
	require.NoError(t, db.Instance.Table("meters").Select("MAX(ts)").Scan(&last).Error)

	return time.Unix(first, 0).Local(), time.Unix(last, 0).Local().Add(tariff.SlotDuration)
}

func harnessFeedInStatic() *float64 {
	v, err := strconv.ParseFloat(os.Getenv("LEDGER_FEEDIN_STATIC"), 64)
	if err != nil {
		return nil
	}
	return &v
}

// w2Anchoring is the parameter under study in the harness: what, if anything, the
// counterfactual battery's simulated SoC is reconciled with at a calendar-day boundary
// or at a gap in the slot series.
//
//   - Day/Gap RESET the simulated SoC to the measured one (the shipped-before-N1
//     behaviour), which hands the counterfactual however much energy the real battery
//     had accumulated while the ledger was not looking.
//   - GapDelta instead CARRIES the measured battery's own state change across the
//     stretch the ledger could not price onto the simulated SoC, preserving the
//     simulation's own divergence.
type w2Anchoring struct {
	Day, Gap, GapDelta bool
}

func (a w2Anchoring) String() string {
	switch {
	case a.Day && a.Gap:
		return "reset day+gap"
	case a.Gap:
		return "reset gap"
	case a.Day:
		return "reset day"
	case a.GapDelta:
		return "carry gap delta"
	default:
		return "free-running"
	}
}

// w2Stats is one W2 run's outcome: its flows plus the bookkeeping that shows how much
// of the run was simulation and how much was measured state handed to it.
type w2Stats struct {
	Flows []worldFlow
	// AnchorEvents counts re-anchors AFTER the initial one (the initial anchor is
	// unavoidable - a simulation needs a starting state).
	AnchorEvents int
	// AnchorDeltaKWh is Σ (measured SoC - simulated SoC) in kWh over those events:
	// the energy the counterfactual was handed without buying or generating it.
	AnchorDeltaKWh    float64
	AnchorAbsDeltaKWh float64
	// FinalDriftKWh is (simulated final SoC - measured final SoC) in kWh: what the
	// counterfactual battery holds at the end of the period that reality does not,
	// or vice versa.
	FinalDriftKWh float64
	// Gaps counts breaks in the otherwise-15-minute-contiguous slot series, and
	// GapStateKWh sums the measured battery's OWN state change across them (SoC at
	// the first slot after the gap minus SoC at the last slot before it) - energy
	// that moved while the ledger was not looking, which no world in the chain
	// prices, and which a reset transplants wholesale.
	Gaps        int
	GapStateKWh float64
	Events      []string
}

// simulateW2 is the harness's parameterised twin of computeW2. It exists so the
// anchoring policy can be varied without a production knob; TestW2VariantMatchesProduction
// pins it to the shipped behaviour so the two cannot silently diverge.
func simulateW2(slots []slotData, phys batteryPhysics, a w2Anchoring) w2Stats {
	out := w2Stats{Flows: make([]worldFlow, len(slots))}

	var socKWh float64
	var started bool
	var day string
	var prevStart time.Time

	var prevMeasured, prevEndMeasured float64

	for i, s := range slots {
		measured := *s.BatterySocFrac * phys.CapacityKWh
		today := s.Start.Local().Format("2006-01-02")
		gap := !prevStart.IsZero() && !s.Start.Equal(prevStart.Add(tariff.SlotDuration))

		if gap {
			out.Gaps++
			out.GapStateKWh += measured - prevEndMeasured
			out.Events = append(out.Events, fmt.Sprintf("  gap %s -> %s (%d slots missing): measured pack moved %+.3f kWh (naive %+.3f), simulated was %.3f, measured is %.3f (reset would inject %+.3f)",
				prevStart.Format("01-02 15:04"), s.Start.Format("01-02 15:04"),
				int(s.Start.Sub(prevStart)/tariff.SlotDuration)-1,
				measured-prevEndMeasured, measured-prevMeasured, socKWh, measured, measured-socKWh))
		}

		switch {
		case !started:
			socKWh, started = measured, true
		case (a.Day && today != day) || (a.Gap && gap):
			out.AnchorEvents++
			out.AnchorDeltaKWh += measured - socKWh
			out.AnchorAbsDeltaKWh += math.Abs(measured - socKWh)
			socKWh = measured
		case a.GapDelta && gap:
			out.AnchorEvents++
			carried := min(max(socKWh+measured-prevEndMeasured, phys.FloorFrac*phys.CapacityKWh), phys.CapacityKWh)
			out.AnchorDeltaKWh += carried - socKWh
			out.AnchorAbsDeltaKWh += math.Abs(carried - socKWh)
			socKWh = carried
		}
		day = today
		prevMeasured = measured
		// measured SoC at the END of this slot: soc_temp is recorded at slot START, so
		// the pack's own measured movement during the slot has to be added to get the
		// state the next slot begins from. Uses the same one-way efficiencies
		// simulateSlotStep applies, so this is the simulation's own physics, not a new
		// assumption.
		prevEndMeasured = measured + s.BatteryChargeKWh*phys.EtaC - s.BatteryDischargeKWh/phys.EtaD

		newSoc, flow, _, _, _ := simulateSlotStep(batteryModeNormal, s.modelledLoadKWh(), s.PVKWh, socKWh, phys)
		socKWh = newSoc
		out.Flows[i] = flow
		prevStart = s.Start
	}

	if started {
		out.FinalDriftKWh = socKWh - prevEndMeasured
	}
	return out
}

// harnessRow is one variant's euro read-out.
type harnessRow struct {
	Label                string
	W0, W1, W2, W3       float64
	PV, Battery, Control float64
	ControlRouting       float64
	ControlTiming        float64
	AnchorEvents         int
	AnchorDeltaKWh       float64
	FinalDriftKWh        float64
}

func harnessMeasure(label string, slots []slotData, phys batteryPhysics, a w2Anchoring) (harnessRow, w2Stats) {
	st := simulateW2(slots, phys, a)

	w0 := settleFlows(slots, computeW0(slots))
	w1 := settleFlows(slots, computeW1(slots))
	w2 := settleFlows(slots, st.Flows)
	w3 := settleFlows(slots, actualFlows(slots))

	full := w2.PerSlot - w3.PerSlot
	routing := w2.PeriodAverage - w3.PeriodAverage

	return harnessRow{
		Label:          label,
		W0:             w0.PerSlot,
		W1:             w1.PerSlot,
		W2:             w2.PerSlot,
		W3:             w3.PerSlot,
		PV:             w0.PerSlot - w1.PerSlot,
		Battery:        w1.PerSlot - w2.PerSlot,
		Control:        full,
		ControlRouting: routing,
		ControlTiming:  full - routing,
		AnchorEvents:   st.AnchorEvents,
		AnchorDeltaKWh: st.AnchorDeltaKWh,
		FinalDriftKWh:  st.FinalDriftKWh,
	}, st
}

func TestLedgerHarness(t *testing.T) {
	src := os.Getenv("LEDGER_DB")
	if src == "" {
		t.Skip("set LEDGER_DB=/path/to/a/copy/of/evcc.db to run the harness")
	}

	openLedgerDBCopy(t, src)
	ctx := context.Background()
	from, to := harnessWindow(t)

	set, err := buildLedgerSlots(ctx, from, to, true, true, harnessFeedInStatic())
	require.NoError(t, err)

	phys, err := deriveBatteryPhysicsUncached(ctx)
	require.NoError(t, err)

	cov := set.coverage()
	res := computeMeterResidual(set.Slots)

	tariffEarliest, err := EarliestTariffSlot(ctx)
	require.NoError(t, err)
	chainEarliest, err := EarliestChainSlot(ctx)
	require.NoError(t, err)
	realised, err := ComputeRealisedCost(ctx, from, to, harnessFeedInStatic())
	require.NoError(t, err)

	fmt.Printf("\nwindow          %s .. %s\n", from.Format(time.RFC3339), to.Format(time.RFC3339))
	fmt.Printf("earliest        tariff %s, chain %s\n", tariffEarliest.Local().Format(time.RFC3339), chainEarliest.Local().Format(time.RFC3339))
	fmt.Printf("realised        perSlot EUR %.4f, periodAverage EUR %.4f over %d/%d slots (%.1f%%)\n",
		realised.Settled.PerSlot, realised.Settled.PeriodAverage,
		realised.Coverage.ValidSlots, realised.Coverage.TotalSlots, realised.Coverage.Fraction*100)
	fmt.Printf("chain coverage  %d/%d valid (%.1f%%), feed-in fallback on %d slots @ EUR %.4f\n",
		cov.ValidSlots, cov.TotalSlots, cov.Fraction*100, set.FeedInFallbackSlots, set.FeedInFallbackPrice)
	fmt.Printf("meterResidual   sum %+.3f kWh, abs %.3f kWh over %d slots\n", res.SumKWh, res.AbsSumKWh, res.Slots)
	fmt.Printf("physics         capacity %.2f kWh (%s)\n", phys.CapacityKWh, phys.CapacitySource)
	fmt.Printf("                floor %.4f (%s)\n", phys.FloorFrac, phys.FloorSource)
	fmt.Printf("                rate ceiling %.3f kWh/slot charge, %.3f kWh/slot discharge\n", phys.MaxChargeKWh, phys.MaxDischargeKWh)

	var meanGrid float64
	for _, s := range set.Slots {
		meanGrid += s.PriceGrid
	}
	if len(set.Slots) > 0 {
		meanGrid /= float64(len(set.Slots))
	}
	fmt.Printf("mean grid price EUR %.4f/kWh -> residual band |R| = EUR %.4f, net = EUR %.4f\n\n",
		meanGrid, res.AbsSumKWh*meanGrid, res.SumKWh*meanGrid)

	floors := []struct {
		label string
		frac  float64
	}{
		{"observed", phys.FloorFrac},
	}
	if v := os.Getenv("LEDGER_FLOORS"); v != "" {
		for _, f := range splitCSV(v) {
			frac, err := strconv.ParseFloat(f, 64)
			require.NoError(t, err)
			floors = append(floors, struct {
				label string
				frac  float64
			}{fmt.Sprintf("floor=%.3f", frac), frac})
		}
	}

	var events []string
	var gapState float64
	var gaps int

	fmt.Printf("%-28s %9s %9s %9s %9s | %9s %9s %9s | %6s %10s %10s\n",
		"variant", "W0", "W1", "W2", "W3", "PV", "Battery", "Control", "anchors", "anchorkWh", "driftkWh")
	for _, f := range floors {
		p := phys
		p.FloorFrac = f.frac
		for _, a := range []w2Anchoring{{Day: true, Gap: true}, {Gap: true}, {GapDelta: true}, {}} {
			r, st := harnessMeasure(fmt.Sprintf("%s/%s", f.label, a), set.Slots, p, a)
			fmt.Printf("%-28s %9.4f %9.4f %9.4f %9.4f | %9.4f %9.4f %9.4f | %6d %10.3f %10.3f\n",
				r.Label, r.W0, r.W1, r.W2, r.W3, r.PV, r.Battery, r.Control, r.AnchorEvents, r.AnchorDeltaKWh, r.FinalDriftKWh)
			if events == nil {
				events = st.Events
				gapState, gaps = st.GapStateKWh, st.Gaps
			}
		}
	}
	if dump := os.Getenv("LEDGER_DUMP"); dump != "" {
		ledger, err := ComputeLedger(ctx, from, to, harnessFeedInStatic())
		require.NoError(t, err)
		buf, err := json.MarshalIndent(ledger, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(dump, buf, 0o600))
		fmt.Printf("\nwrote full ComputeLedger payload to %s\n", dump)
	}

	fmt.Printf("\n%d gaps in the slot series, measured battery state moved %+.3f kWh across them\n", gaps, gapState)
	if os.Getenv("LEDGER_GAPS") != "" {
		for _, e := range events {
			fmt.Println(e)
		}
	}
	fmt.Println()
}

func splitCSV(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}

// TestW2VariantMatchesProduction pins the harness's parameterised simulateW2 to the
// shipped computeW2 at the anchoring policy production actually uses, so the comparison
// table the harness prints cannot quietly become a table about a different simulator.
// The fixture deliberately contains a gap, a midnight crossing and both charge and
// discharge slots - and three gaps in all, because the first one's carry lands inside
// [floor, capacity] and so pins nothing about the bound: deleting the min(max(...)) from
// either simulator left this test green. Gap two drives the carry below the floor
// (simulated 1.22kWh, carry -2.17kWh) and gap three above capacity (0.5 + 9.6kWh), so
// the bound is now exercised in both directions. It is the one part of the carry that
// can absorb energy silently.
func TestW2VariantMatchesProduction(t *testing.T) {
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

	want, drift, err := computeW2(slots, phys)
	require.NoError(t, err)

	got := simulateW2(slots, phys, w2Anchoring{GapDelta: true})
	require.Equal(t, want, got.Flows)
	require.Equal(t, drift.Gaps, got.Gaps)
	require.InDelta(t, drift.CarriedKWh, got.AnchorDeltaKWh, 1e-12)
	require.InDelta(t, drift.FinalKWh, got.FinalDriftKWh, 1e-12)

	// the fixture must keep BINDING the bound, or the equality above goes back to pinning
	// nothing about it. Unbounded, the three carries would be -2.583, -3.194 and +9.600
	// (sum +3.823); bounded at [0.5, 10] they are -2.583, -0.720 and +9.500.
	require.Equal(t, 3, drift.Gaps)
	require.InDelta(t, 6.197, drift.CarriedKWh, 1e-3)
}
