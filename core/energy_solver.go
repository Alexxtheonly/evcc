package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/evcc-io/evcc/util/request"
	"github.com/evcc-io/evcc/util/sponsor"
	optimizer "github.com/evcc-io/optimizer/client"
)

type energyBattery struct {
	optimizer.BatteryConfig
	Availability       []bool   `json:"availability,omitempty"`
	EtaC               *float64 `json:"eta_c,omitempty"`
	EtaD               *float64 `json:"eta_d,omitempty"`
	WearCost           *float64 `json:"wear_cost,omitempty"`
	FirstStepCharge    *float64 `json:"first_step_charge,omitempty"`
	FirstStepDischarge *float64 `json:"first_step_discharge,omitempty"`
}

type energyRequest struct {
	optimizer.OptimizationInput
	Batteries []energyBattery `json:"batteries"`
}

func (req energyRequest) ordinary() optimizer.OptimizationInput {
	res := req.OptimizationInput
	res.Batteries = make([]optimizer.BatteryConfig, len(req.Batteries))
	for i, b := range req.Batteries {
		res.Batteries[i] = b.BatteryConfig
	}
	return res
}

func (site *Site) requireEnergyCapabilities(ctx context.Context) error {
	uri := optimizerURI()
	if site.energyCapabilitiesURI == uri && time.Now().Before(site.energyCapabilitiesUntil) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(uri, "/")+"/optimize/capabilities", nil)
	if err != nil {
		return err
	}
	resp, err := request.NewClient(site.log).Do(req)
	if err != nil {
		return fmt.Errorf("energy optimizer capabilities: %w", err)
	}
	defer resp.Body.Close()
	var capabilities struct {
		Version  int      `json:"version"`
		Features []string `json:"features"`
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New("energy optimizer requires the compatible local companion; capabilities unavailable")
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 16<<10)).Decode(&capabilities); err != nil {
		return fmt.Errorf("energy optimizer capabilities: %w", err)
	}
	if capabilities.Version != 1 {
		return errors.New("unsupported energy optimizer capability version")
	}
	for _, name := range []string{"battery_availability", "battery_efficiency", "battery_wear", "fixed_first_step"} {
		if !slices.Contains(capabilities.Features, name) {
			return fmt.Errorf("energy optimizer missing %s capability", name)
		}
	}
	site.energyCapabilitiesURI, site.energyCapabilitiesUntil = uri, time.Now().Add(time.Hour)
	return nil
}

func (site *Site) solveEnergy(ctx context.Context, req energyRequest) (optimizer.OptimizationResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return optimizer.OptimizationResult{}, err
	}
	resp, err := site.optimizerClient.PostOptimizeChargeScheduleWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body), func(_ context.Context, req *http.Request) error {
		if sponsor.IsAuthorized() {
			req.Header.Set("Authorization", "Bearer "+sponsor.Token)
		}
		return nil
	})
	if err != nil {
		return optimizer.OptimizationResult{}, err
	}
	if resp.StatusCode() != http.StatusOK {
		return optimizer.OptimizationResult{}, apiError(resp)
	}
	if resp.JSON200 == nil {
		return optimizer.OptimizationResult{}, errors.New("optimizer returned no schedule")
	}
	res := *resp.JSON200
	if res.Status != optimizer.Optimal && res.Status != optimizer.Feasible {
		return res, fmt.Errorf("optimizer status: %s", res.Status)
	}
	if err := validateEnergyResult(req, res); err != nil {
		return res, err
	}
	return res, nil
}

func validateEnergyResult(req energyRequest, res optimizer.OptimizationResult) error {
	n := len(req.TimeSeries.Dt)
	if n == 0 || len(res.Batteries) != len(req.Batteries) || len(res.GridImport) != n || len(res.GridExport) != n {
		return errors.New("optimizer schedule has incomplete dimensions")
	}
	valid := func(vv []float32) bool {
		if len(vv) != n {
			return false
		}
		for _, v := range vv {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || v < -0.1 {
				return false
			}
		}
		return true
	}
	if !valid(res.GridImport) || !valid(res.GridExport) {
		return errors.New("optimizer returned invalid grid energy")
	}
	for i, b := range res.Batteries {
		if !valid(b.ChargingPower) || !valid(b.DischargingPower) || !valid(b.StateOfCharge) {
			return errors.New("optimizer returned invalid battery energy")
		}
		cfg := req.Batteries[i]
		if (len(cfg.Availability) != 0 && len(cfg.Availability) != n) || len(cfg.SGoal) > n || (cfg.FirstStepCharge == nil) != (cfg.FirstStepDischarge == nil) {
			return errors.New("optimizer battery request has inconsistent dimensions")
		}
		for t, goal := range cfg.SGoal {
			if goal > 0 && b.StateOfCharge[t]+1 < goal {
				return fmt.Errorf("optimizer cannot meet battery %d departure goal", i)
			}
		}
		for j, available := range cfg.Availability {
			if !available && (b.ChargingPower[j] > 0.1 || b.DischargingPower[j] > 0.1) {
				return errors.New("optimizer charged an unavailable vehicle")
			}
		}
		if cfg.FirstStepCharge != nil && (math.Abs(float64(b.ChargingPower[0])-*cfg.FirstStepCharge) > 0.1 || math.Abs(float64(b.DischargingPower[0])-*cfg.FirstStepDischarge) > 0.1) {
			return errors.New("optimizer changed the fixed first action")
		}
	}
	return nil
}

func energyCost(req energyRequest, res optimizer.OptimizationResult) float64 {
	var cost float64
	for t, imp := range res.GridImport {
		cost += float64(imp*req.TimeSeries.PN[t] - res.GridExport[t]*req.TimeSeries.PE[t])
	}
	for i, b := range res.Batteries {
		cfg := req.Batteries[i]
		etaD := float64(req.EtaD)
		if cfg.EtaD != nil {
			etaD = *cfg.EtaD
		}
		if cfg.WearCost != nil {
			for _, discharge := range b.DischargingPower {
				cost += *cfg.WearCost * float64(discharge) / etaD
			}
		}
		cost -= float64(cfg.PA * (b.StateOfCharge[len(b.StateOfCharge)-1] - cfg.SInitial))
	}
	return cost
}

func fixedFirstAction(req energyRequest, res optimizer.OptimizationResult) energyRequest {
	req.Batteries = slices.Clone(req.Batteries)
	for i := range req.Batteries {
		charge, discharge := float64(res.Batteries[i].ChargingPower[0]), float64(res.Batteries[i].DischargingPower[0])
		req.Batteries[i].FirstStepCharge = &charge
		req.Batteries[i].FirstStepDischarge = &discharge
	}
	return req
}

func scenarioRequest(base energyRequest, forecast []energyForecastSlot, highDemand bool) energyRequest {
	res := base
	res.TimeSeries.Gt, res.TimeSeries.Ft = slices.Clone(base.TimeSeries.Gt), slices.Clone(base.TimeSeries.Ft)
	// The observed present is shared. Only unknown future slots vary.
	for t := 1; t < len(forecast); t++ {
		f := forecast[t]
		if highDemand {
			res.TimeSeries.Gt[t] += float32(f.HomeHighWh - f.HomeWh)
			res.TimeSeries.Ft[t] = float32(f.SolarLowWh)
		} else {
			res.TimeSeries.Gt[t] += float32(f.HomeLowWh - f.HomeWh)
			res.TimeSeries.Ft[t] = float32(f.SolarHighWh)
		}
	}
	return res
}

// robustEnergy evaluates at most three joint first actions in three futures (nine solves total).
func (site *Site) robustEnergy(ctx context.Context, req energyRequest, details requestDetails, base optimizer.OptimizationResult) (optimizer.OptimizationResult, *energyScenarios, error) {
	requests := []energyRequest{req, scenarioRequest(req, site.energyInsights.Forecast, true), scenarioRequest(req, site.energyInsights.Forecast, false)}
	results := []optimizer.OptimizationResult{base, {}, {}}
	for i := 1; i < len(results); i++ {
		res, err := site.solveEnergy(ctx, requests[i])
		if err != nil {
			return base, nil, fmt.Errorf("scenario envelope: %w", err)
		}
		results[i] = res
	}
	costs := make([][]float64, len(results))
	paths := make([][]optimizer.OptimizationResult, len(results))
	for candidate := range results {
		costs[candidate], paths[candidate] = make([]float64, 3), make([]optimizer.OptimizationResult, 3)
		for scenario := range requests {
			res := results[scenario]
			fixed := fixedFirstAction(requests[scenario], results[candidate])
			if scenario != candidate {
				var err error
				res, err = site.solveEnergy(ctx, fixed)
				if err != nil {
					return base, nil, fmt.Errorf("common first action %d, scenario %d: %w", candidate, scenario, err)
				}
			}
			costs[candidate][scenario] = energyCost(fixed, res)
			paths[candidate][scenario] = res
		}
	}
	selected := minimaxRegret(costs)
	if selected < 0 {
		return base, nil, errors.New("scenario costs unavailable")
	}
	names := []string{"base", "highDemandLowSolar", "lowDemandHighSolar"}
	info := &energyScenarios{Status: "evaluated", Selected: names[selected]}
	for i, row := range costs {
		info.Evaluations = append(info.Evaluations, energyScenarioEvaluation{Action: names[i], Costs: row})
	}
	low, high := slices.Min(costs[selected]), slices.Max(costs[selected])
	info.CostLow, info.CostHigh = &low, &high
	var capacity float64
	for _, detail := range details.BatteryDetails {
		if detail.Type == batteryTypeBattery {
			capacity += detail.Capacity
		}
	}
	if capacity > 0 {
		for t := range req.TimeSeries.Dt {
			low, high := math.Inf(1), math.Inf(-1)
			for _, path := range paths[selected] {
				var stored float64
				for i, detail := range details.BatteryDetails {
					if detail.Type == batteryTypeBattery {
						stored += float64(path.Batteries[i].StateOfCharge[t])
					}
				}
				soc := stored / (capacity * 10)
				low, high = min(low, soc), max(high, soc)
			}
			info.BatterySocLow = append(info.BatterySocLow, low)
			info.BatterySocHigh = append(info.BatterySocHigh, high)
		}
	}
	return paths[selected][0], info, nil
}
