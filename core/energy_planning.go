package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	optimizer "github.com/evcc-io/optimizer/client"
)

func (site *Site) prepareEnergy(req optimizer.OptimizationInput, details *requestDetails) (energyRequest, error) {
	res := energyRequest{OptimizationInput: req}
	for _, bat := range req.Batteries {
		res.Batteries = append(res.Batteries, energyBattery{BatteryConfig: bat})
	}
	if err := site.energyForecast(req, *details); err != nil {
		return res, err
	}
	cfg := site.energyInsights.Settings
	for _, detail := range details.BatteryDetails {
		if detail.Type != batteryTypeBattery {
			continue
		}
		plane := cfg.BatteryEnergyPlane[detail.Name]
		if plane == "" {
			plane = "unknown"
		}
		if err := metrics.SetBatteryMeasurementPlane(detail.Name, plane); err != nil {
			return res, err
		}
	}
	site.Lock()
	refresh := site.energyEfficiencyRefresh
	site.energyEfficiencyRefresh = false
	site.Unlock()
	if refresh || time.Since(site.energyEfficiencyUpdated) > time.Hour {
		candidates, err := metrics.BatteryEfficiencyCandidates(time.Now().AddDate(0, 0, -30))
		if err != nil {
			return res, fmt.Errorf("battery efficiency history: %w", err)
		}
		site.energyEfficiencyCandidates, site.energyEfficiencyUpdated = candidates, time.Now()
	}
	for i, detail := range details.BatteryDetails {
		if detail.Type != batteryTypeBattery {
			continue
		}
		economics := energyEconomics{Name: detail.Name, ChargeEfficiency: eta, DischargeEfficiency: eta, Source: "default"}
		for _, candidate := range site.energyEfficiencyCandidates {
			if candidate.Name != detail.Name {
				continue
			}
			economics.CandidateChargeEfficiency, economics.CandidateDischargeEfficiency = candidate.ChargeEfficiency, candidate.DischargeEfficiency
			economics.CalibrationReason, economics.MeasurementPlane = candidate.Source, candidate.MeasurementPlane
			if cfg.UseLearnedEfficiency && candidate.Applicable && time.Since(candidate.To) <= 7*24*time.Hour {
				if candidate.ChargeEfficiency != nil {
					economics.ChargeEfficiency = *candidate.ChargeEfficiency
					economics.Source = "measured"
				}
				if candidate.DischargeEfficiency != nil {
					economics.DischargeEfficiency = *candidate.DischargeEfficiency
					economics.Source = "measured"
				}
			}
		}
		if override, ok := cfg.BatteryEfficiency[detail.Name]; ok {
			economics.ChargeEfficiency, economics.DischargeEfficiency = *override.ChargeEfficiency, *override.DischargeEfficiency
			economics.Source = "configured"
		}
		if _, override := cfg.BatteryEfficiency[detail.Name]; cfg.UseLearnedEfficiency || override {
			res.Batteries[i].EtaC, res.Batteries[i].EtaD = &economics.ChargeEfficiency, &economics.DischargeEfficiency
		}
		if wear, ok := cfg.BatteryWear[detail.Name]; ok {
			economics.WearPerKWh = &wear
			perWh := wear / 1000
			res.Batteries[i].WearCost = &perWh
		}
		details.BatteryDetails[i].etaC, details.BatteryDetails[i].etaD = economics.ChargeEfficiency, economics.DischargeEfficiency
		details.BatteryDetails[i].wearPerKWh = economics.WearPerKWh
		site.energyInsights.Economics = append(site.energyInsights.Economics, economics)
	}
	if cfg.Arrivals {
		if err := site.addExpectedVehicles(&res, details); err != nil {
			return res, err
		}
	}
	return res, nil
}

func (site *Site) energyForecast(req optimizer.OptimizationInput, details requestDetails) error {
	if site.energyProfile == nil || len(site.energyProfile.Rates) != len(req.TimeSeries.Dt) {
		return errors.New("household forecast dimensions unavailable")
	}
	issued := time.Now()
	archive := make([]metrics.HomeForecastSlot, 0, len(details.Timestamps))
	for i, start := range details.Timestamps {
		raw := site.energyProfile.Rates[i]
		fraction := float64(req.TimeSeries.Dt[i]) / tariff.SlotDuration.Seconds()
		base := site.energyHome[i] * fraction
		shift := base - raw.Base*fraction
		low := max(0, min(base, raw.Low*fraction+shift))
		high := max(base, raw.High*fraction+shift)
		solar := float64(req.TimeSeries.Ft[i])
		solarLow, solarHigh := solar, solar
		if solar > 0 {
			var err error
			solarLow, solarHigh, _, err = metrics.SolarForecastRange(issued, int(start.Sub(issued).Minutes()), solar)
			if err != nil {
				return fmt.Errorf("solar forecast envelope: %w", err)
			}
		}
		site.energyInsights.Forecast = append(site.energyInsights.Forecast, energyForecastSlot{
			Start: start, End: start.Add(time.Duration(req.TimeSeries.Dt[i]) * time.Second),
			HomeLowWh: low, HomeWh: base, HomeHighWh: high,
			SolarLowWh: solarLow, SolarWh: solar, SolarHighWh: solarHigh,
			GridPrice: float64(req.TimeSeries.PN[i]) * 1e3,
		})
		if i > 0 {
			archive = append(archive, metrics.HomeForecastSlot{Start: start, Low: low, Base: base, High: high})
		}
	}
	return metrics.ArchiveHomeForecast(issued, archive)
}

func (site *Site) optimizeEnergy(req optimizer.OptimizationInput, details requestDetails) error {
	extended, err := site.prepareEnergy(req, &details)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cfg := site.energyInsights.Settings
	if optimizerURI() != OPTIMIZER_URI || cfg.Robust || cfg.Arrivals || cfg.UseLearnedEfficiency || len(cfg.BatteryWear) > 0 || len(cfg.BatteryEfficiency) > 0 {
		if err := site.requireEnergyCapabilities(ctx); err != nil {
			return err
		}
	}
	res, err := site.solveEnergy(ctx, extended)
	if err != nil {
		status := string(res.Status)
		if status == "" {
			status = "Error"
		}
		site.persistOptimizerRun(status, res)
		return err
	}
	if cfg.Robust {
		res, site.energyInsights.Scenarios, err = site.robustEnergy(ctx, extended, details, res)
		if err != nil {
			site.energyInsights.Scenarios = &energyScenarios{Status: "unavailable", Reason: err.Error()}
			return err
		}
	}
	firstEnd := details.Timestamps[0].Add(time.Duration(req.TimeSeries.Dt[0]) * time.Second)
	if err := validateEnergyPlanInputs(firstEnd, time.Now(), cfg, site.GetEnergyIntelligenceSettings()); err != nil {
		return err
	}
	if err := site.validateEnergyDevices(details); err != nil {
		return err
	}
	site.persistOptimizerRun(string(res.Status), res)
	ordinary := extended.ordinary()
	site.populateEnergyDevices(extended, details, res)
	if err := site.archiveEnergyRun(extended, details, res); err != nil {
		return err
	}
	site.populateEnergyGrid(ordinary, res)
	site.publish("evopt", optimizerResult{Updated: time.Now(), Req: ordinary, Res: res, Details: details})
	site.applyOptimizerResult(ordinary, details.BatteryDetails, res)
	return nil
}

func validateEnergyPlanInputs(firstEnd, now time.Time, planned, current EnergyIntelligenceSettings) error {
	if !firstEnd.After(now) {
		return fmt.Errorf("optimizer schedule expired before completion: %w", errOptimizerReplan)
	}
	if !reflect.DeepEqual(planned, current) {
		return fmt.Errorf("energy settings changed while optimizing; waiting for a fresh plan: %w", errOptimizerReplan)
	}
	return nil
}

func (site *Site) validateEnergyDevices(details requestDetails) error {
	for _, detail := range details.BatteryDetails {
		if detail.loadpoint == nil || detail.Type != batteryTypeVehicle {
			continue
		}
		id := *detail.loadpoint
		if id < 0 || id >= len(site.loadpoints) || site.loadpoints[id] == nil {
			return fmt.Errorf("optimizer loadpoint disappeared: %w", errOptimizerReplan)
		}
		lp := site.loadpoints[id]
		if lp.GetStatus() != api.StatusB && lp.GetStatus() != api.StatusC {
			return fmt.Errorf("vehicle disconnected while optimizing: %w", errOptimizerReplan)
		}
		for _, v := range site.Vehicles().Settings() {
			if v.Name() == detail.Name && v.Instance() != lp.GetVehicle() {
				return fmt.Errorf("vehicle assignment changed while optimizing: %w", errOptimizerReplan)
			}
		}
	}
	return nil
}

func (site *Site) populateEnergyDevices(req energyRequest, details requestDetails, res optimizer.OptimizationResult) {
	for i, detail := range details.BatteryDetails {
		bat := req.Batteries[i]
		device := energyDevice{Key: detail.key(), Name: detail.Name, Title: detail.Title, Kind: string(detail.Type), CapacityKWh: detail.Capacity}
		if detail.arrival != nil {
			device.Key, device.Kind = "expected:"+detail.Name, "expectedVehicle"
			device.Arrival, device.Departure = detail.arrival, detail.departure
		}
		if detail.Capacity > 0 {
			soc := float64(bat.SInitial) / (detail.Capacity * 10)
			device.InitialSoc = &soc
		}
		for t, start := range details.Timestamps {
			b := res.Batteries[i]
			soc := 0.0
			if detail.Capacity > 0 {
				soc = float64(b.StateOfCharge[t]) / (detail.Capacity * 10)
			}
			device.Plan = append(device.Plan, energyPlanSlot{Start: start, ChargeWh: float64(b.ChargingPower[t]), DischargeWh: float64(b.DischargingPower[t]), Soc: soc})
		}
		site.energyInsights.Devices = append(site.energyInsights.Devices, device)
	}
}

func (site *Site) populateEnergyGrid(req optimizer.OptimizationInput, res optimizer.OptimizationResult) {
	for t := range site.energyInsights.Forecast {
		var charge, discharge float64
		for _, battery := range res.Batteries {
			charge += max(0, float64(battery.ChargingPower[t]))
			discharge += max(0, float64(battery.DischargingPower[t]))
		}
		imp := max(0, float64(energyGridImport(req, res, t)))
		// AC-side Wh bounds describe source allocation, not forecast uncertainty or per-device routing.
		low := max(0, charge-float64(req.TimeSeries.Ft[t])-discharge)
		high := min(charge, imp)
		if low > high+0.1 {
			continue
		}
		low = min(low, high)
		slot := &site.energyInsights.Forecast[t]
		slot.GridImportWh, slot.GridChargeMinWh, slot.GridChargeMaxWh = &imp, &low, &high
	}
}

func (site *Site) archiveEnergyRun(req energyRequest, details requestDetails, res optimizer.OptimizationResult) error {
	stamp := time.Now()
	slot := stamp.Truncate(tariff.SlotDuration)
	var action string
	for i, detail := range details.BatteryDetails {
		s := currentSlotSuggestion(detail, req.Batteries[i].BatteryConfig, res.Batteries[i], energyGridImport(req.OptimizationInput, res, 0), res.GridExport[0], float64(req.TimeSeries.Dt[0])/3600)
		action += detail.Name + ":" + s.Action + ";"
	}
	site.RLock()
	previousSnapshot := site.energySnapshotID
	site.RUnlock()
	var batteries []metrics.SnapshotBatteryEconomics
	for i, detail := range details.BatteryDetails {
		if detail.Type != batteryTypeBattery {
			continue
		}
		b := req.Batteries[i]
		floor := 0.0
		ceiling := 1.0
		if detail.Capacity > 0 {
			floor = float64(b.SMin) / (detail.Capacity * 1000)
			ceiling = float64(b.SMax) / (detail.Capacity * 1000)
		}
		batteries = append(batteries, metrics.SnapshotBatteryEconomics{Name: detail.Name, CapacityKWh: detail.Capacity,
			EtaC: detail.etaC, EtaD: detail.etaD, FloorFrac: floor, ChargeCeilingFrac: &ceiling,
			MaxChargeKWh: float64(b.CMax) / 4000, MaxDischargeKWh: float64(b.DMax) / 4000,
			WearPerKWh: detail.wearPerKWh, Source: "decision_snapshot", MeasurementPlane: site.energyInsights.Settings.BatteryEnergyPlane[detail.Name]})
	}
	values := []any{req, res, struct {
		Batteries []metrics.SnapshotBatteryEconomics `json:"batteries"`
	}{batteries}, site.energyInsights}
	encoded := make([]json.RawMessage, len(values))
	for i, v := range values {
		value, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("optimizer snapshot: %w", err)
		}
		encoded[i] = value
	}
	// Freeze material configuration/goals, but not inventory, forecasts or the
	// shrinking current interval: those routinely change on every control tick.
	identityReq := req
	identityReq.Batteries = append([]energyBattery(nil), req.Batteries...)
	identityReq.TimeSeries = optimizer.TimeSeries{}
	var identities []any
	for i := range identityReq.Batteries {
		identityReq.Batteries[i].SInitial = 0
		identityReq.Batteries[i].PDemand = nil
		identityReq.Batteries[i].FirstStepCharge = nil
		identityReq.Batteries[i].FirstStepDischarge = nil
		d := details.BatteryDetails[i]
		identities = append(identities, []any{d.Type, d.Name, d.loadpoint, d.controllable, d.arrival, d.departure})
	}
	identity, err := json.Marshal([]any{identityReq, identities, encoded[2], site.energyInsights.Settings})
	if err != nil {
		return err
	}
	changed := site.energySnapshotAssumptions != "" && slot.Equal(site.energySnapshotSlot) && string(identity) != site.energySnapshotAssumptions
	if previousSnapshot != nil && slot.Equal(site.energySnapshotSlot) && action == site.energySnapshotAction && !changed {
		site.energyInsights.SnapshotID = previousSnapshot
		return nil
	}
	if changed {
		site.Lock()
		site.energyUnpricedSlot = slot
		site.Unlock()
		if err := metrics.InvalidateControlSlotSnapshot(slot); err != nil {
			return err
		}
	}
	version := util.Version
	if version == "" {
		version = "energy-intelligence-v1"
	}
	id, err := metrics.SaveOptimizerSnapshot(metrics.OptimizerSnapshot{Timestamp: stamp, ControllerVersion: version,
		Request: encoded[0], Result: encoded[1], Economics: encoded[2], Quality: encoded[3]})
	if err != nil {
		return err
	}
	site.Lock()
	site.energySnapshotID = &id
	site.Unlock()
	site.energyInsights.SnapshotID = &id
	site.energySnapshotSlot, site.energySnapshotAction = slot, action
	site.energySnapshotAssumptions = string(identity)
	return metrics.BindOptimizerRunSnapshot(slot, id)
}

func energyPayback(pn, soc, discharge []float32, initial float32, etaC, etaD, wear float64) bool {
	if etaC <= 0 || etaD <= 0 || math.IsNaN(etaC) || math.IsNaN(etaD) {
		return false
	}
	if len(pn) < 2 {
		return false
	}
	price := float64(pn[0]) * 1e3
	if price <= 0 && wear == 0 {
		return true
	}
	var value, energy float64
	for t := 1; t < min(len(pn), len(soc), len(discharge)); t++ {
		value += float64(discharge[t]) * (float64(pn[t])*1e3 - wear/etaD)
		energy += float64(discharge[t])
		if soc[t] <= initial {
			return energy > 0 && value/energy >= (price+chargePaybackBuffer)/(etaC*etaD)
		}
	}
	return false
}
