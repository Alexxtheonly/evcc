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

func TestRobustFloorPartialEVActionRunsAtFullPower(t *testing.T) {
	req := floorRequest()
	full := 11040.0 * 537 / 3600
	actions, err := executableFirstActions(req, floorDetails(api.ModePV), floorResult(1222.2))
	require.NoError(t, err, "the 18:06 failure")
	require.NotEmpty(t, actions)
	for _, action := range actions {
		assert.InDelta(t, full, *action.request.Batteries[0].FirstStepCharge, 1e-6, "the floor fast-charges: %s", action.name)
	}
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
		{"floor partial", api.ModePV, 20900, nil, 1222, full},
		{"mode now near the limit stores what fits", api.ModeNow, 43000, nil, 900, 1000 / eta},
		{"mode now partial", api.ModeNow, 25000, nil, 1500, full},
		// 900 W of planned surplus at minimum power: tracked by the pv loop, never grid power
		{"pv surplus at minimum power", api.ModePV, 25000, nil, float32(minimum), minimum},
		{"limited grid setpoint", api.ModeMinPV, 25000, nil, 1700, 1700},
		{"float32 just below c_min", api.ModePV, 25000, nil, float32(minimum) - .0001, minimum},
		{"translated to stop", api.ModePV, 25000, nil, 10, 0},
		{"band below reference", api.ModePV, 25000, cheapBand(liveBandInput()), 1700, full},
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
	assert.InDelta(t, 11040.0*537/3600, res.Batteries[0].ChargingPower[0], .5)
}
