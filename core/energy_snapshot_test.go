package core

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/util"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/require"
)

func TestEnergySnapshotPreservesChangedAssumptions(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())
	stamp := time.Now().Truncate(15 * time.Minute)
	site := &Site{log: util.NewLogger("snapshot-test"), energyInsights: optimizerInsights{Settings: EnergyIntelligenceSettings{SettlementMode: "simulation"}}}
	req := energyRequest{OptimizationInput: optimizer.OptimizationInput{EtaC: .9, EtaD: .9, TimeSeries: optimizer.TimeSeries{Dt: []int{900}}}, Batteries: []energyBattery{{BatteryConfig: optimizer.BatteryConfig{SInitial: 5000, SMax: 18000, SMin: 966, CMax: 6000, DMax: 6000, SCapacity: 19320}}}}
	details := requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeBattery, Name: "db:11", Capacity: 19.32, etaC: .9, etaD: .9}}}
	res := optimizer.OptimizationResult{GridImport: []float32{1000}, GridExport: []float32{0}, Batteries: []optimizer.BatteryResult{{ChargingPower: []float32{0}, DischargingPower: []float32{0}, StateOfCharge: []float32{5000}}}}
	suggested := "hold"
	require.NoError(t, metrics.PersistControlSlot(stamp, "normal", &suggested, "", true, nil))
	require.NoError(t, site.archiveEnergyRun(req, details, res))
	first := *site.energySnapshotID
	require.NoError(t, metrics.BindControlSlotSnapshot(stamp, first))
	// Routine replanning changes current inventory and the first partial slot.
	req.Batteries[0].SInitial = 4900
	req.TimeSeries.Dt[0] = 890
	require.NoError(t, site.archiveEnergyRun(req, details, res))
	require.Equal(t, first, *site.energySnapshotID)
	// Same hold action, genuinely different assumed conversion efficiency.
	declared := .97
	req.Batteries[0].EtaC = &declared
	details.BatteryDetails[0].etaC = declared
	site.energyInsights.Settings.BatteryEfficiency = map[string]EnergyEfficiencySetting{"db:11": {ChargeEfficiency: &declared, DischargeEfficiency: &declared}}
	require.NoError(t, site.archiveEnergyRun(req, details, res))
	require.NotEqual(t, first, *site.energySnapshotID)
	var row struct{ SnapshotUnavailable bool }
	require.NoError(t, db.Instance.Table("control_slots").Where("ts = ?", stamp.Unix()).Scan(&row).Error)
	require.True(t, row.SnapshotUnavailable)
	for _, change := range []func(){
		func() { req.Batteries[0].SGoal = []float32{5500} },
		func() { req.Batteries[0].SMax = 17000 },
		func() { site.energyInsights.Settings.BatteryWear = map[string]float64{"db:11": .03} },
		func() { site.energyInsights.Settings.BatteryEnergyPlane = map[string]string{"db:11": "dc"} },
	} {
		previous := *site.energySnapshotID
		change()
		require.NoError(t, site.archiveEnergyRun(req, details, res))
		require.NotEqual(t, previous, *site.energySnapshotID)
	}
	snapshot, err := metrics.GetOptimizerSnapshot(*site.energySnapshotID)
	require.NoError(t, err)
	var economics struct {
		Batteries []metrics.SnapshotBatteryEconomics `json:"batteries"`
	}
	require.NoError(t, json.Unmarshal(snapshot.Economics, &economics))
	require.NotNil(t, economics.Batteries[0].ChargeCeilingFrac)
	require.InDelta(t, 17000.0/19320, *economics.Batteries[0].ChargeCeilingFrac, 1e-6)
}
