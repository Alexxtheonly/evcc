package metrics

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/require"
)

func TestPersistOptimizerRun(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(optimizerRun)))

	slot := time.Date(2026, 4, 15, 16, 15, 0, 0, time.UTC)
	objective, overshoot := 1.5, 0.0
	importExceeded, exportHit := false, true

	require.NoError(t, PersistOptimizerRun(slot, "Optimal", &objective, &overshoot, &overshoot, &importExceeded, &exportHit))

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
	require.NoError(t, PersistOptimizerRun(other, "Infeasible", nil, nil, nil, nil, nil))

	var res2 optimizerRun
	require.NoError(t, db.Instance.Where("ts = ?", other.Unix()).First(&res2).Error)
	require.Equal(t, "Infeasible", res2.Status)
	require.Nil(t, res2.ObjectiveValue)
	require.Nil(t, res2.GridImportOvershoot)
	require.Nil(t, res2.GridExportOvershoot)
	require.Nil(t, res2.GridImportLimitExceeded)
	require.Nil(t, res2.GridExportLimitHit)

	// duplicate slot ignored, first observation kept
	require.NoError(t, PersistOptimizerRun(slot, "Infeasible", nil, nil, nil, nil, nil))

	var count int64
	require.NoError(t, db.Instance.Model(new(optimizerRun)).Where("ts = ?", slot.Unix()).Count(&count).Error)
	require.Equal(t, int64(1), count)

	require.NoError(t, db.Instance.Where("ts = ?", slot.Unix()).First(&res).Error)
	require.Equal(t, "Optimal", res.Status)
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
