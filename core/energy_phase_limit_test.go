package core

import (
	"encoding/json"
	"testing"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/types"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// phaseLimitRequest is the 2026-11-18 03:45 site: BYD home battery (GEN24,
// 6 kW grid charge), Luna on a 3p 16 A wallbox (loadpoint 0), Calypso on a
// 1p 16 A wallbox (loadpoint 1).
func phaseLimitRequest() (energyRequest, []batteryDetail) {
	luna, calypso := 0, 1
	wear := 0.00003
	req := energyRequest{OptimizationInput: optimizer.OptimizationInput{
		EtaC: 0.95, EtaD: 0.95,
		Grid: optimizer.GridConfig{PMaxImp: 30000},
		TimeSeries: optimizer.TimeSeries{
			Dt: []int{900, 900, 900}, Gt: []float32{575, 575, 600}, Ft: []float32{0, 0, 0},
			PN: []float32{0.0002, 0.0002, 0.0003}, PE: []float32{0.00008, 0.00008, 0.00008},
		},
	}}
	req.Batteries = []energyBattery{
		{BatteryConfig: optimizer.BatteryConfig{CMax: 6000, DMax: 6000, SCapacity: 10000, SMax: 10000, SInitial: 3000, ChargeFromGrid: true}, WearCost: &wear},
		{BatteryConfig: optimizer.BatteryConfig{CMin: 4140, CMax: 11040, SCapacity: 75000, SMax: 60000, SInitial: 30000, ChargeFromGrid: true}},
		{BatteryConfig: optimizer.BatteryConfig{CMin: 1380, CMax: 3680, SCapacity: 50000, SMax: 40000, SInitial: 20000, ChargeFromGrid: true}},
	}
	details := []batteryDetail{
		{Type: batteryTypeBattery, Name: "byd", controllable: true},
		{Type: batteryTypeVehicle, loadpoint: &luna},
		{Type: batteryTypeVehicle, loadpoint: &calypso},
	}
	return req, details
}

func maxCurrent16(int) float64 { return 16 }

// legacyEnergyRequest has the encoding energyRequest had before ADR-049.
type legacyEnergyRequest struct {
	optimizer.OptimizationInput
	Batteries []energyBattery `json:"batteries"`
}

func TestEnergyPhaseLimitDisabledIsByteIdentical(t *testing.T) {
	req, details := phaseLimitRequest()
	before, err := json.Marshal(legacyEnergyRequest{req.OptimizationInput, req.Batteries})
	require.NoError(t, err)

	applyPhaseLimit(&req, details, 0, 0.8, maxCurrent16)
	assert.Nil(t, req.PhaseLimit)
	after, err := json.Marshal(req)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
	assert.NotContains(t, string(after), "phase")
	assert.NotContains(t, energyRequestFeatures(req), "phase_limit")

	// golden: pinned so an unrelated encoding change is noticed here too
	golden := `{"batteries":[{"c_max":6000,"c_min":0,"charge_from_grid":true,"d_max":6000,"p_a":0,"s_capacity":10000,"s_initial":3000,"s_max":10000,"s_min":0,"wear_cost":0.00003},` +
		`{"c_max":11040,"c_min":4140,"charge_from_grid":true,"d_max":0,"p_a":0,"s_capacity":75000,"s_initial":30000,"s_max":60000,"s_min":0},` +
		`{"c_max":3680,"c_min":1380,"charge_from_grid":true,"d_max":0,"p_a":0,"s_capacity":50000,"s_initial":20000,"s_max":40000,"s_min":0}],` +
		`"eta_c":0.95,"eta_d":0.95,"grid":{"p_max_imp":30000},"strategy":{},` +
		`"time_series":{"dt":[900,900,900],"ft":[0,0,0],"gt":[575,575,600],"p_E":[0.00008,0.00008,0.00008],"p_N":[0.0002,0.0002,0.0003]}}`
	var got, want any
	require.NoError(t, json.Unmarshal(after, &got))
	require.NoError(t, json.Unmarshal([]byte(golden), &want))
	assert.Equal(t, want, got)
}

func TestEnergyPhaseLimitRequestFields(t *testing.T) {
	req, details := phaseLimitRequest()
	// an expected vehicle without a loadpoint id gets the largest charger
	req.Batteries = append(req.Batteries, energyBattery{BatteryConfig: optimizer.BatteryConfig{CMax: 11040}})
	details = append(details, batteryDetail{Type: batteryTypeVehicle})
	maxCurrent := func(id int) float64 {
		return map[int]float64{0: 16, 1: 16, -1: 20}[id]
	}

	applyPhaseLimit(&req, details, 45, 0.8, maxCurrent)
	assert.Contains(t, energyRequestFeatures(req), "phase_limit")

	body, err := json.Marshal(req)
	require.NoError(t, err)
	var doc struct {
		Grid       map[string]float64 `json:"grid"`
		TimeSeries struct {
			Gt           []float64 `json:"gt"`
			BasePhaseMax []float64 `json:"base_phase_max"`
		} `json:"time_series"`
		Batteries []map[string]any `json:"batteries"`
	}
	require.NoError(t, json.Unmarshal(body, &doc))

	assert.Equal(t, 45.0, doc.Grid["i_max_phase"])
	assert.Equal(t, 30000.0, doc.Grid["p_max_imp"], "existing grid fields kept")
	assert.Equal(t, []float64{575, 575, 600}, doc.TimeSeries.Gt)
	assert.InDeltaSlice(t, []float64{460, 460, 480}, doc.TimeSeries.BasePhaseMax, 1e-6)

	assert.Equal(t, 3.0, doc.Batteries[0]["phase_divisor"])
	assert.NotContains(t, doc.Batteries[0], "phase_current")
	for i, want := range []float64{16, 16, 20} {
		assert.Equal(t, want, doc.Batteries[i+1]["phase_current"], "vehicle %d", i)
		assert.NotContains(t, doc.Batteries[i+1], "phase_divisor")
	}

	// a scenario changes Gt: base_phase_max follows
	scenario := req
	scenario.TimeSeries.Gt = []float32{575, 1000, 600}
	body, err = json.Marshal(scenario)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &doc))
	assert.InDeltaSlice(t, []float64{460, 800, 480}, doc.TimeSeries.BasePhaseMax, 1e-6)

	// the share is clamped to [1/3, 1]
	req2, details2 := phaseLimitRequest()
	applyPhaseLimit(&req2, details2, 45, 0, maxCurrent16)
	assert.InDelta(t, 1.0/3, req2.PhaseLimit.HomeShare, 1e-9)
}

func TestEnergyPhaseLimitReplayCoefficients(t *testing.T) {
	// 2026-11-18 03:45-04:05: limit 45 A. The worst-phase row of a slot in which
	// all three devices charge must exceed the limit, so the solver has to drop
	// one of them; without the battery it fits.
	req, details := phaseLimitRequest()
	share := homePhaseShare([]float64{10, 1, 1}, nil, 0, 0) // base ~10 A on L1
	applyPhaseLimit(&req, details, 45, share, maxCurrent16)

	u, hours := phaseVoltage(), 0.25
	limitWh := req.PhaseLimit.LimitA * u * hours
	row := func(t int, charging ...int) float64 {
		wh := req.PhaseLimit.basePhaseMax(req.TimeSeries.Gt)[t]
		for _, i := range charging {
			b := req.Batteries[i]
			if b.PhaseCurrent != nil {
				wh += *b.PhaseCurrent * u * hours
			} else {
				wh += float64(b.CMax) * hours / *b.PhaseDivisor
			}
		}
		return wh
	}
	all := row(0, 0, 1, 2)
	assert.Greater(t, all, limitWh, "Luna + Calypso + battery + base exceeds 45 A")
	assert.InDelta(t, 16+16+6000/(3*u)+575*share/(u*hours), all/(u*hours), 1e-6)
	assert.LessOrEqual(t, row(0, 1, 2), limitWh, "both cars + base fit 45 A")
}

func TestHomePhaseShare(t *testing.T) {
	u := phaseVoltage()
	for _, tc := range []struct {
		name      string
		grid      []float64
		lps       [][]float64
		pv, bat   float64
		wantShare float64
	}{
		{"no phase currents", nil, nil, 0, 0, 1.0 / 3},
		{"balanced", []float64{4, 4, 4}, nil, 0, 0, 1.0 / 3},
		{"base on L1", []float64{10, 1, 1}, nil, 0, 0, 10.0 / 12},
		{"too little to tell", []float64{1, 0.5, 0.5}, nil, 0, 0, 1.0 / 3},
		// the 03:45 meter: base 10 A on L1, Luna 3x16 A, Calypso 16 A on L1,
		// battery charging 6 kW. Loadpoints and battery removed, base remains.
		{"replay minus loadpoints and battery", []float64{10 + 16 + 16 + 6000/(3*u), 1 + 16 + 6000/(3*u), 1 + 16 + 6000/(3*u)},
			[][]float64{{16, 16, 16}, {16, 0, 0}}, 0, -6000, 10.0 / 12},
		// PV covers part of each phase evenly
		{"pv symmetric", []float64{10 - 3000/(3*u), 1 - 3000/(3*u), 1 - 3000/(3*u)}, nil, 3000, 0, 10.0 / 12},
	} {
		assert.InDelta(t, tc.wantShare, homePhaseShare(tc.grid, tc.lps, tc.pv, tc.bat), 1e-6, tc.name)
	}
}

func TestPhaseLimitGate(t *testing.T) {
	u := phaseVoltage()
	base, ok := phaseBaseA([]float64{10 + 16 + 16, 17, 17}, 0)
	require.True(t, ok)
	assert.InDelta(t, 42, base, 1e-9)

	// a running 6 kW grid charge does not count itself
	running, _ := phaseBaseA([]float64{42 + 6000/(3*u), 17, 17}, -6000)
	assert.InDelta(t, 42, running, 1e-9)

	for _, tc := range []struct {
		name   string
		gate   phaseLimitGate
		charge float64
		want   bool
	}{
		{"disabled", phaseLimitGate{}, 6000, false},
		{"disabled without meter", phaseLimitGate{valid: false}, 6000, false},
		{"replay: 42 A + 8.7 A > 45 A", phaseLimitGate{limitA: 45, baseA: 42, valid: true}, 6000, true},
		{"fits", phaseLimitGate{limitA: 45, baseA: 30, valid: true}, 6000, false},
		{"exactly at limit", phaseLimitGate{limitA: 45, baseA: 45 - 6000/(3*u), valid: true}, 6000, false},
		{"no phase currents fails closed", phaseLimitGate{limitA: 45}, 6000, true},
	} {
		assert.Equal(t, tc.want, tc.gate.exceeds(tc.charge), tc.name)
	}
}

func TestBatteryModeCandidatePhaseLimit(t *testing.T) {
	// plan that pays a slot-0 grid charge back at 0.40
	req, res := chargeReqRes([]float32{0.0002, 0.0004}, 5000, []float32{6375, 4990}, []float32{0, 1375})
	req.Batteries[0].CMax = 6000
	details := []batteryDetail{{Type: batteryTypeBattery, Name: "a", controllable: true}}

	for _, tc := range []struct {
		name       string
		action     string
		gate       phaseLimitGate
		wantMode   api.BatteryMode
		wantReason optimizerVetoReason
	}{
		{"charge without limit", "charge", phaseLimitGate{}, api.BatteryCharge, vetoReasonNone},
		{"charge fits", "charge", phaseLimitGate{limitA: 45, baseA: 20, valid: true}, api.BatteryCharge, vetoReasonNone},
		{"charge vetoed to hold", "charge", phaseLimitGate{limitA: 45, baseA: 42, valid: true}, api.BatteryHold, vetoReasonPhaseLimit},
		// holdcharge withholds charging, it never adds import
		{"holdcharge untouched", "holdcharge", phaseLimitGate{limitA: 45, baseA: 44, valid: true}, api.BatteryHoldCharge, vetoReasonNone},
		{"normal untouched", "normal", phaseLimitGate{limitA: 45, baseA: 60, valid: true}, api.BatteryNormal, vetoReasonNone},
	} {
		d := batteryModeCandidate(map[string]types.Suggestion{"battery:a": {Action: tc.action}}, req, res, details, tc.gate)
		assert.Equal(t, tc.wantMode, d.mode, tc.name)
		assert.Equal(t, tc.wantReason, d.vetoReason, tc.name)
		if tc.wantReason == vetoReasonPhaseLimit {
			assert.True(t, d.chargeVetoed, tc.name)
			assert.Equal(t, api.BatteryCharge, d.suggestedMode, tc.name)
		}
		if tc.wantMode == api.BatteryCharge {
			assert.Equal(t, 6000.0, d.chargePowerW, tc.name)
		}
	}
}

func TestBatterySuggestionModePhaseLimit(t *testing.T) {
	site := &Site{}
	site.optimizerBatteryMode = api.BatteryCharge
	site.optimizerBatteryModeUpdated = site.optimizerTime()
	site.optimizerChargePrice = 1
	site.optimizerChargePowerW = 6000
	rate := api.Rate{Value: 0.2}

	mode, ok := site.batterySuggestionMode(rate)
	require.True(t, ok)
	assert.Equal(t, api.BatteryCharge, mode, "limit off")

	site.gridPhaseLimitA = 45
	site.phaseSnapshot = phaseSnapshot{baseA: 20, homeShare: 1.0 / 3, valid: true}
	mode, _ = site.batterySuggestionMode(rate)
	assert.Equal(t, api.BatteryCharge, mode, "fits")

	// a car started after the decision
	site.phaseSnapshot.baseA = 42
	mode, ok = site.batterySuggestionMode(rate)
	assert.True(t, ok)
	assert.Equal(t, api.BatteryHold, mode, "live gate holds")
}

func TestEnergyIntelligenceGridPhaseLimitValidation(t *testing.T) {
	cfg := EnergyIntelligenceSettings{SettlementMode: "simulation"}
	for _, tc := range []struct {
		limit float64
		ok    bool
	}{{0, true}, {45, true}, {6, true}, {5, false}, {-1, false}, {1001, false}} {
		cfg.GridPhaseLimitA = tc.limit
		assert.Equal(t, tc.ok, cfg.validate() == nil, "%v", tc.limit)
	}
	body, err := json.Marshal(EnergyIntelligenceSettings{SettlementMode: "simulation"})
	require.NoError(t, err)
	assert.NotContains(t, string(body), "gridPhaseLimitA", "unset limit leaves stored settings unchanged")
}
