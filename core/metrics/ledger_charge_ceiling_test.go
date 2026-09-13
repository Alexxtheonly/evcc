package metrics

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSnapshotGridChargeCeilingDoesNotLimitSolarCharging(t *testing.T) {
	snapshot := &OptimizerSnapshot{Version: 1, Economics: json.RawMessage(`{"batteries":[{"name":"battery","capacityKWh":19.32,"etaC":0.9,"etaD":0.9,"floorFrac":0.05,"chargeCeilingFrac":0.95,"maxChargeKWh":5,"maxDischargeKWh":5,"source":"configured"}]}`)}
	physics, _, valid := snapshotPhysics(snapshot, batteryPhysics{})
	require.True(t, valid)
	initial := 19.32 * 0.94
	next, flow, ok := simulateSlotStep(batteryModeCharge, 0, 0, initial, physics)
	require.True(t, ok)
	require.InDelta(t, 19.32*0.95, next, 1e-9)
	require.InDelta(t, 0.1932/0.9, flow.ImportKWh, 1e-9)
	next, _, ok = simulateSlotStep(batteryModeNormal, 0, 5, initial, physics)
	require.True(t, ok)
	require.InDelta(t, 19.32, next, 1e-9)
	snapshot.Economics = json.RawMessage(strings.ReplaceAll(string(snapshot.Economics), `"chargeCeilingFrac":0.95`, `"chargeCeilingFrac":1.05`))
	_, source, valid := snapshotPhysics(snapshot, batteryPhysics{})
	require.False(t, valid)
	require.Equal(t, "invalid_snapshot_charge_ceiling", source)
}
