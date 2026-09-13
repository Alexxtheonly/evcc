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

	"github.com/evcc-io/evcc/core/types"
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

var errEnergyInfeasible = errors.New("energy schedule infeasible")

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
		if resp.StatusCode() == http.StatusUnprocessableEntity {
			return optimizer.OptimizationResult{}, fmt.Errorf("%w: %v", errEnergyInfeasible, apiError(resp))
		}
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
	if len(res.GridImportOvershoot) != 0 && !valid(res.GridImportOvershoot) {
		return errors.New("optimizer returned invalid import overshoot")
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
				return fmt.Errorf("%w: battery %d departure goal", errEnergyInfeasible, i)
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

func energyGridImport(req optimizer.OptimizationInput, res optimizer.OptimizationResult, t int) float32 {
	imp := res.GridImport[t]
	if (req.Grid.PMaxImp == 0 || req.Grid.PrcPExcImp == 0) && t < len(res.GridImportOvershoot) {
		imp += res.GridImportOvershoot[t]
	}
	return imp
}

func energyCost(req energyRequest, res optimizer.OptimizationResult) float64 {
	var cost float64
	demandRate := req.Grid.PMaxImp != 0 && req.Grid.PrcPExcImp != 0
	var peakOvershoot float64
	for t := range res.GridImport {
		imp := energyGridImport(req.OptimizationInput, res, t)
		if t < len(res.GridImportOvershoot) {
			if demandRate && t < len(req.TimeSeries.Dt) && req.TimeSeries.Dt[t] > 0 {
				peakOvershoot = max(peakOvershoot, float64(res.GridImportOvershoot[t])*3600/float64(req.TimeSeries.Dt[t]))
			}
		}
		cost += float64(imp*req.TimeSeries.PN[t] - res.GridExport[t]*req.TimeSeries.PE[t])
	}
	cost += peakOvershoot * float64(req.Grid.PrcPExcImp)
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

type energyFirstAction struct {
	name    string
	request energyRequest
}

func executableFirstActions(req energyRequest, details requestDetails, base optimizer.OptimizationResult) ([]energyFirstAction, error) {
	if len(req.TimeSeries.Dt) == 0 || len(details.BatteryDetails) != len(req.Batteries) {
		return nil, errors.New("executable actions require complete device details")
	}
	index := -1
	net := float64(req.TimeSeries.Gt[0] - req.TimeSeries.Ft[0])
	hours := float64(req.TimeSeries.Dt[0]) / 3600
	for i, detail := range details.BatteryDetails {
		if detail.Type == batteryTypeBattery {
			if index >= 0 {
				return nil, errors.New("robust dispatch requires one home battery: shared inverter allocation is unknown")
			}
			index = i
			continue
		}
		charge := float64(base.Batteries[i].ChargingPower[0])
		// Full power and stop have a stable EV command. A partial command can
		// instead invoke PV tracking, phase switching or minimum-current gates.
		if charge > .1 && charge < float64(req.Batteries[i].CMax)*hours-.1 {
			return nil, errors.New("robust dispatch cannot fix a partial EV first action: PV tracking and current gates remain authoritative")
		}
		net += charge - float64(base.Batteries[i].DischargingPower[0])
	}
	if index < 0 {
		return nil, errors.New("robust battery-mode evaluation requires a home battery")
	}
	bat := req.Batteries[index]
	etaC, etaD := float64(req.EtaC), float64(req.EtaD)
	if bat.EtaC != nil {
		etaC = *bat.EtaC
	}
	if bat.EtaD != nil {
		etaD = *bat.EtaD
	}
	chargeMax := max(0, min(float64(bat.CMax)*hours, float64(bat.SMax-bat.SInitial)/etaC))
	capacity := max(bat.SCapacity, bat.SMax)
	solarChargeMax := max(0, min(float64(bat.CMax)*hours, float64(capacity-bat.SInitial)/etaC))
	dischargeMax := max(0, min(float64(bat.DMax)*hours, float64(bat.SInitial-bat.SMin)*etaD))
	var actions []energyFirstAction
	for _, mode := range []string{"normal", "hold", "holdcharge", "charge"} {
		charge, discharge := 0.0, 0.0
		if mode != "holdcharge" {
			charge = min(max(0, -net), solarChargeMax)
		}
		if mode == "normal" || mode == "holdcharge" {
			discharge = min(max(0, net), dischargeMax)
		}
		if mode == "charge" {
			if !bat.ChargeFromGrid || chargeMax == 0 {
				continue
			}
			charge = chargeMax
		}
		// SMax is the grid-charge ceiling. Normal PV charging can go to
		// physical capacity; omit a mode the current solver cannot represent
		// rather than pretending the inverter stops harvesting at that ceiling.
		if charge > chargeMax+.1 {
			continue
		}
		duplicate := false
		for _, action := range actions {
			prior := action.request.Batteries[index]
			duplicate = duplicate || (math.Abs(*prior.FirstStepCharge-charge) < .1 && math.Abs(*prior.FirstStepDischarge-discharge) < .1)
		}
		if duplicate {
			continue
		}
		fixed := fixedFirstAction(req, base)
		fixed.Batteries[index].FirstStepCharge, fixed.Batteries[index].FirstStepDischarge = &charge, &discharge
		actions = append(actions, energyFirstAction{mode, fixed})
	}
	return actions, nil
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

// robustEnergy evaluates at most three distinct executable mode vectors in three
// futures (nine constrained solves, plus the initial unconstrained EV plan).
func (site *Site) robustEnergy(ctx context.Context, req energyRequest, details requestDetails, base optimizer.OptimizationResult) (optimizer.OptimizationResult, *energyScenarios, error) {
	requests := []energyRequest{req, scenarioRequest(req, site.energyInsights.Forecast, true), scenarioRequest(req, site.energyInsights.Forecast, false)}
	actions, err := executableFirstActions(req, details, base)
	if err != nil {
		return base, nil, err
	}
	var costs [][]float64
	var paths [][]optimizer.OptimizationResult
	var names []string
	for _, action := range actions {
		row, path := make([]float64, 3), make([]optimizer.OptimizationResult, 3)
		feasible := true
		for scenario := range requests {
			fixed := requests[scenario]
			fixed.Batteries = action.request.Batteries
			res, err := site.solveEnergy(ctx, fixed)
			if err != nil {
				if !errors.Is(err, errEnergyInfeasible) {
					return base, nil, err
				}
				feasible = false
				break // e.g. hold cannot meet an immediate explicit goal
			}
			// Reject mode translations that cannot execute this vector.
			suggestions := make(map[string]types.Suggestion)
			for i, detail := range details.BatteryDetails {
				if detail.Type != batteryTypeBattery {
					continue
				}
				s := currentSlotSuggestion(detail, fixed.Batteries[i].BatteryConfig, res.Batteries[i], energyGridImport(fixed.OptimizationInput, res, 0), res.GridExport[0], float64(fixed.TimeSeries.Dt[0])/3600)
				if s.Action != action.name {
					feasible = false
				}
				suggestions[detail.key()] = s
			}
			if scenario == 0 && batteryModeCandidate(suggestions, fixed.ordinary(), res, details.BatteryDetails).vetoReason != vetoReasonNone {
				feasible = false
			}
			if !feasible {
				break
			}
			row[scenario] = energyCost(fixed, res)
			path[scenario] = res
		}
		if feasible {
			costs, paths, names = append(costs, row), append(paths, path), append(names, action.name)
		}
	}
	selected := minimaxRegret(costs)
	if selected < 0 {
		return base, nil, errors.New("scenario costs unavailable")
	}
	info := &energyScenarios{Status: "evaluated", Selected: names[selected], Reason: "Executable battery modes at current modeled net demand; EV full-power/stop commands held fixed. Forecast changes and hardware safety gates remain authoritative."}
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
