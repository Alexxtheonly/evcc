package metrics

import (
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

	_, err := ComputeRealisedCost(from, to)
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

	res, err := ComputeRealisedCost(from, to)
	require.NoError(t, err)

	require.Equal(t, 5, res.Coverage.TotalSlots)
	require.Equal(t, 3, res.Coverage.ValidSlots)
	require.InDelta(t, 0.6, res.Coverage.Fraction, 1e-9)

	// cost only over the 3 valid slots (1kWh import @ 0.25 each), not scaled up to
	// pretend the excluded slots behaved the same way
	require.InDelta(t, 3*0.25, res.Settled.PerSlot, 1e-9)
}
