package core

import (
	"encoding/json"
	"math"
	"time"
)

// ADR-049: per-phase grid import limit. The solver gets one conservative
// worst-phase row per slot, the execution gate vetoes a battery grid charge
// that would push the most loaded phase over the limit. No phase identity is
// modelled: every term is the worst case on a single phase.

// homePhaseShareMin is the worst-phase share of household load in a
// perfectly balanced house, the floor for a measured share.
const homePhaseShareMin = 1.0 / 3

// homePhaseShareUnknown is the share before any phase currents were seen:
// all household load on one phase.
const homePhaseShareUnknown = 1.0

// homePhaseShareMinA (A summed over phases) is the household current below
// which the measured phase split is noise and the balanced floor applies.
const homePhaseShareMinA = 3.0

// homePhaseShareHold keeps the worst observed share this long, so a briefly
// balanced moment does not lower the row.
const homePhaseShareHold = time.Hour

const (
	phaseSnapshotMaxAge = 60 * time.Second // older snapshot: gate fails closed
	phaseGateClearA     = 2.0              // a latched veto clears this far below the limit
	phaseGateHoldOff    = 60 * time.Second // minimum veto duration
	phaseChargeMargin   = 1.05             // DC/AC and rating margin on battery charge power
)

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
	// homeShare is the worst-phase share of household load in [1/3, 1],
	// the peak over homePhaseShareHold; 1 until phase currents were seen.
	homeShare   float64
	homeShareAt time.Time
	// liveChargeW is the measured battery charge power (W, >= 0)
	liveChargeW float64
	at          time.Time // measurement time; zero = never
	valid       bool      // grid phase currents read this cycle
	latched     bool      // live gate vetoes until cleared with hysteresis
	vetoAt      time.Time // last time the live gate tripped
}

// homePhaseShare derives the worst-phase share of household load from signed
// grid phase currents (import positive), the loadpoint phase currents and the
// site's PV and battery power (battery positive = discharge). Inverter output
// is taken as symmetric across three phases. Without phase currents the share
// is unknown (1); with too little household current to tell it is 1/3.
func homePhaseShare(grid []float64, loadpoints [][]float64, pvW, batteryW float64) float64 {
	if len(grid) != 3 {
		return homePhaseShareUnknown
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
// before loadpoints update, the goroutine that writes lp.chargeCurrents. It
// also advances the live gate's latch.
func (site *Site) updatePhaseSnapshot(state siteState) {
	var lps [][]float64
	for _, lp := range site.loadpoints {
		if lp != nil && lp.chargeCurrents != nil {
			lps = append(lps, lp.chargeCurrents)
		}
	}
	now := time.Now()
	base, ok := phaseBaseA(state.gridCurrents, state.battery.Power)
	share := homePhaseShare(state.gridCurrents, lps, state.pvPower, state.battery.Power)

	site.Lock()
	defer site.Unlock()
	prev := site.phaseSnapshot
	snap := phaseSnapshot{baseA: base, liveChargeW: max(0, -state.battery.Power), at: now, valid: ok,
		homeShare: share, homeShareAt: now, latched: prev.latched, vetoAt: prev.vetoAt}
	switch {
	case !ok && !prev.homeShareAt.IsZero() && now.Sub(prev.homeShareAt) < homePhaseShareHold:
		snap.homeShare, snap.homeShareAt = prev.homeShare, prev.homeShareAt // keep the last measured peak
	case !ok:
		snap.homeShare, snap.homeShareAt = homePhaseShareUnknown, time.Time{}
	case prev.homeShareAt.IsZero() == false && prev.homeShare > share && now.Sub(prev.homeShareAt) < homePhaseShareHold:
		snap.homeShare, snap.homeShareAt = prev.homeShare, prev.homeShareAt
	}
	site.phaseSnapshot = snap
	g := site.phaseLimitGateLocked()
	g.latched = false
	switch charge := site.phaseChargeRatingLocked(); {
	case g.limitA <= 0:
		site.phaseSnapshot.latched = false
	case g.exceeds(charge):
		site.phaseSnapshot.latched, site.phaseSnapshot.vetoAt = true, now
	case prev.latched && (now.Sub(prev.vetoAt) < phaseGateHoldOff || g.withChargeA(charge) > g.limitA-phaseGateClearA):
		// stay latched: hold-off running or not yet clearly below the limit
	default:
		site.phaseSnapshot.latched = false
	}
}

// phaseChargeRatingLocked is the battery grid charge power the gate assumes:
// the larger of the active decision's, the request's rating and the measured.
func (site *Site) phaseChargeRatingLocked() float64 {
	return max(site.optimizerChargePowerW, site.batteryChargeRatingW, site.phaseSnapshot.liveChargeW)
}

// phaseLimitGate decides whether a battery grid charge fits the phase limit.
// The zero value is disabled.
type phaseLimitGate struct {
	limitA      float64   // per-phase limit, 0 = disabled
	baseA       float64   // live worst-phase import without the battery
	liveChargeW float64   // measured battery charge power, a floor for the rating
	valid       bool      // baseA measured
	at, now     time.Time // measurement and evaluation time
	latched     bool      // live veto not yet cleared
}

// withChargeA is the worst phase with the battery charging at chargeW (or
// the measured power if higher), plus the DC/AC margin, over three phases.
func (g phaseLimitGate) withChargeA(chargeW float64) float64 {
	return g.baseA + max(chargeW, g.liveChargeW)*phaseChargeMargin/(3*phaseVoltage())
}

// exceeds reports whether charging at chargeW would push the worst phase over
// the limit. Without fresh phase currents (meter error, no capability or a
// snapshot older than phaseSnapshotMaxAge) the gate fails closed.
func (g phaseLimitGate) exceeds(chargeW float64) bool {
	if g.limitA <= 0 {
		return false
	}
	if !g.valid || g.now.Sub(g.at) > phaseSnapshotMaxAge || g.latched {
		return true
	}
	return g.withChargeA(chargeW) > g.limitA
}

// phaseLimitGateLocked is the gate from the latest snapshot. Caller holds the
// site lock (read or write).
func (site *Site) phaseLimitGateLocked() phaseLimitGate {
	s := site.phaseSnapshot
	return phaseLimitGate{limitA: site.gridPhaseLimitA, baseA: s.baseA, liveChargeW: s.liveChargeW,
		valid: s.valid, at: s.at, now: time.Now(), latched: s.latched}
}

func (site *Site) phaseLimitGate() phaseLimitGate {
	site.RLock()
	defer site.RUnlock()
	return site.phaseLimitGateLocked()
}

// phaseLimitVetoesCharge is the live gate for every battery charge path
// (optimizer, external mode, classic grid charge).
func (site *Site) phaseLimitVetoesCharge() bool {
	site.RLock()
	defer site.RUnlock()
	return site.phaseLimitGateLocked().exceeds(site.phaseChargeRatingLocked())
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
// at their power divided by three phases, scaled up when the measured charge
// power liveChargeW exceeds their summed rating; loadpoints and expected vehicles at
// their charger's maximum current on the worst phase whenever they charge,
// whatever their phase count (a 3p car at 16 A loads every phase with 16 A,
// a 1p car loads one phase with 16 A). maxCurrent returns the loadpoint's
// maximum current, or the largest of all loadpoints for id < 0.
func applyPhaseLimit(req *energyRequest, details []batteryDetail, limitA, homeShare, liveChargeW float64, maxCurrent func(id int) float64) {
	if limitA <= 0 {
		return
	}
	if homeShare <= 0 {
		homeShare = homePhaseShareUnknown // never measured
	}
	req.PhaseLimit = &energyPhaseLimit{LimitA: limitA, HomeShare: min(1, max(homePhaseShareMin, homeShare))}
	var rating float64
	for i := range req.Batteries {
		if i < len(details) && details[i].Type == batteryTypeBattery {
			rating += float64(req.Batteries[i].CMax)
		}
	}
	scale := 1.0
	if rating > 0 {
		scale = max(1, liveChargeW/rating)
	}
	for i := range req.Batteries {
		bat := &req.Batteries[i]
		if i < len(details) && details[i].Type == batteryTypeBattery {
			divisor := max(1, energyPhaseDivisor/scale)
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
