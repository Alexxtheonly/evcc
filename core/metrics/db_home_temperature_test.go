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
