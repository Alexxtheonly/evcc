package metrics

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/require"
)

// TestQueryHomeTemperatureSamples verifies the meters/meters join across the Home and
// Temperature entities: a slot with both readings comes back paired, a slot missing the
// temperature reading is silently dropped rather than surfacing as some default temperature.
func TestQueryHomeTemperatureSamples(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	home, err := createEntity(Home, Home, "")
	require.NoError(t, err)
	temp, err := createEntity(Temperature, Temperature, "")
	require.NoError(t, err)

	base := time.Date(2026, 1, 15, 6, 0, 0, 0, time.UTC).Truncate(15 * time.Minute)
	pairedSlot := base
	unpairedSlot := base.Add(15 * time.Minute)

	socTemp := -2.5
	require.NoError(t, db.Instance.Create(&meter{Meter: home.Id, Timestamp: pairedSlot.Unix(), Energy: 1.2}).Error)
	require.NoError(t, db.Instance.Create(&meter{Meter: temp.Id, Timestamp: pairedSlot.Unix(), SocTemp: &socTemp}).Error)

	// unpaired slot has a Home reading but no matching Temperature reading
	require.NoError(t, db.Instance.Create(&meter{Meter: home.Id, Timestamp: unpairedSlot.Unix(), Energy: 0.9}).Error)

	rows, err := QueryHomeTemperatureSamples(base)
	require.NoError(t, err)
	require.Len(t, rows, 1, "only the paired slot, the unpaired one is dropped")
	require.InDelta(t, 1200.0, rows[0].Energy, 1e-9, "kWh converted to Wh")
	require.InDelta(t, -2.5, rows[0].Temperature, 1e-9)
}

// TestQueryHomeTemperatureSamplesExcludesTaintedRows verifies #27: a recovered row (a
// downtime catchup that dumps a whole outage's energy into one slot) or an incomplete row (a
// power reading that failed mid-slot) is excluded the same way energyProfile excludes them -
// the fit has no outlier resistance, so a single tainted slot must not reach it. A legacy row
// with NULL energy is treated as 0, matching energyProfile's COALESCE, rather than producing a
// NULL sample gorm silently zero-values.
func TestQueryHomeTemperatureSamplesExcludesTaintedRows(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	home, err := createEntity(Home, Home, "")
	require.NoError(t, err)
	temp, err := createEntity(Temperature, Temperature, "")
	require.NoError(t, err)

	base := time.Date(2026, 1, 15, 6, 0, 0, 0, time.UTC).Truncate(15 * time.Minute)
	cleanSlot := base
	recoveredSlot := base.Add(15 * time.Minute)
	incompleteSlot := base.Add(30 * time.Minute)
	legacyNullSlot := base.Add(45 * time.Minute)

	t0 := -2.5
	require.NoError(t, db.Instance.Create(&meter{Meter: home.Id, Timestamp: cleanSlot.Unix(), Energy: 1.0}).Error)
	require.NoError(t, db.Instance.Create(&meter{Meter: temp.Id, Timestamp: cleanSlot.Unix(), SocTemp: &t0}).Error)

	require.NoError(t, db.Instance.Create(&meter{Meter: home.Id, Timestamp: recoveredSlot.Unix(), Energy: 50.0, Recovered: true}).Error)
	require.NoError(t, db.Instance.Create(&meter{Meter: temp.Id, Timestamp: recoveredSlot.Unix(), SocTemp: &t0}).Error)

	require.NoError(t, db.Instance.Create(&meter{Meter: home.Id, Timestamp: incompleteSlot.Unix(), Energy: 50.0, Incomplete: true}).Error)
	require.NoError(t, db.Instance.Create(&meter{Meter: temp.Id, Timestamp: incompleteSlot.Unix(), SocTemp: &t0}).Error)

	// legacy row: energy column left at its column default (NULL is not representable via the
	// meter struct's float64, so this exercises the same COALESCE path via SQL directly)
	require.NoError(t, db.Instance.Exec(
		`INSERT INTO meters (meter, ts, energy, return_energy) VALUES (?, ?, NULL, 0)`,
		home.Id, legacyNullSlot.Unix(),
	).Error)
	require.NoError(t, db.Instance.Create(&meter{Meter: temp.Id, Timestamp: legacyNullSlot.Unix(), SocTemp: &t0}).Error)

	rows, err := QueryHomeTemperatureSamples(base)
	require.NoError(t, err)
	require.Len(t, rows, 2, "recovered and incomplete slots excluded; clean and legacy-null slots kept")

	byEnergy := make(map[float64]bool, len(rows))
	for _, r := range rows {
		byEnergy[r.Energy] = true
	}
	require.True(t, byEnergy[1000.0], "clean slot: 1.0kWh -> 1000Wh")
	require.True(t, byEnergy[0.0], "legacy NULL energy coalesced to 0, not dropped or NaN")
}
