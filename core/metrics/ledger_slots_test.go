package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/require"
)

func mustCreateEntity(t *testing.T, group, name string) entity {
	t.Helper()
	e, err := createEntity(group, name, name)
	require.NoError(t, err)
	return e
}

// TestRefusedBeforeTariffStart covers ADR-011 honesty rule 4: no figure for any
// period before the tariffs table starts - the caller gets the earliest computable
// date back, not a silently back-filled number.
func TestRefusedBeforeTariffStart(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)

	loc := time.Now().Location()
	tariffStart := time.Date(2026, 8, 17, 0, 0, 0, 0, loc)

	g, f := 0.25, 0.05
	require.NoError(t, PersistTariffs(tariffStart, &g, &f, nil, nil))
	require.NoError(t, persist(grid, tariffStart, 1, 0, nil, false, false))
	require.NoError(t, persist(home, tariffStart, 1, 0, nil, false, false))

	from := tariffStart.Add(-24 * time.Hour)
	to := tariffStart

	_, err := ComputeRealisedCost(context.Background(), from, to, nil)
	require.Error(t, err)

	var refused *ErrBeforeTariffStart
	require.ErrorAs(t, err, &refused)
	require.True(t, refused.Earliest.Equal(tariffStart), "expected earliest computable date %v, got %v", tariffStart, refused.Earliest)
}

// TestCoverageExcludesRecoveredIncompleteSlots covers ADR-011 honesty rule 3: an
// incomplete grid reading and a slot with no meter row at all are both dropped from
// the computation and reported in Coverage, never scaled up to compensate.
func TestCoverageExcludesRecoveredIncompleteSlots(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)

	loc := time.Now().Location()
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, loc)

	g, f := 0.25, 0.05

	// slots 0,2,3: clean. slot 1: grid reading incomplete. slot 4: no meter row at
	// all (only a tariff), simulating a data gap rather than a flagged reading.
	for i := range 4 {
		ts := base.Add(time.Duration(i) * 15 * time.Minute)
		require.NoError(t, persist(grid, ts, 1.0, 0, nil, false, i == 1))
		require.NoError(t, persist(home, ts, 1.0, 0, nil, false, false))
		require.NoError(t, PersistTariffs(ts, &g, &f, nil, nil))
	}
	gapTs := base.Add(4 * 15 * time.Minute)
	require.NoError(t, PersistTariffs(gapTs, &g, &f, nil, nil))

	from := base
	to := base.Add(5 * 15 * time.Minute)

	res, err := ComputeRealisedCost(context.Background(), from, to, nil)
	require.NoError(t, err)

	require.Equal(t, 5, res.Coverage.TotalSlots)
	require.Equal(t, 3, res.Coverage.ValidSlots)
	require.InDelta(t, 0.6, res.Coverage.Fraction, 1e-9)

	// cost only over the 3 valid slots (1kWh import @ 0.25 each), not scaled up to
	// pretend the excluded slots behaved the same way
	require.InDelta(t, 3*0.25, res.Settled.PerSlot, 1e-9)
}

// TestBuildLedgerSlotsRefusesOversizedRange covers the Priority-3 fix: the ledger
// endpoint is unauthenticated (server/http_savings_ledger_handler.go) and
// ComputeLedger runs upwards of a dozen queries against a database with a single
// connection (server/db/db.go's SetMaxOpenConns(1)) - an unbounded ?from=2000-01-01
// would queue every other write behind it. buildLedgerSlots is the one choke point
// every ledger computation goes through, so the cap belongs there.
func TestBuildLedgerSlotsRefusesOversizedRange(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	from := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

	_, err := buildLedgerSlots(context.Background(), from, to, false, false, nil)
	require.ErrorIs(t, err, ErrLedgerRangeTooLarge)
}

// TestBuildLedgerSlotsRefusesUnalignedRange covers the "confident EUR 0.00" bug: an
// unaligned from (not on a 15-minute tariff slot boundary) matches zero rows at every
// step of buildLedgerSlots' fixed-15-minute walk, even over a period full of data -
// producing a plausible-looking but wrong "computed over 0 slots" answer instead of
// visibly failing.
func TestBuildLedgerSlotsRefusesUnalignedRange(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)

	loc := time.Now().Location()
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, loc)
	g, f := 0.25, 0.05
	require.NoError(t, PersistTariffs(base, &g, &f, nil, nil))
	require.NoError(t, persist(grid, base, 1, 0, nil, false, false))
	require.NoError(t, persist(home, base, 1, 0, nil, false, false))

	unalignedFrom := base.Add(7 * time.Minute)
	_, err := buildLedgerSlots(context.Background(), unalignedFrom, unalignedFrom.Add(15*time.Minute), false, false, nil)
	require.ErrorIs(t, err, ErrLedgerRangeUnaligned)

	unalignedTo := base.Add(15*time.Minute + 3*time.Second)
	_, err = buildLedgerSlots(context.Background(), base, unalignedTo, false, false, nil)
	require.ErrorIs(t, err, ErrLedgerRangeUnaligned)

	// the aligned equivalent must still work
	_, err = buildLedgerSlots(context.Background(), base, base.Add(15*time.Minute), false, false, nil)
	require.NoError(t, err)
}

// TestBuildLedgerSlotsRefusesInvertedRange covers D1: a reversed or identical
// [from,to) used to fall through to a plain errors.New("invalid period: to must be
// after from"), which savingsLedgerErrorStatus doesn't recognise, so the HTTP handler
// reported 500 for what is a malformed request, not a server fault - the same failure
// mode ErrLoadpointNoChargeMeter had before it got its own case (see that error's doc
// comment). ErrLedgerRangeInverted gives the handler something to match on.
func TestBuildLedgerSlotsRefusesInvertedRange(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	loc := time.Now().Location()
	base := time.Date(2026, 8, 22, 0, 0, 0, 0, loc)

	// reversed: to before from
	_, err := buildLedgerSlots(context.Background(), base.Add(24*time.Hour), base, false, false, nil)
	require.ErrorIs(t, err, ErrLedgerRangeInverted)

	// identical: to == from
	_, err = buildLedgerSlots(context.Background(), base, base, false, false, nil)
	require.ErrorIs(t, err, ErrLedgerRangeInverted)
}

// TestComputeRealisedCostIgnoresBatterySocFailures covers the Priority-4 finding: one
// shared filter previously served every ledger computation, so a slot with a missing
// or invalid battery SoC reading was dropped from EVERY computation - including
// ComputeRealisedCost, whose own doc comment says it reads only the grid meter and
// tariffs and is deliberately independent of the battery. A week of BYD SoC read
// failures must not delete a week from "the one measured number everything hangs
// off".
func TestComputeRealisedCostIgnoresBatterySocFailures(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	mustCreateEntity(t, Battery, "bat1") // configured, but no reading this slot

	loc := time.Now().Location()
	base := time.Date(2026, 8, 12, 0, 0, 0, 0, loc)
	g, f := 0.30, 0.05

	require.NoError(t, persist(grid, base, 1.0, 0, nil, false, false))
	require.NoError(t, persist(home, base, 1.0, 0, nil, false, false))
	require.NoError(t, PersistTariffs(base, &g, &f, nil, nil))
	// no battery meter row at all for this slot - a read failure, not a config choice

	res, err := ComputeRealisedCost(context.Background(), base, base.Add(15*time.Minute), nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.Coverage.ValidSlots, "a battery SoC failure must not exclude a slot from the realised-cost figure")
	require.InDelta(t, 0.30, res.Settled.PerSlot, 1e-9)

	// the same failure DOES still exclude the slot from a battery-dependent
	// computation - ComputeChain needs BatterySocFrac for W2.
	set, err := buildLedgerSlots(context.Background(), base, base.Add(15*time.Minute), false, true, nil)
	require.NoError(t, err)
	require.Empty(t, set.Slots, "a battery-dependent computation must still drop a slot with no SoC reading")
}

// TestBuildLedgerSlotsEnergyBalance is a data-integrity guard on buildLedgerSlots'
// output itself, independent of any world simulation: every slot's measured sources
// (PV generation, grid import, battery discharge) must balance its measured sinks
// (household residual, loadpoint charging, grid export, battery charge) to within
// meter rounding. This is the invariant a future change to which meters feed which
// slotData field (the exact class of mistake ADR-011's Priority-1 rework fixed for
// loadpoints) would violate.
func TestBuildLedgerSlotsEnergyBalance(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	pv := mustCreateEntity(t, PV, "pv1")
	bat := mustCreateEntity(t, Battery, "bat1")
	lp := mustCreateEntity(t, Loadpoint, "lp-1")

	loc := time.Now().Location()
	base := time.Date(2026, 8, 11, 9, 0, 0, 0, loc)

	// sources: 3.0kWh PV, 1.0kWh grid import, 0.5kWh battery discharge = 4.5kWh
	// sinks: 2.0kWh home (residual), 1.5kWh loadpoint, 0.5kWh grid export, 0.5kWh
	// battery charge = 4.5kWh
	require.NoError(t, persist(grid, base, 1.0, 0.5, nil, false, false))
	require.NoError(t, persist(home, base, 2.0, 0, nil, false, false))
	require.NoError(t, persist(pv, base, 3.0, 0, nil, false, false))
	soc := 50.0
	require.NoError(t, persist(bat, base, 0.5, 0.5, &soc, false, false))
	require.NoError(t, persist(lp, base, 1.5, 0, nil, false, false))
	g, f := 0.30, 0.05
	require.NoError(t, PersistTariffs(base, &g, &f, nil, nil))

	set, err := buildLedgerSlots(context.Background(), base, base.Add(15*time.Minute), true, true, nil)
	require.NoError(t, err)
	require.Len(t, set.Slots, 1)

	s := set.Slots[0]
	sources := s.PVKWh + s.GridImportKWh + s.BatteryDischargeKWh
	sinks := s.HomeKWh + s.LoadpointKWh + s.GridExportKWh + s.BatteryChargeKWh
	require.InDelta(t, sources, sinks, 1e-9, "PV + import + discharge must balance home + loadpoint + export + charge")
}

// TestQueryTariffSlotsBindsFeedIn covers the GORM naming-strategy trap:
// tariffValue tags FeedIn as column "feedin" (see tariffValue in tariffs.go), but
// queryTariffSlots' local scan struct left the field untagged, so GORM's default
// naming strategy expected a "feed_in" column and the actual "feedin" column never
// bound - every export in the ledger silently priced at EUR 0 regardless of what
// was on record. Grid was unaffected only because its column name happens to equal
// its lowercased field name.
func TestQueryTariffSlotsBindsFeedIn(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	loc := time.Now().Location()
	ts := time.Date(2026, 8, 20, 12, 0, 0, 0, loc)

	g, f := 0.30, 0.08
	require.NoError(t, PersistTariffs(ts, &g, &f, nil, nil))

	slots, err := queryTariffSlots(context.Background(), ts, ts.Add(15*time.Minute))
	require.NoError(t, err)
	require.Contains(t, slots, ts.Unix())

	got := slots[ts.Unix()]
	require.InDelta(t, 0.30, got.Grid, 1e-9)
	require.NotNil(t, got.FeedIn, "FeedIn must bind from the \"feedin\" column, not stay nil")
	require.InDelta(t, 0.08, *got.FeedIn, 1e-9, "FeedIn must bind from the \"feedin\" column, not stay at its zero value")
}

// ptr is a local helper for the *float64 prices these tests deal in.
func ptr(v float64) *float64 { return &v }

// TestFeedInFallbackGuard is the safety net on P1. Substituting the site's currently
// configured static feed-in price for a slot that has none on record is only honest
// while that configured price is also what the period actually recorded - the configs
// table keeps no history, so nothing else can tell us the rate hasn't changed since.
// The case that matters most is "recorded differs from configured": that is what
// happens the day the owner replaces the EUR 0.00 placeholder with the real EEG rate,
// and it must refuse rather than silently reprice history.
func TestFeedInFallbackGuard(t *testing.T) {
	for _, tc := range []struct {
		desc   string
		rows   map[int64]tariffSlot
		static *float64
		want   *float64
	}{
		{
			desc:   "no static tariff configured: never substitute",
			rows:   map[int64]tariffSlot{1: {Grid: 0.3, FeedIn: ptr(0)}, 2: {Grid: 0.3}},
			static: nil,
			want:   nil,
		},
		{
			desc:   "every recorded price equals the configured one: substitute",
			rows:   map[int64]tariffSlot{1: {Grid: 0.3, FeedIn: ptr(0)}, 2: {Grid: 0.3}},
			static: ptr(0),
			want:   ptr(0),
		},
		{
			desc:   "recorded price differs from the configured one: refuse",
			rows:   map[int64]tariffSlot{1: {Grid: 0.3, FeedIn: ptr(0)}, 2: {Grid: 0.3}},
			static: ptr(0.0786),
			want:   nil,
		},
		{
			desc:   "recorded prices disagree with each other: refuse",
			rows:   map[int64]tariffSlot{1: {Grid: 0.3, FeedIn: ptr(0)}, 2: {Grid: 0.3, FeedIn: ptr(0.0786)}},
			static: ptr(0),
			want:   nil,
		},
		{
			desc:   "nothing recorded in the period: nothing corroborates the value, refuse",
			rows:   map[int64]tariffSlot{1: {Grid: 0.3}, 2: {Grid: 0.3}},
			static: ptr(0),
			want:   nil,
		},
		{
			desc:   "empty period: refuse",
			rows:   map[int64]tariffSlot{},
			static: ptr(0),
			want:   nil,
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			got := feedInFallback(tc.rows, tc.static)
			if tc.want == nil {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.InDelta(t, *tc.want, *got, 1e-9)
		})
	}
}

// TestBuildLedgerSlotsStaticFeedInFallback is P1 end to end: a slot with a grid price,
// a grid and a home reading, but no recorded feed-in price used to be dropped outright.
// On this site that was 86 of 238 slots in the current period - and the dropped half
// was the PV-rich daytime half, so the headline figures were computed on a biased
// sample. It is now included when (and only when) feedInFallback's guard holds.
func TestBuildLedgerSlotsStaticFeedInFallback(t *testing.T) {
	seed := func(t *testing.T) time.Time {
		t.Helper()
		require.NoError(t, db.NewInstance("sqlite", ":memory:"))
		require.NoError(t, SetupSchema())

		grid := mustCreateEntity(t, Grid, Grid)
		home := mustCreateEntity(t, Home, Home)

		base := time.Date(2026, 8, 21, 12, 30, 0, 0, time.Now().Location())
		g, f := 0.25, 0.0
		for i := range 4 {
			ts := base.Add(time.Duration(i) * 15 * time.Minute)
			require.NoError(t, persist(grid, ts, 1.0, 0, nil, false, false))
			require.NoError(t, persist(home, ts, 1.0, 0, nil, false, false))
			// only the first two slots got a feed-in price on record
			if i < 2 {
				require.NoError(t, PersistTariffs(ts, &g, &f, nil, nil))
			} else {
				require.NoError(t, PersistTariffs(ts, &g, nil, nil, nil))
			}
		}
		return base
	}

	t.Run("no static tariff: the unpriced slots stay excluded", func(t *testing.T) {
		base := seed(t)
		set, err := buildLedgerSlots(context.Background(), base, base.Add(time.Hour), false, false, nil)
		require.NoError(t, err)
		require.Equal(t, 4, set.TotalSlots)
		require.Len(t, set.Slots, 2)
		require.Zero(t, set.FeedInFallbackSlots)
	})

	t.Run("static tariff matching the record: the unpriced slots are included", func(t *testing.T) {
		base := seed(t)
		set, err := buildLedgerSlots(context.Background(), base, base.Add(time.Hour), false, false, ptr(0))
		require.NoError(t, err)
		require.Len(t, set.Slots, 4)
		require.Equal(t, 2, set.FeedInFallbackSlots)
		require.InDelta(t, 0, set.FeedInFallbackPrice, 1e-9)
		for _, s := range set.Slots {
			require.InDelta(t, 0, s.PriceFeedIn, 1e-9)
		}
	})

	t.Run("static tariff differing from the record: still excluded", func(t *testing.T) {
		base := seed(t)
		set, err := buildLedgerSlots(context.Background(), base, base.Add(time.Hour), false, false, ptr(0.0786))
		require.NoError(t, err)
		require.Len(t, set.Slots, 2, "the configured rate changed since those slots were recorded - substituting it would reprice history")
		require.Zero(t, set.FeedInFallbackSlots)
	})
}

// TestEarliestTariffSlotIgnoresFeedIn is P2: the endpoint's lower bound only needs a
// grid price. Requiring a feed-in price too made every window before the first
// recorded feed-in value a hard refusal - 352 slots / 3.7 days on this site, 331 of
// them with a grid meter reading, none of them queryable at all.
func TestEarliestTariffSlotIgnoresFeedIn(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	loc := time.Now().Location()
	gridOnly := time.Date(2026, 8, 17, 20, 30, 0, 0, loc)
	withFeedIn := time.Date(2026, 8, 21, 12, 30, 0, 0, loc)

	g, f := 0.25, 0.0
	require.NoError(t, PersistTariffs(gridOnly, &g, nil, nil, nil))
	require.NoError(t, PersistTariffs(withFeedIn, &g, &f, nil, nil))

	earliest, err := EarliestTariffSlot(context.Background())
	require.NoError(t, err)
	require.True(t, earliest.Equal(gridOnly), "expected %v, got %v", gridOnly, earliest)
}

// TestFeedInFallbackCountExcludesDroppedSlots: FeedInFallbackSlots is what the payload
// note quotes, so it has to count slots the substitution actually put into the result.
// Counting at the point of substitution instead over-reported badly on real data - a
// 590-slot window claimed 416 substituted slots while only 217 slots were included at
// all, because the pre-battery half took the fallback price and was then dropped for
// having no battery SoC.
func TestFeedInFallbackCountExcludesDroppedSlots(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	pv := mustCreateEntity(t, PV, PV)

	base := time.Date(2026, 8, 21, 12, 30, 0, 0, time.Now().Location())
	g, f := 0.25, 0.0

	// slot 0: fully measured, feed-in on record - the evidence the guard needs
	require.NoError(t, persist(grid, base, 1.0, 0, nil, false, false))
	require.NoError(t, persist(home, base, 1.0, 0, nil, false, false))
	require.NoError(t, persist(pv, base, 0.5, 0, nil, false, false))
	require.NoError(t, PersistTariffs(base, &g, &f, nil, nil))

	// slot 1: no feed-in price AND no PV reading - takes the fallback, then drops
	ts := base.Add(15 * time.Minute)
	require.NoError(t, persist(grid, ts, 1.0, 0, nil, false, false))
	require.NoError(t, persist(home, ts, 1.0, 0, nil, false, false))
	require.NoError(t, PersistTariffs(ts, &g, nil, nil, nil))

	set, err := buildLedgerSlots(context.Background(), base, base.Add(30*time.Minute), false, false, ptr(0))
	require.NoError(t, err)
	require.Len(t, set.Slots, 1)
	require.Zero(t, set.FeedInFallbackSlots, "a slot dropped for an unrelated missing reading is not a slot the substitution produced")
}
