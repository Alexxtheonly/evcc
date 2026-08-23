package metrics

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPersistOptimizerRun(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(optimizerRun)))

	slot := time.Date(2026, 4, 15, 16, 15, 0, 0, time.UTC)
	objective, overshoot := 1.5, 0.0
	importExceeded, exportHit := false, true

	require.NoError(t, PersistOptimizerRun(slot, "Optimal", &objective, &overshoot, &overshoot, &importExceeded, &exportHit, true))

	var res optimizerRun
	require.NoError(t, db.Instance.First(&res).Error)
	require.Equal(t, slot.Unix(), res.Timestamp)
	require.Equal(t, "Optimal", res.Status)
	require.NotNil(t, res.ObjectiveValue)
	require.InDelta(t, 1.5, *res.ObjectiveValue, 0.001)
	require.NotNil(t, res.GridImportOvershoot)
	require.NotNil(t, res.GridImportLimitExceeded)
	require.False(t, *res.GridImportLimitExceeded)
	require.NotNil(t, res.GridExportLimitHit)
	require.True(t, *res.GridExportLimitHit)

	// an infeasible run has no schedule to measure: the diagnostic fields must
	// be NULL, not a silently-zeroed "no overshoot, no violation" reading
	other := slot.Add(15 * time.Minute)
	require.NoError(t, PersistOptimizerRun(other, "Infeasible", nil, nil, nil, nil, nil, false))

	var res2 optimizerRun
	require.NoError(t, db.Instance.Where("ts = ?", other.Unix()).First(&res2).Error)
	require.Equal(t, "Infeasible", res2.Status)
	require.Nil(t, res2.ObjectiveValue)
	require.Nil(t, res2.GridImportOvershoot)
	require.Nil(t, res2.GridExportOvershoot)
	require.Nil(t, res2.GridImportLimitExceeded)
	require.Nil(t, res2.GridExportLimitHit)

	// duplicate slot ignored, first observation kept - only true for the
	// sampled conflict strategy, see TestPersistOptimizerRunDistinctTimestamps
	// for the non-sampled one
	require.NoError(t, PersistOptimizerRun(slot, "Infeasible", nil, nil, nil, nil, nil, true))

	var count int64
	require.NoError(t, db.Instance.Model(new(optimizerRun)).Where("ts = ?", slot.Unix()).Count(&count).Error)
	require.Equal(t, int64(1), count)

	require.NoError(t, db.Instance.Where("ts = ?", slot.Unix()).First(&res).Error)
	require.Equal(t, "Optimal", res.Status)
}

// TestPersistOptimizerRunDistinctTimestamps confirms the mechanical layer
// this package exposes places no per-slot dedup of its own on the caller -
// that policy (sample one Optimal/Feasible row per slot, but always record
// every other status at its own timestamp) lives in the caller
// (core.Site.persistOptimizerRun, see TestPersistOptimizerRunNonOptimalAlwaysRecorded
// in core/site_optimizer_test.go for F2's behavioral coverage). Here, several
// runs at distinct real timestamps within the same nominal slot must all
// land their own row.
func TestPersistOptimizerRunDistinctTimestamps(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(optimizerRun)))

	slot := time.Date(2026, 4, 15, 16, 15, 0, 0, time.UTC)
	objective, overshoot := 2.0, 0.0
	ok := false

	require.NoError(t, PersistOptimizerRun(slot, "Optimal", &objective, &overshoot, &overshoot, &ok, &ok, true))
	for i := 1; i <= 3; i++ {
		require.NoError(t, PersistOptimizerRun(slot.Add(time.Duration(i)*30*time.Second), "Infeasible", nil, nil, nil, nil, nil, false))
	}

	var rows []optimizerRun
	require.NoError(t, db.Instance.Where("ts >= ? AND ts < ?", slot.Unix(), slot.Add(15*time.Minute).Unix()).Order("ts").Find(&rows).Error)
	require.Len(t, rows, 4)

	assert.Equal(t, "Optimal", rows[0].Status)
	require.NotNil(t, rows[0].ObjectiveValue)

	for _, r := range rows[1:] {
		assert.Equal(t, "Infeasible", r.Status)
		assert.Nil(t, r.ObjectiveValue)
	}
}

// TestPersistOptimizerRunNonSampledCollisionUpdates covers the edge case
// DoNothing would get wrong for the non-sampled (always-record) path: two
// runs landing in the same wall-clock second (this table's ts granularity is
// whole unix seconds) must not have the second one silently vanish - that
// would undermine F2's "every non-Optimal run is recorded" guarantee for
// exactly the runs it exists to catch. UpdateAll means the latest status for
// that second wins instead of being dropped.
func TestPersistOptimizerRunNonSampledCollisionUpdates(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(optimizerRun)))

	ts := time.Date(2026, 4, 15, 16, 15, 3, 0, time.UTC)

	require.NoError(t, PersistOptimizerRun(ts, "Infeasible", nil, nil, nil, nil, nil, false))
	require.NoError(t, PersistOptimizerRun(ts, "Error", nil, nil, nil, nil, nil, false))

	var count int64
	require.NoError(t, db.Instance.Model(new(optimizerRun)).Where("ts = ?", ts.Unix()).Count(&count).Error)
	require.Equal(t, int64(1), count, "same-second collision still leaves exactly one row, not zero")

	var status string
	require.NoError(t, db.Instance.Raw("SELECT status FROM optimizer_runs WHERE ts = ?", ts.Unix()).Row().Scan(&status))
	assert.Equal(t, "Error", status, "the later run wins rather than being silently dropped")
}

// TestDeleteOptimizerRuns covers F9: a manual-delete endpoint for
// optimizer_runs, matching the existing energy/tariffs ones.
func TestDeleteOptimizerRuns(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(optimizerRun)))

	base := time.Date(2026, 4, 15, 16, 0, 0, 0, time.UTC)
	for i := range 4 {
		ts := base.Add(time.Duration(i) * 15 * time.Minute)
		require.NoError(t, PersistOptimizerRun(ts, "Optimal", nil, nil, nil, nil, nil, true))
	}

	count := func() int64 {
		var n int64
		require.NoError(t, db.Instance.Model(new(optimizerRun)).Count(&n).Error)
		return n
	}
	require.Equal(t, int64(4), count())

	// both bounds are required
	_, err := DeleteOptimizerRuns(time.Time{}, base)
	require.Error(t, err)

	rows, err := DeleteOptimizerRuns(base, base.Add(30*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, int64(2), rows)
	assert.Equal(t, int64(2), count())
}

// TestPersistOptimizerRunLegacyRow simulates a row written before the
// diagnostic columns existed: querying it back must return nil, not 0.
func TestPersistOptimizerRunLegacyRow(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(optimizerRun)))

	slot := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Instance.Exec(
		`INSERT INTO optimizer_runs (ts, status) VALUES (?, ?)`, slot.Unix(), "Optimal",
	).Error)

	var res optimizerRun
	require.NoError(t, db.Instance.First(&res).Error)
	require.Nil(t, res.ObjectiveValue)
	require.Nil(t, res.GridImportOvershoot)
	require.Nil(t, res.GridExportOvershoot)
	require.Nil(t, res.GridImportLimitExceeded)
	require.Nil(t, res.GridExportLimitHit)
}
