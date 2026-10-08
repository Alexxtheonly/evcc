package core

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// floorRequest reconstructs the 2026-10-07 18:06:02 run from the live log: Calypso at 38 %
// below its 40 % floor (55 kWh, 80 % limit), first slot 8m57s, BYD at 15 %, measured home
// and solar slots blended in. Every such run failed with "robust dispatch cannot fix a
// partial EV first action" (21 times between 18:00 and 18:10): the floor needs 1222 Wh,
// less than the 1647 Wh of a full-power first slot.
func floorRequest() energyRequest {
	const n = 8
	req := energyRequest{OptimizationInput: optimizer.OptimizationInput{EtaC: eta, EtaD: eta, Strategy: optimizer.OptimizerStrategy{
		ChargingStrategy: optimizer.OptimizerStrategyChargingStrategy("attenuate_demand_peaks"), DischargingStrategy: optimizer.OptimizerStrategyDischargingStrategyDischargeBeforeImport,
	}, TimeSeries: optimizer.TimeSeries{
		Dt: []int{537, 900, 900, 900, 900, 900, 900, 900},
		Gt: []float32{102.7, 170, 167, 207, 220, 230, 240, 235},
		Ft: []float32{34, 46, 37, 28, 18, 12, 9, 6},
		PN: []float32{.0005002, .0005037, .0005024, .000503, .0004871, .0004798, .0004696, .0004666},
		PE: make([]float32, n),
	}}}
	for t := range req.TimeSeries.PE {
		req.TimeSeries.PE[t] = .0000786
	}
	pa := terminalStorageValue(.0004666)
	req.Batteries = []energyBattery{
		{BatteryConfig: optimizer.BatteryConfig{ChargeFromGrid: true, CMin: 1380, CMax: 11040, PA: pa, SInitial: 20900, SMax: 44000, SMin: 22000, CPriority: 1}},
		{BatteryConfig: optimizer.BatteryConfig{ChargeFromGrid: true, CMax: 6000, DMax: 6294.56, PA: pa, SCapacity: 19320, SInitial: 2898, SMax: 18354, SMin: 966}},
	}
	return req
}

func floorDetails(mode api.ChargeMode) requestDetails {
	return requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeVehicle, Name: "calypso", Capacity: 55, mode: mode}, {Type: batteryTypeBattery, Name: "db:11", Capacity: 19.32}}}
}

func floorResult(planned float32) optimizer.OptimizationResult {
	return optimizer.OptimizationResult{Batteries: []optimizer.BatteryResult{
		{ChargingPower: []float32{planned}, DischargingPower: []float32{0}},
		{ChargingPower: []float32{0}, DischargingPower: []float32{0}},
	}}
}

func TestRobustFloorPartialEVActionRunsTheFloor(t *testing.T) {
	req := floorRequest()
	floor := (22000.0 - 20900) / eta // the floor fast-charges until minSoc, then follows the plan
	actions, err := executableFirstActions(req, floorDetails(api.ModePV), floorResult(1222.2))
	require.NoError(t, err, "the 18:06 failure")
	require.NotEmpty(t, actions)
	for _, action := range actions {
		assert.InDelta(t, floor, *action.request.Batteries[0].FirstStepCharge, 1e-6, action.name)
	}
	// a plan charging beyond the floor in the same slot keeps its planned energy
	actions, err = executableFirstActions(req, floorDetails(api.ModePV), floorResult(1500))
	require.NoError(t, err)
	assert.InDelta(t, 1500, *actions[0].request.Batteries[0].FirstStepCharge, 1e-3)
}

func TestExecutedEVCharge(t *testing.T) {
	wire := func(v float32) float64 { f, _ := wireValue(v); return f }
	const seconds = 900.0
	full := 11040.0 * seconds / 3600 // 2760 Wh
	minimum := 1380.0 * seconds / 3600
	for _, tc := range []struct {
		name     string
		mode     api.ChargeMode
		sInitial float32
		band     *energyBand
		planned  float32
		want     float64
	}{
		{"stop", api.ModePV, 25000, nil, 0, 0},
		{"full", api.ModePV, 25000, nil, float32(full), full},
		{"floor partial: full power to the floor", api.ModePV, 20900, nil, 900, 1100 / eta},
		{"floor partial beyond the floor", api.ModePV, 20900, nil, 1700, 1700},
		{"within the loadpoint's full tolerance", api.ModePV, 25000, nil, float32(full - 10), full},
		{"mode now near the limit stores what fits", api.ModeNow, 43000, nil, 900, 1000 / eta},
		{"mode now partial", api.ModeNow, 25000, nil, 1500, full},
		// 900 W of planned surplus at minimum power: tracked by the pv loop, never grid power
		{"pv surplus at minimum power", api.ModePV, 25000, nil, float32(minimum), minimum},
		{"limited grid setpoint", api.ModeMinPV, 25000, nil, 1700, 1700},
		{"float32 just below c_min", api.ModePV, 25000, nil, float32(minimum) - .0001, minimum},
		{"translated to stop", api.ModePV, 25000, nil, 10, 0},
		// review exp2: a surplus-fed band charge at or below the reference is not widened
		{"band partial below reference stays as planned", api.ModePV, 25000, cheapBand(liveBandInput()), 1700, 1700},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := floorRequest()
			req.TimeSeries.Dt[0] = int(seconds)
			req.TimeSeries.PN[0] = .0003 // below the band reference
			req.Batteries[0].SInitial = tc.sInitial
			detail := batteryDetail{Type: batteryTypeVehicle, mode: tc.mode, band: tc.band}
			assert.InDelta(t, tc.want, executedEVCharge(detail, req, 0, tc.planned, seconds, wire), 1e-6)
		})
	}
}

// TestRobustFloorIntegration replays the 18:06 run against a real companion: the plan,
// robust evaluation and the fixed floor charge must all succeed.
func TestRobustFloorIntegration(t *testing.T) {
	url := os.Getenv("EVCC_ENERGY_SOLVER_TEST_URL")
	if url == "" {
		t.Skip("set EVCC_ENERGY_SOLVER_TEST_URL to the isolated companion")
	}
	client, err := optimizer.NewClientWithResponses(url)
	require.NoError(t, err)
	site := &Site{log: util.NewLogger("floor-test"), optimizerClient: client}
	req := floorRequest()
	for t := range req.TimeSeries.Dt {
		gt, ft := float64(req.TimeSeries.Gt[t]), float64(req.TimeSeries.Ft[t])
		site.energyInsights.Forecast = append(site.energyInsights.Forecast, energyForecastSlot{HomeLowWh: gt * .8, HomeWh: gt, HomeHighWh: gt * 1.3, SolarLowWh: ft * .5, SolarWh: ft, SolarHighWh: ft * 1.2})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := site.solveEnergy(ctx, req)
	require.NoError(t, err)
	planned := base.Batteries[0].ChargingPower[0]
	require.Greater(t, planned, float32(.1))
	require.NotEqual(t, float32(11040.0*537/3600), planned, "the live failure needs a partial floor slot")
	res, summary, err := site.robustEnergy(ctx, req, floorDetails(api.ModePV), base)
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.InDelta(t, (22000.0-20900)/eta, res.Batteries[0].ChargingPower[0], .5)
}

// TestRobustBandHoldIntegration: with a band car charging in the first slot, robust
// evaluation must still succeed and must not apply "normal", which would feed the car.
func TestRobustBandHoldIntegration(t *testing.T) {
	url := os.Getenv("EVCC_ENERGY_SOLVER_TEST_URL")
	if url == "" {
		t.Skip("set EVCC_ENERGY_SOLVER_TEST_URL to the isolated companion")
	}
	client, err := optimizer.NewClientWithResponses(url)
	require.NoError(t, err)
	site := &Site{log: util.NewLogger("band-hold-test"), optimizerClient: client}
	req := floorRequest()
	// below the band reference long enough for a 30-minute run (first slot is 8m57s)
	req.TimeSeries.PN[0], req.TimeSeries.PN[1], req.TimeSeries.PN[2] = .0003, .0003, .0003
	req.Batteries[0].SInitial = 22550 // 41 %, inside the band
	req.Batteries[1].SInitial = 15456 // home battery 80 %
	for i := range req.Batteries {
		req.Batteries[i].PA = terminalStorageValue(.0003) // the horizon minimum
	}
	details := floorDetails(api.ModePV)
	in := liveBandInput()
	in.soc, in.minSoc, in.limitSoc, in.pa = 41, 40, 80, req.Batteries[0].PA
	details.BatteryDetails[0].band = cheapBand(in)
	applyCheapBand(&req.Batteries[0], details.BatteryDetails[0].band)
	require.NotNil(t, req.Batteries[0].BandStorageFeed)
	for t := range req.TimeSeries.Dt {
		gt, ft := float64(req.TimeSeries.Gt[t]), float64(req.TimeSeries.Ft[t])
		site.energyInsights.Forecast = append(site.energyInsights.Forecast, energyForecastSlot{HomeLowWh: gt * .8, HomeWh: gt, HomeHighWh: gt * 1.3, SolarLowWh: ft * .5, SolarWh: ft, SolarHighWh: ft * 1.2})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := site.solveEnergy(ctx, req)
	require.NoError(t, err)
	require.Greater(t, base.Batteries[0].ChargingPower[0], float32(1000), "the car charges now")
	assert.InDelta(t, 0, base.Batteries[1].DischargingPower[0], .1)
	res, summary, err := site.robustEnergy(ctx, req, details, base)
	require.NoError(t, err)
	assert.NotEqual(t, "normal", summary.Selected)
	assert.InDelta(t, 0, res.Batteries[1].DischargingPower[0], .1)
}

// TestRobustBandRunLowSolarIntegration replays review exp1: a band run starts in slot 0 on
// solar (car still StatusB, no run state) and the low-solar scenario leaves the minimum run
// nothing to charge from below the reference. Robust evaluation must not fail the run.
func TestRobustBandRunLowSolarIntegration(t *testing.T) {
	url := os.Getenv("EVCC_ENERGY_SOLVER_TEST_URL")
	if url == "" {
		t.Skip("set EVCC_ENERGY_SOLVER_TEST_URL to the isolated companion")
	}
	client, err := optimizer.NewClientWithResponses(url)
	require.NoError(t, err)
	site := &Site{log: util.NewLogger("band-run-test"), optimizerClient: client}
	pa := float32(0.00027941758)
	req := energyRequest{OptimizationInput: optimizer.OptimizationInput{EtaC: eta, EtaD: eta, Strategy: optimizer.OptimizerStrategy{
		ChargingStrategy: optimizer.OptimizerStrategyChargingStrategy("none"), DischargingStrategy: optimizer.OptimizerStrategyDischargingStrategy("none"),
	}, TimeSeries: optimizer.TimeSeries{
		Dt: []int{627, 900, 900, 900, 900, 900}, Gt: []float32{300, 300, 300, 300, 300, 300},
		Ft: []float32{1200, 1300, 1300, 800, 0, 0}, PN: []float32{.0004, .0004, .0004, .0004, .0004, .0004},
		PE: []float32{.0000786, .0000786, .0000786, .0000786, .0000786, .0000786},
	}}}
	req.Batteries = []energyBattery{
		{BatteryConfig: optimizer.BatteryConfig{ChargeFromGrid: true, CMin: 1380, CMax: 11040, PA: pa, SInitial: 19800, SMax: 55000, SMin: 16500}},
		{BatteryConfig: optimizer.BatteryConfig{ChargeFromGrid: true, CMax: 6000, DMax: 6294.56, PA: pa, SCapacity: 19320, SInitial: 10000, SMax: 18354, SMin: 966}},
	}
	details := requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeVehicle, Name: "calypso", Capacity: 55, mode: api.ModePV}, {Type: batteryTypeBattery, Name: "db:11", Capacity: 19.32}}}
	in := liveBandInput()
	in.pa = pa
	details.BatteryDetails[0].band = cheapBand(in)
	applyCheapBand(&req.Batteries[0], details.BatteryDetails[0].band)
	require.NotNil(t, req.Batteries[0].CMinRun)
	low := []float64{1200, 300, 300, 0, 0, 0}
	for t, ft := range req.TimeSeries.Ft {
		site.energyInsights.Forecast = append(site.energyInsights.Forecast, energyForecastSlot{HomeLowWh: 300, HomeWh: 300, HomeHighWh: 300, SolarLowWh: low[t], SolarWh: float64(ft), SolarHighWh: float64(ft)})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := site.solveEnergy(ctx, req)
	require.NoError(t, err)
	require.Greater(t, base.Batteries[0].ChargingPower[0], float32(100), "the run starts on solar")
	_, summary, err := site.robustEnergy(ctx, req, details, base)
	require.NoError(t, err)
	assert.NotEmpty(t, summary.Evaluations)
}
