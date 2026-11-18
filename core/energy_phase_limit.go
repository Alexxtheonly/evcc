package core

import (
	"encoding/json"
	"math"
)

// ADR-049: per-phase grid import limit. The solver gets one conservative
// worst-phase row per slot, the execution gate vetoes a battery grid charge
// that would push the most loaded phase over the limit. No phase identity is
// modelled: every term is the worst case on a single phase.

// homePhaseShareMin is the worst-phase share of household load when nothing
// better is known: a perfectly balanced house.
const homePhaseShareMin = 1.0 / 3

// homePhaseShareMinA (A summed over phases) is the household current below
// which the measured phase split is noise and the balanced default applies.
const homePhaseShareMinA = 3.0

// phaseVoltage is the nominal phase voltage the limit converts with.
func phaseVoltage() float64 {
	if Voltage > 0 {
		return Voltage
	}
	return 230
}

// phaseSnapshot is the live per-phase state, taken once per site cycle from
// the loop goroutine that owns the loadpoint measurements.
type phaseSnapshot struct {
	// baseA is the worst grid phase import (A) with the home battery's own
	// flow removed, i.e. what the phase carries if the battery stands still.
	baseA float64
	// homeShare is the worst-phase share of household load in [1/3, 1].
	homeShare float64
	valid     bool
}

// homePhaseShare derives the worst-phase share of household load from signed
// grid phase currents (import positive), the loadpoint phase currents and the
// site's PV and battery power (battery positive = discharge). Inverter output
// is taken as symmetric across three phases. Falls back to 1/3 when the meter
// has no phase currents or the household draws too little to tell.
func homePhaseShare(grid []float64, loadpoints [][]float64, pvW, batteryW float64) float64 {
	if len(grid) != 3 {
		return homePhaseShareMin
	}
	inverterA := (max(0, pvW) + batteryW) / (3 * phaseVoltage())
	home := make([]float64, 3)
	var sum float64
	for p := range home {
		home[p] = grid[p] + inverterA
		for _, lp := range loadpoints {
			if len(lp) == 3 {
				home[p] -= lp[p]
			}
		}
		home[p] = max(0, home[p])
		sum += home[p]
	}
	if sum < homePhaseShareMinA {
		return homePhaseShareMin
	}
	return min(1, max(homePhaseShareMin, max(home[0], home[1], home[2])/sum))
}

// phaseBaseA is the worst grid phase import (A) with the battery's flow
// removed: charging (negative power) is subtracted, discharging added back,
// so a running grid charge does not count itself and the gate cannot flap.
func phaseBaseA(grid []float64, batteryW float64) (float64, bool) {
	if len(grid) != 3 {
		return 0, false
	}
	return max(grid[0], grid[1], grid[2]) + batteryW/(3*phaseVoltage()), true
}

// updatePhaseSnapshot runs in the site loop after the meters were read and
// before loadpoints update, the goroutine that writes lp.chargeCurrents.
func (site *Site) updatePhaseSnapshot(state siteState) {
	var lps [][]float64
	for _, lp := range site.loadpoints {
		if lp != nil && lp.chargeCurrents != nil {
			lps = append(lps, lp.chargeCurrents)
		}
	}
	base, ok := phaseBaseA(state.gridCurrents, state.battery.Power)
	snap := phaseSnapshot{
		baseA:     base,
		homeShare: homePhaseShare(state.gridCurrents, lps, state.pvPower, state.battery.Power),
		valid:     ok,
	}
	site.Lock()
	site.phaseSnapshot = snap
	site.Unlock()
}

// phaseLimitGate decides whether a battery grid charge fits the phase limit.
// The zero value is disabled.
type phaseLimitGate struct {
	limitA float64 // per-phase limit, 0 = disabled
	baseA  float64 // live worst-phase import without the battery
	valid  bool    // baseA measured
}

// exceeds reports whether charging at chargeW, split evenly over three
// phases, would push the worst phase over the limit. Without phase currents
// the grid power is unknown per phase and the gate fails closed.
func (g phaseLimitGate) exceeds(chargeW float64) bool {
	if g.limitA <= 0 {
		return false
	}
	if !g.valid {
		return true
	}
	return g.baseA+chargeW/(3*phaseVoltage()) > g.limitA
}

// phaseLimitGateLocked is the gate from the latest snapshot. Caller holds the
// site lock (read or write).
func (site *Site) phaseLimitGateLocked() phaseLimitGate {
	return phaseLimitGate{limitA: site.gridPhaseLimitA, baseA: site.phaseSnapshot.baseA, valid: site.phaseSnapshot.valid}
}

func (site *Site) phaseLimitGate() phaseLimitGate {
	site.RLock()
	defer site.RUnlock()
	return site.phaseLimitGateLocked()
}

// energyPhaseLimit is the request part of the per-phase limit. base_phase_max
// is derived from Gt at encoding time, so scenario requests that change Gt
// stay consistent.
type energyPhaseLimit struct {
	LimitA    float64 // grid.i_max_phase, A per phase
	HomeShare float64 // worst-phase share of Gt
}

// basePhaseMax is the worst-phase household energy per slot (Wh): Gt times
// the measured worst-phase share. Gt already contains unmodelled loadpoint
// load, which therefore counts at the household share.
func (p energyPhaseLimit) basePhaseMax(gt []float32) []float64 {
	res := make([]float64, len(gt))
	for t, v := range gt {
		res[t] = float64(v) * p.HomeShare
	}
	return res
}

// MarshalJSON adds grid.i_max_phase and time_series.base_phase_max when a
// phase limit is set. Without one the encoding is the plain struct, byte for
// byte what it was before the limit existed.
func (req energyRequest) MarshalJSON() ([]byte, error) {
	type plain energyRequest
	body, err := json.Marshal(plain(req))
	if err != nil || req.PhaseLimit == nil {
		return body, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	extend := func(key string, field string, value any) error {
		obj := map[string]json.RawMessage{}
		if raw, ok := doc[key]; ok && string(raw) != "null" {
			if err := json.Unmarshal(raw, &obj); err != nil {
				return err
			}
		}
		v, err := json.Marshal(value)
		if err != nil {
			return err
		}
		obj[field] = v
		raw, err := json.Marshal(obj)
		doc[key] = raw
		return err
	}
	if err := extend("grid", "i_max_phase", req.PhaseLimit.LimitA); err != nil {
		return nil, err
	}
	if err := extend("time_series", "base_phase_max", req.PhaseLimit.basePhaseMax(req.TimeSeries.Gt)); err != nil {
		return nil, err
	}
	return json.Marshal(doc)
}

// energyPhaseDivisor spreads a home battery's AC power over three phases.
const energyPhaseDivisor = 3

// applyPhaseLimit attaches the per-phase limit to req. Home batteries count
// at their power divided by three phases; loadpoints and expected vehicles at
// their charger's maximum current on the worst phase whenever they charge,
// whatever their phase count (a 3p car at 16 A loads every phase with 16 A,
// a 1p car loads one phase with 16 A). maxCurrent returns the loadpoint's
// maximum current, or the largest of all loadpoints for id < 0.
func applyPhaseLimit(req *energyRequest, details []batteryDetail, limitA, homeShare float64, maxCurrent func(id int) float64) {
	if limitA <= 0 {
		return
	}
	req.PhaseLimit = &energyPhaseLimit{LimitA: limitA, HomeShare: min(1, max(homePhaseShareMin, homeShare))}
	for i := range req.Batteries {
		bat := &req.Batteries[i]
		if i < len(details) && details[i].Type == batteryTypeBattery {
			divisor := float64(energyPhaseDivisor)
			bat.PhaseDivisor = &divisor
			continue
		}
		id := -1
		if i < len(details) && details[i].loadpoint != nil {
			id = *details[i].loadpoint
		}
		current := maxCurrent(id)
		if current <= 0 {
			// unknown charger: its full power on one phase
			current = math.Max(0, float64(bat.CMax)) / phaseVoltage()
		}
		bat.PhaseCurrent = &current
	}
}

// loadpointMaxCurrent returns loadpoint id's maximum current, or the largest
// of all loadpoints for an unknown id.
func (site *Site) loadpointMaxCurrent(id int) float64 {
	lps := site.Loadpoints()
	if id >= 0 && id < len(lps) && lps[id] != nil {
		return lps[id].GetMaxCurrent()
	}
	var res float64
	for _, lp := range lps {
		if lp != nil {
			res = max(res, lp.GetMaxCurrent())
		}
	}
	return res
}
