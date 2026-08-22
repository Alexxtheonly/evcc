package metrics

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/require"
)

// TestBatteryPowerSamples verifies the charge/discharge split (return_energy = charge,
// energy = discharge, see the accumulation convention documented on the function) and that
// zero, recovered and incomplete slots are excluded - the same exclusions LastSlotEnergy and
// the household demand profile already apply, so a downtime catchup or a failed reading does
// not skew a derived power limit either.
func TestBatteryPowerSamples(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	entity, err := createEntity(Battery, "test-battery", "")
	require.NoError(t, err)

	base := time.Unix(0, 0)
	rows := []struct {
		offset                time.Duration
		energy, returnEnergy  float64
		recovered, incomplete bool
	}{
		{0 * time.Minute, 1000, 0, false, false},    // discharge 1000kWh
		{15 * time.Minute, 0, 2000, false, false},   // charge 2000kWh
		{30 * time.Minute, 0, 0, false, false},      // idle: zero, excluded from both
		{45 * time.Minute, 500, 3000, false, false}, // both directions in one slot (edge case)
		{60 * time.Minute, 9999, 9999, true, false}, // recovered: excluded
		{75 * time.Minute, 8888, 8888, false, true}, // incomplete: excluded
	}

	for _, r := range rows {
		require.NoError(t, persist(entity, base.Add(r.offset), r.energy, r.returnEnergy, nil, r.recovered, r.incomplete))
	}

	c := &Collector{entity: entity}
	charge, discharge, err := c.BatteryPowerSamples(base)
	require.NoError(t, err)

	require.ElementsMatch(t, []float64{2000, 3000}, charge)
	require.ElementsMatch(t, []float64{1000, 500}, discharge)
}

func TestBatteryPowerSamplesRespectsFrom(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	entity, err := createEntity(Battery, "test-battery-2", "")
	require.NoError(t, err)

	base := time.Unix(0, 0)
	require.NoError(t, persist(entity, base, 1000, 0, nil, false, false))
	require.NoError(t, persist(entity, base.Add(15*time.Minute), 2000, 0, nil, false, false))

	c := &Collector{entity: entity}
	charge, discharge, err := c.BatteryPowerSamples(base.Add(15 * time.Minute))
	require.NoError(t, err)

	require.Empty(t, charge)
	require.ElementsMatch(t, []float64{2000}, discharge, "slot before 'from' must be excluded")
}
