package metrics

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPersistControlSlot(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(controlSlot)))

	slot := time.Date(2026, 4, 15, 16, 15, 0, 0, time.UTC)
	price := 0.22

	require.NoError(t, PersistControlSlot(slot, "charge", "charge", "", true, &price))

	var res controlSlot
	require.NoError(t, db.Instance.First(&res).Error)
	require.Equal(t, slot.Unix(), res.Timestamp)
	require.Equal(t, "charge", res.AppliedMode)
	require.Equal(t, "charge", res.SuggestedMode)
	require.Equal(t, "", res.VetoReason)
	require.True(t, res.HealthOk)
	require.NotNil(t, res.Price)
	require.InDelta(t, 0.22, *res.Price, 0.001)

	// a slot with no active charge decision carries no price - absence, not 0
	other := slot.Add(15 * time.Minute)
	require.NoError(t, PersistControlSlot(other, "normal", "normal", "", true, nil))

	var res2 controlSlot
	require.NoError(t, db.Instance.Where("ts = ?", other.Unix()).First(&res2).Error)
	require.Nil(t, res2.Price)

	// a vetoed suggestion records why the applied mode differs from the
	// suggested one - the whole point of the table
	third := other.Add(15 * time.Minute)
	require.NoError(t, PersistControlSlot(third, "normal", "charge", "payback", true, nil))

	var res3 controlSlot
	require.NoError(t, db.Instance.Where("ts = ?", third.Unix()).First(&res3).Error)
	require.Equal(t, "normal", res3.AppliedMode)
	require.Equal(t, "charge", res3.SuggestedMode)
	require.Equal(t, "payback", res3.VetoReason)

	// duplicate slot ignored, first observation kept (mirrors PersistTariffs)
	require.NoError(t, PersistControlSlot(slot, "normal", "normal", "", false, nil))

	var count int64
	require.NoError(t, db.Instance.Model(new(controlSlot)).Where("ts = ?", slot.Unix()).Count(&count).Error)
	require.Equal(t, int64(1), count)

	require.NoError(t, db.Instance.Where("ts = ?", slot.Unix()).First(&res).Error)
	require.Equal(t, "charge", res.AppliedMode)
}

// TestDeleteControlSlots covers F9: a manual-delete endpoint for
// control_slots, matching the existing energy/tariffs ones.
func TestDeleteControlSlots(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(controlSlot)))

	base := time.Date(2026, 4, 15, 16, 0, 0, 0, time.UTC)
	for i := range 4 {
		ts := base.Add(time.Duration(i) * 15 * time.Minute)
		require.NoError(t, PersistControlSlot(ts, "normal", "normal", "", true, nil))
	}

	count := func() int64 {
		var n int64
		require.NoError(t, db.Instance.Model(new(controlSlot)).Count(&n).Error)
		return n
	}
	require.Equal(t, int64(4), count())

	// both bounds are required
	_, err := DeleteControlSlots(time.Time{}, base)
	require.Error(t, err)

	rows, err := DeleteControlSlots(base, base.Add(30*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, int64(2), rows)
	assert.Equal(t, int64(2), count())
}

// TestPersistControlSlotLegacyRow simulates a row written before a column
// existed (e.g. before Price was added): querying it back must not fail or
// silently coerce the missing value into 0.
func TestPersistControlSlotLegacyRow(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(controlSlot)))

	slot := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Instance.Exec(
		`INSERT INTO control_slots (ts, applied_mode, suggested_mode, veto_reason, health_ok) VALUES (?, ?, ?, ?, ?)`,
		slot.Unix(), "normal", "normal", "", true,
	).Error)

	var res controlSlot
	require.NoError(t, db.Instance.First(&res).Error)
	require.Nil(t, res.Price)
}
