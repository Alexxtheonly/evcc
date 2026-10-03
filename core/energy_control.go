package core

import (
	"math"
	"time"

	"github.com/evcc-io/evcc/api"
)

const energySwitchingFraction = 0.003

type energyControlEvaluation struct {
	Action string    `json:"action"`
	Costs  []float64 `json:"costs"`
	Regret float64   `json:"regret"`
}

type energyControlEconomics struct {
	validUntil      time.Time
	BestAction      string                    `json:"bestAction"`
	SelectedAction  string                    `json:"selectedAction"`
	PreferredAction string                    `json:"preferredAction,omitempty"`
	RegretGap       *float64                  `json:"regretGap,omitempty"`
	Materiality     *float64                  `json:"materiality,omitempty"`
	Reason          string                    `json:"reason"`
	Evaluations     []energyControlEvaluation `json:"evaluations"`
}

func (site *Site) optimizerTime() time.Time {
	if site.optimizerNow != nil {
		return site.optimizerNow()
	}
	return time.Now()
}

func (site *Site) energyPreferredAction() string {
	site.RLock()
	defer site.RUnlock()
	age := site.optimizerTime().Sub(site.optimizerBatteryModeUpdated)
	applied := site.batteryAppliedModeLocked()
	if !site.Automatic() || !site.optimizerModeFresh(age) || applied != site.optimizerBatteryMode || site.batteryModeExternal != api.BatteryUnknown {
		return ""
	}
	if applied == api.BatteryUnknown {
		return ""
	}
	return applied.String()
}

func (site *Site) optimizerModeFresh(age time.Duration) bool {
	return age >= 0 && age < optimizerBatteryModeValidity &&
		(site.optimizerModeExpiresAt.IsZero() || site.optimizerTime().Before(site.optimizerModeExpiresAt))
}

func energyActionMateriality(req energyRequest, a, b energyFirstAction) float64 {
	var transfer float64
	for i := range a.request.Batteries {
		x, y := a.request.Batteries[i], b.request.Batteries[i]
		transfer += math.Abs(*x.FirstStepCharge-*y.FirstStepCharge) + math.Abs(*x.FirstStepDischarge-*y.FirstStepDischarge)
	}
	var scale, low, high float64
	for _, price := range req.TimeSeries.PN {
		p := float64(price)
		scale, low, high = max(scale, math.Abs(p)), min(low, p), max(high, p)
	}
	return transfer * max(scale, high-low) * energySwitchingFraction
}

func selectEnergyAction(req energyRequest, actions []energyFirstAction, costs [][]float64, preferred string) (int, *energyControlEconomics) {
	best := minimaxRegret(costs)
	if best < 0 || len(actions) != len(costs) {
		return -1, nil
	}
	minima := make([]float64, len(costs[0]))
	for j := range minima {
		minima[j] = costs[0][j]
		for _, row := range costs {
			minima[j] = min(minima[j], row[j])
		}
	}
	info := &energyControlEconomics{BestAction: actions[best].name, PreferredAction: preferred, Reason: "minimumRegret"}
	preference := -1
	for i, row := range costs {
		var regret float64
		for j, cost := range row {
			regret = max(regret, cost-minima[j])
		}
		info.Evaluations = append(info.Evaluations, energyControlEvaluation{actions[i].name, row, regret})
		if actions[i].name == preferred {
			preference = i
		}
	}
	selected := best
	if preference >= 0 {
		gap := info.Evaluations[preference].Regret - info.Evaluations[best].Regret
		margin := energyActionMateriality(req, actions[preference], actions[best])
		if math.IsNaN(margin) || math.IsInf(margin, 0) {
			return -1, nil
		}
		info.RegretGap, info.Materiality = &gap, &margin
		if gap <= margin {
			selected = preference
			if selected != best {
				info.Reason = "economicTie"
			}
		}
	} else if preferred != "" {
		info.Reason = "preferredUnavailable"
	}
	info.SelectedAction = actions[selected].name
	return selected, info
}
