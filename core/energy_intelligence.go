package core

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/server/db/settings"
)

const energyIntelligenceKey = "energyIntelligence"

type EnergyIntelligenceSettings struct {
	Robust               bool                               `json:"robust"`
	Arrivals             bool                               `json:"arrivals"`
	UseLearnedEfficiency bool                               `json:"useLearnedEfficiency"`
	BatteryWear          map[string]float64                 `json:"batteryWear"`
	BatteryEnergyPlane   map[string]string                  `json:"batteryEnergyPlane"`
	BatteryEfficiency    map[string]EnergyEfficiencySetting `json:"batteryEfficiency"`
	SettlementMode       string                             `json:"settlementMode"`
	SettlementFrom       *time.Time                         `json:"settlementFrom"`
}

type EnergyEfficiencySetting struct {
	ChargeEfficiency    *float64 `json:"chargeEfficiency"`
	DischargeEfficiency *float64 `json:"dischargeEfficiency"`
}

func (cfg EnergyIntelligenceSettings) validate() error {
	if cfg.SettlementMode != "simulation" && cfg.SettlementMode != "interval" {
		return errors.New("settlementMode must be simulation or interval")
	}
	if len(cfg.BatteryWear) > 64 {
		return errors.New("too many battery wear entries")
	}
	for name, value := range cfg.BatteryWear {
		if name == "" || len(name) > 128 || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
			return fmt.Errorf("invalid battery wear for %q: expected 0..100 currency per DC kWh", name)
		}
	}
	for name, plane := range cfg.BatteryEnergyPlane {
		if name == "" || (plane != "ac" && plane != "dc" && plane != "unknown") {
			return fmt.Errorf("invalid battery energy plane for %q", name)
		}
	}
	for name, efficiency := range cfg.BatteryEfficiency {
		for _, value := range []*float64{efficiency.ChargeEfficiency, efficiency.DischargeEfficiency} {
			if name == "" || value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value <= 0 || *value > 1 {
				return fmt.Errorf("invalid efficiencies for %q: charge and discharge must be in (0,1]", name)
			}
		}
	}
	return nil
}

func (site *Site) GetEnergyIntelligenceSettings() EnergyIntelligenceSettings {
	cfg := EnergyIntelligenceSettings{SettlementMode: "simulation", BatteryWear: map[string]float64{}}
	if err := settings.Json(energyIntelligenceKey, &cfg); err != nil && !errors.Is(err, settings.ErrNotFound) {
		site.log.ERROR.Printf("energy intelligence settings: %v", err)
		return EnergyIntelligenceSettings{SettlementMode: "simulation", BatteryWear: map[string]float64{}}
	}
	if err := cfg.validate(); err != nil {
		site.log.ERROR.Printf("energy intelligence settings: %v", err)
		return EnergyIntelligenceSettings{SettlementMode: "simulation", BatteryWear: map[string]float64{}}
	}
	return cfg
}

func (site *Site) SetEnergyIntelligenceSettings(cfg EnergyIntelligenceSettings) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	names := make(map[string]bool)
	for name := range cfg.BatteryWear {
		names[name] = true
	}
	for name := range cfg.BatteryEnergyPlane {
		names[name] = true
	}
	for name := range cfg.BatteryEfficiency {
		names[name] = true
	}
	for name := range names {
		found := false
		for _, dev := range site.batteryMeters {
			found = found || dev.Config().Name == name
		}
		if !found {
			return fmt.Errorf("unknown home battery %q", name)
		}
	}
	if err := settings.SetJson(energyIntelligenceKey, cfg); err != nil {
		return err
	}
	site.Lock()
	site.energyEfficiencyRefresh = true
	site.Unlock()
	site.publish(energyIntelligenceKey, cfg)
	site.Optimize()
	return nil
}

type energyForecastSlot struct {
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	HomeLowWh   float64   `json:"homeLowWh"`
	HomeWh      float64   `json:"homeWh"`
	HomeHighWh  float64   `json:"homeHighWh"`
	SolarLowWh  float64   `json:"solarLowWh"`
	SolarWh     float64   `json:"solarWh"`
	SolarHighWh float64   `json:"solarHighWh"`
	GridPrice   float64   `json:"gridPrice"`
}

type energyPlanSlot struct {
	Start       time.Time `json:"start"`
	ChargeWh    float64   `json:"chargeWh"`
	DischargeWh float64   `json:"dischargeWh"`
	Soc         float64   `json:"soc"`
}

type energyDevice struct {
	Key         string           `json:"key"`
	Name        string           `json:"name"`
	Title       string           `json:"title"`
	Kind        string           `json:"kind"`
	CapacityKWh float64          `json:"capacityKWh"`
	InitialSoc  *float64         `json:"initialSoc,omitempty"`
	Arrival     *time.Time       `json:"arrival,omitempty"`
	Departure   *time.Time       `json:"departure,omitempty"`
	Plan        []energyPlanSlot `json:"plan"`
}

type energyEconomics struct {
	Name                         string   `json:"name"`
	ChargeEfficiency             float64  `json:"chargeEfficiency"`
	DischargeEfficiency          float64  `json:"dischargeEfficiency"`
	Source                       string   `json:"source"`
	CandidateChargeEfficiency    *float64 `json:"candidateChargeEfficiency,omitempty"`
	CandidateDischargeEfficiency *float64 `json:"candidateDischargeEfficiency,omitempty"`
	WearPerKWh                   *float64 `json:"wearPerKWh,omitempty"`
	CalibrationReason            string   `json:"calibrationReason,omitempty"`
	MeasurementPlane             string   `json:"measurementPlane,omitempty"`
}

type energyScenarioEvaluation struct {
	Action string    `json:"action"`
	Costs  []float64 `json:"costs"`
}

type energyScenarios struct {
	Status         string                     `json:"status"`
	Reason         string                     `json:"reason,omitempty"`
	Selected       string                     `json:"selected,omitempty"`
	Evaluations    []energyScenarioEvaluation `json:"evaluations,omitempty"`
	CostLow        *float64                   `json:"costLow,omitempty"`
	CostHigh       *float64                   `json:"costHigh,omitempty"`
	BatterySocLow  []float64                  `json:"batterySocLow,omitempty"`
	BatterySocHigh []float64                  `json:"batterySocHigh,omitempty"`
}

type optimizerInsights struct {
	Updated    time.Time                  `json:"updated"`
	Automatic  bool                       `json:"automatic"`
	Status     string                     `json:"status"`
	Reason     string                     `json:"reason,omitempty"`
	Settings   EnergyIntelligenceSettings `json:"settings"`
	Profile    *metrics.ProfileQuality    `json:"profile,omitempty"`
	Forecast   []energyForecastSlot       `json:"forecast,omitempty"`
	Devices    []energyDevice             `json:"devices,omitempty"`
	Economics  []energyEconomics          `json:"economics,omitempty"`
	Scenarios  *energyScenarios           `json:"scenarios,omitempty"`
	SnapshotID *uint64                    `json:"snapshotId,omitempty"`
}

func (site *Site) publishEnergyInsights(err error) {
	value := site.energyInsights
	value.Updated = time.Now()
	value.Automatic = site.Automatic()
	value.Settings = site.GetEnergyIntelligenceSettings()
	if err != nil {
		value.Status, value.Reason = "unavailable", err.Error()
		value.Devices = nil
		value.SnapshotID = nil
	} else if value.Status == "" {
		value.Status = "ready"
	}
	site.publish("optimizerInsights", value)
}

// minimaxRegret compares each fixed first action against the best action in each future.
func minimaxRegret(costs [][]float64) int {
	if len(costs) == 0 || len(costs[0]) == 0 {
		return -1
	}
	best := make([]float64, len(costs[0]))
	for j := range best {
		best[j] = math.Inf(1)
		for _, row := range costs {
			if len(row) != len(best) || math.IsNaN(row[j]) || math.IsInf(row[j], 0) {
				return -1
			}
			best[j] = min(best[j], row[j])
		}
	}
	selected, score := -1, math.Inf(1)
	for i, row := range costs {
		regret := 0.0
		for j, cost := range row {
			regret = max(regret, cost-best[j])
		}
		if regret < score {
			selected, score = i, regret
		}
	}
	return selected
}
