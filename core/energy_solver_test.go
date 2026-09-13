package core

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/evcc-io/evcc/util"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpectedBatteryAvailabilityAndGoal(t *testing.T) {
	start := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	starts := []time.Time{start, start.Add(15 * time.Minute), start.Add(30 * time.Minute), start.Add(45 * time.Minute)}
	bat, ok := expectedVehicleBattery(75, 35, 1380, 11000, 60, start.Add(20*time.Minute), start.Add(time.Hour), starts, []int{900, 900, 900, 900})
	require.True(t, ok)
	assert.Equal(t, []bool{false, false, true, true}, bat.Availability)
	assert.Equal(t, []float32{0, 0, 0, 45000}, bat.SGoal)
	assert.Equal(t, float32(26250), bat.SInitial)
	encoded, err := json.Marshal(energyRequest{Batteries: []energyBattery{bat}})
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"availability":[false,false,true,true]`)
}

func TestExpectedVehiclesSharePhysicalCharger(t *testing.T) {
	start := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	reserved := []energyChargerReservation{{"wallbox", start, start.Add(time.Hour)}}
	assert.False(t, chargerWindowAvailable(reserved, "wallbox", start.Add(30*time.Minute), start.Add(2*time.Hour)))
	assert.True(t, chargerWindowAvailable(reserved, "wallbox", start.Add(time.Hour), start.Add(2*time.Hour)))
	assert.True(t, chargerWindowAvailable(reserved, "other", start, start.Add(time.Hour)))
}

func TestEnergyCostSeparatesWearAndInventory(t *testing.T) {
	etaD, wear := 0.8, 0.00005
	req := energyRequest{OptimizationInput: optimizer.OptimizationInput{EtaD: .9, TimeSeries: optimizer.TimeSeries{PN: []float32{.0001, .0004}, PE: []float32{0, 0}}}, Batteries: []energyBattery{{BatteryConfig: optimizer.BatteryConfig{SInitial: 5000, PA: .0001}, EtaD: &etaD, WearCost: &wear}}}
	res := optimizer.OptimizationResult{GridImport: []float32{1000, 0}, GridExport: []float32{0, 0}, Batteries: []optimizer.BatteryResult{{DischargingPower: []float32{0, 800}, StateOfCharge: []float32{5000, 4000}}}}
	assert.InDelta(t, .25, energyCost(req, res), .000001) // import .10 + DC wear .05 + depleted inventory .10
	assert.False(t, energyPayback([]float32{.0002, .0004}, []float32{6000, 5000}, []float32{0, 900}, 5000, .95, .95, .2))
}

func TestEnergyCostPricesImportOvershootOnce(t *testing.T) {
	req := energyRequest{OptimizationInput: optimizer.OptimizationInput{Grid: optimizer.GridConfig{PMaxImp: 4000}, TimeSeries: optimizer.TimeSeries{PN: []float32{.0002}, PE: []float32{0}, Dt: []int{900}}}}
	res := optimizer.OptimizationResult{GridImport: []float32{0}, GridImportOvershoot: []float32{1000}, GridExport: []float32{0}}
	assert.InDelta(t, .2, energyCost(req, res), .000001)
	assert.Equal(t, 1, minimaxRegret([][]float64{{energyCost(req, res)}, {.15}})) // overshoot cannot win by disappearing
	req.Grid.PrcPExcImp = .00001
	res.GridImport[0] = 1000                              // demand-rate response already includes overshoot
	assert.InDelta(t, .24, energyCost(req, res), .000001) // .20 energy + 4000W * .00001
}

func TestRobustFirstActionUsesExecutableModes(t *testing.T) {
	req := energyRequest{OptimizationInput: optimizer.OptimizationInput{EtaC: 1, EtaD: 1, TimeSeries: optimizer.TimeSeries{Dt: []int{900, 900}, Gt: []float32{1000, 500}, Ft: []float32{0, 0}}}, Batteries: []energyBattery{{BatteryConfig: optimizer.BatteryConfig{SInitial: 1000, SMax: 1000, DMax: 4000}}}}
	details := requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeBattery}}}
	partial := optimizer.OptimizationResult{Batteries: []optimizer.BatteryResult{{ChargingPower: []float32{0, 0}, DischargingPower: []float32{500, 500}}}}
	actions, err := executableFirstActions(req, details, partial)
	require.NoError(t, err)
	require.Len(t, actions, 2) // normal=1000Wh, hold=0Wh; no unsupported 500Wh
	assert.Equal(t, "normal", actions[0].name)
	assert.Equal(t, 1000.0, *actions[0].request.Batteries[0].FirstStepDischarge)
	assert.Equal(t, 0.0, *actions[1].request.Batteries[0].FirstStepDischarge)
	// Hardware hold blocks discharge only; holdcharge blocks PV charging.
	req.TimeSeries.Gt[0], req.TimeSeries.Ft[0] = 0, 1000
	req.Batteries[0].SInitial, req.Batteries[0].CMax = 0, 4000
	actions, err = executableFirstActions(req, details, partial)
	require.NoError(t, err)
	require.Len(t, actions, 2)
	assert.Equal(t, "normal", actions[0].name)
	assert.Equal(t, 1000.0, *actions[0].request.Batteries[0].FirstStepCharge)
	assert.Equal(t, "holdcharge", actions[1].name)
	assert.Equal(t, 0.0, *actions[1].request.Batteries[0].FirstStepCharge)
}

func TestRobustPartialDischargeIntegration(t *testing.T) {
	url := os.Getenv("EVCC_ENERGY_SOLVER_TEST_URL")
	if url == "" {
		t.Skip("isolated companion required")
	}
	client, err := optimizer.NewClientWithResponses(url)
	require.NoError(t, err)
	site := &Site{log: util.NewLogger("energy-test"), optimizerClient: client}
	req := energyRequest{OptimizationInput: optimizer.OptimizationInput{EtaC: 1, EtaD: 1, TimeSeries: optimizer.TimeSeries{Dt: []int{900, 900}, Gt: []float32{1000, 500}, Ft: []float32{0, 0}, PN: []float32{.0002, .0003}, PE: []float32{0, 0}}}, Batteries: []energyBattery{{BatteryConfig: optimizer.BatteryConfig{SInitial: 1000, SMax: 1000, DMax: 4000}}}}
	details := requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeBattery, Capacity: 1}}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	base, err := site.solveEnergy(ctx, req)
	require.NoError(t, err)
	assert.InDelta(t, 500, base.Batteries[0].DischargingPower[0], .1)
	res, summary, err := site.robustEnergy(ctx, req, details, base)
	require.NoError(t, err)
	assert.Equal(t, "normal", summary.Selected)
	assert.InDelta(t, 1000, res.Batteries[0].DischargingPower[0], .1)
	assert.InDelta(t, .15, energyCost(req, res), .000001)
}

func TestEnergySolverIntegration(t *testing.T) {
	url := os.Getenv("EVCC_ENERGY_SOLVER_TEST_URL")
	if url == "" {
		t.Skip("set EVCC_ENERGY_SOLVER_TEST_URL to the isolated companion")
	}
	t.Setenv("OPTIMIZER_URI", url)
	client, err := optimizer.NewClientWithResponses(url)
	require.NoError(t, err)
	site := &Site{log: util.NewLogger("energy-test"), optimizerClient: client}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, site.requireEnergyCapabilities(ctx))
	const n = 8
	req := energyRequest{OptimizationInput: optimizer.OptimizationInput{EtaC: .9, EtaD: .9}, Batteries: []energyBattery{{BatteryConfig: optimizer.BatteryConfig{SCapacity: 19320, SInitial: 5000, SMax: 18000, SMin: 966, CMax: 6000, DMax: 6000, ChargeFromGrid: true}}}}
	details := requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeBattery, Name: "battery", Capacity: 19.32}}}
	for i := 0; i < n; i++ {
		price := float32(.0001)
		if i > 1 {
			price = .0004
		}
		req.TimeSeries.Dt = append(req.TimeSeries.Dt, 900)
		req.TimeSeries.Gt = append(req.TimeSeries.Gt, 1000)
		req.TimeSeries.Ft = append(req.TimeSeries.Ft, 0)
		req.TimeSeries.PN = append(req.TimeSeries.PN, price)
		req.TimeSeries.PE = append(req.TimeSeries.PE, 0)
		site.energyInsights.Forecast = append(site.energyInsights.Forecast, energyForecastSlot{HomeLowWh: 500, HomeWh: 1000, HomeHighWh: 1500, SolarHighWh: 2000})
	}
	base, err := site.solveEnergy(ctx, req)
	require.NoError(t, err)
	res, summary, err := site.robustEnergy(ctx, req, details, base)
	require.NoError(t, err)
	require.Len(t, summary.Evaluations, 3)
	require.Len(t, summary.BatterySocLow, n)
	require.NoError(t, validateEnergyResult(req, res))
	for _, row := range summary.Evaluations {
		require.Len(t, row.Costs, 3)
	}
	// A real solve must enforce an expected vehicle's absence, not merely echo the field.
	ev := energyBattery{BatteryConfig: optimizer.BatteryConfig{SCapacity: 75000, SInitial: 30000, SMax: 45000, CMax: 11000, ChargeFromGrid: true}, Availability: []bool{false, false, false, false, true, true, true, true}}
	ev.SGoal = make([]float32, n)
	ev.SGoal[n-1] = 35000
	req.Batteries = append(req.Batteries, ev)
	res, err = site.solveEnergy(ctx, req)
	require.NoError(t, err)
	for _, charge := range res.Batteries[1].ChargingPower[:4] {
		assert.InDelta(t, 0, charge, .1)
	}
	assert.GreaterOrEqual(t, res.Batteries[1].StateOfCharge[n-1], float32(34999))
}
