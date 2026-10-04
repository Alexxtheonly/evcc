package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// Captured 2026-10-04 17:00-17:04 CEST: the companion rejected the normal
// candidate whenever float32 gt-ft rounded above its float64 parse.
var capturedBoundaryCases = []struct {
	name   string
	gt, ft float32
	dt     int
}{
	{"17:00:20", 141.4028, 104.34508, 882},
	{"17:00:50", 136.59319, 100.79592, 852},
	{"17:01:50", 126.97395, 93.697624, 792},
	{"17:03:20", 112.54509, 83.05016, 702},
	{"17:03:49", 107.73547, 79.50101, 672},
	{"17:04:21", 102.92586, 75.95186, 642},
}

func boundaryRequest(gt, ft float32, dt int, chargeFromGrid bool) (energyRequest, requestDetails, optimizer.OptimizationResult) {
	eta := .9
	wear := .00003
	ev := func(sInitial, sMax float32) energyBattery {
		return energyBattery{BatteryConfig: optimizer.BatteryConfig{CMax: 11040, CMin: 1380, ChargeFromGrid: true, PA: .0001833678, SInitial: sInitial, SMin: sInitial, SMax: sMax}}
	}
	home := energyBattery{
		BatteryConfig: optimizer.BatteryConfig{CMax: 6000, DMax: 6000, ChargeFromGrid: chargeFromGrid, PA: .0001833678, SCapacity: 19320, SInitial: 4173.12, SMax: 18354, SMin: 966},
		EtaC:          &eta, EtaD: &eta, WearCost: &wear,
	}
	req := energyRequest{
		OptimizationInput: optimizer.OptimizationInput{EtaC: .9, EtaD: .9, TimeSeries: optimizer.TimeSeries{
			Dt: []int{dt, 900}, Gt: []float32{gt, 120}, Ft: []float32{ft, 0}, PN: []float32{.0003, .0003}, PE: []float32{0, 0},
		}},
		Batteries: []energyBattery{ev(34500, 75000), ev(22000, 55000), home},
	}
	details := requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeVehicle}, {Type: batteryTypeVehicle}, {Type: batteryTypeBattery}}}
	idle := optimizer.BatteryResult{ChargingPower: []float32{0, 0}, DischargingPower: []float32{0, 0}}
	base := optimizer.OptimizationResult{Batteries: []optimizer.BatteryResult{idle, idle, idle}}
	return req, details, base
}

// parsedFirstSlot decodes the request exactly as the companion receives it.
func parsedFirstSlot(t *testing.T, req energyRequest) (gt, ft float64) {
	t.Helper()
	body, err := json.Marshal(req)
	require.NoError(t, err)
	var wire struct {
		TimeSeries struct {
			Gt []float64 `json:"gt"`
			Ft []float64 `json:"ft"`
		} `json:"time_series"`
	}
	require.NoError(t, json.Unmarshal(body, &wire))
	return wire.TimeSeries.Gt[0], wire.TimeSeries.Ft[0]
}

func firstAction(t *testing.T, actions []energyFirstAction, name string) energyBattery {
	t.Helper()
	i := slices.IndexFunc(actions, func(a energyFirstAction) bool { return a.name == name })
	require.GreaterOrEqual(t, i, 0, name)
	return actions[i].request.Batteries[2]
}

func TestFixedFirstStepStaysInsideZeroGridBoundary(t *testing.T) {
	var above, below int
	for _, c := range capturedBoundaryCases {
		margin := fixedStepMargin(float64(c.dt) / 3600)
		require.Greater(t, margin, 1e-5, "margin must dwarf the 3e-6 Wh float32 error")

		// discharge edge: demand exceeds PV
		req, details, base := boundaryRequest(c.gt, c.ft, c.dt, true)
		gt, ft := parsedFirstSlot(t, req)
		demand := gt - ft
		if float64(c.gt-c.ft) > demand {
			above++
		} else {
			below++
		}
		actions, err := executableFirstActions(req, details, base)
		require.NoError(t, err, c.name)
		discharge := *firstAction(t, actions, "normal").FirstStepDischarge
		assert.GreaterOrEqual(t, discharge, 0.0, c.name)
		assert.LessOrEqual(t, discharge, demand-margin, c.name)
		assert.InDelta(t, demand-margin, discharge, 1e-9, "%s: exactly the parsed demand less the margin", c.name)

		// charge edge: the same values as PV surplus without grid charging
		req, details, base = boundaryRequest(c.ft, c.gt, c.dt, false)
		gt, ft = parsedFirstSlot(t, req)
		surplus := ft - gt
		actions, err = executableFirstActions(req, details, base)
		require.NoError(t, err, c.name)
		charge := *firstAction(t, actions, "normal").FirstStepCharge
		assert.GreaterOrEqual(t, charge, 0.0, c.name)
		assert.LessOrEqual(t, charge, surplus-margin, c.name)
		assert.InDelta(t, surplus-margin, charge, 1e-9, c.name)
	}
	// The captured inputs reproduce both rounding directions.
	assert.Equal(t, 3, above)
	assert.Equal(t, 3, below)

	// Below the margin the battery stays idle rather than reversing direction.
	req, details, base := boundaryRequest(100.0001, 100, 900, true)
	actions, err := executableFirstActions(req, details, base)
	require.NoError(t, err)
	normal := firstAction(t, actions, "normal")
	assert.Zero(t, *normal.FirstStepDischarge)
	assert.Zero(t, *normal.FirstStepCharge)
}

// boundarySolver mirrors the companion's zero-grid rule in float64: without
// battery export or grid charging, a fixed first step may not exceed demand or
// surplus by any amount.
func boundarySolver(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			TimeSeries struct {
				Dt []int     `json:"dt"`
				Gt []float64 `json:"gt"`
				Ft []float64 `json:"ft"`
			} `json:"time_series"`
			Batteries []struct {
				ChargeFromGrid     bool     `json:"charge_from_grid"`
				DischargeToGrid    bool     `json:"discharge_to_grid"`
				SInitial           float64  `json:"s_initial"`
				FirstStepCharge    *float64 `json:"first_step_charge"`
				FirstStepDischarge *float64 `json:"first_step_discharge"`
			} `json:"batteries"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ts := req.TimeSeries
		n := len(ts.Dt)
		net := ts.Gt[0] - ts.Ft[0]
		res := optimizer.OptimizationResult{Status: optimizer.Optimal, GridImport: make([]float32, n), GridExport: make([]float32, n)}
		for _, b := range req.Batteries {
			c, d := *b.FirstStepCharge, *b.FirstStepDischarge
			net += c - d
			res.Batteries = append(res.Batteries, optimizer.BatteryResult{
				ChargingPower:    append([]float32{float32(c)}, make([]float32, n-1)...),
				DischargingPower: append([]float32{float32(d)}, make([]float32, n-1)...),
				StateOfCharge:    slices.Repeat([]float32{float32(b.SInitial)}, n),
			})
		}
		for _, b := range req.Batteries {
			if (*b.FirstStepDischarge > 0 && !b.DischargeToGrid && net < 0) || (*b.FirstStepCharge > 0 && !b.ChargeFromGrid && net > 0) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"No feasible schedule: Infeasible"}`))
				return
			}
		}
		res.GridImport[0], res.GridExport[0] = float32(max(0, net)), float32(max(0, -net))
		for i := 1; i < n; i++ {
			res.GridImport[i] = float32(ts.Gt[i] - ts.Ft[i])
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(res); err != nil {
			t.Error(err)
		}
	}))
}

func TestRobustNormalSurvivesCapturedBoundary(t *testing.T) {
	server := boundarySolver(t)
	defer server.Close()
	client, err := optimizer.NewClientWithResponses(server.URL)
	require.NoError(t, err)
	site := &Site{log: util.NewLogger("boundary"), optimizerClient: client}
	for _, c := range capturedBoundaryCases {
		for _, surplus := range []bool{false, true} {
			gt, ft := c.gt, c.ft
			if surplus {
				gt, ft = ft, gt
			}
			req, details, base := boundaryRequest(gt, ft, c.dt, !surplus)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, info, err := site.robustEnergy(ctx, req, details, base)
			cancel()
			require.NoError(t, err, c.name)
			for _, excluded := range info.Excluded {
				assert.NotEqual(t, "normal", excluded.Action, "%s surplus=%v: %+v", c.name, surplus, excluded)
			}
			assert.True(t, slices.ContainsFunc(info.Evaluations, func(e energyScenarioEvaluation) bool { return e.Action == "normal" }), "%s surplus=%v", c.name, surplus)
		}
	}
}

func boundaryControlSite(t *testing.T, now *time.Time) (*Site, *api.MockBatteryController) {
	t.Helper()
	enableAutomatic(t)
	batCon := api.NewMockBatteryController(gomock.NewController(t))
	var bat api.Meter = &struct {
		api.Meter
		api.BatteryController
	}{BatteryController: batCon}
	site := &Site{
		log: util.NewLogger("boundary"), valueChan: make(chan util.Param, 1024), optimizerNow: func() time.Time { return *now },
		batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{Name: "bat"}, bat)},
	}
	return site, batCon
}

func boundaryDecision(site *Site, mode api.BatteryMode, price float64, firstEnd time.Time) {
	site.setOptimizerBatteryMode(optimizerDecision{
		mode: mode, suggestedMode: mode, price: price,
		economics: &energyControlEconomics{SelectedAction: mode.String(), validUntil: energyDecisionExpiry(firstEnd)},
	})
}

func TestEnergyDecisionSurvivesSlotBoundaryUntilReplaced(t *testing.T) {
	firstEnd := time.Date(2026, 10, 3, 21, 0, 0, 0, time.UTC)
	now := firstEnd.Add(-33 * time.Second)
	site, batCon := boundaryControlSite(t, &now)
	rate := api.Rate{Value: .2}

	batCon.EXPECT().SetBatteryMode(api.BatteryHold)
	boundaryDecision(site, api.BatteryHold, 0, firstEnd)
	site.updateBatteryMode(false, rate)

	// 21:00:08 tonight released hold to normal; the mock rejects any write.
	now = firstEnd.Add(8 * time.Second)
	site.updateBatteryMode(false, rate)
	assert.Equal(t, api.BatteryHold, site.GetBatteryMode())
	assert.Equal(t, "hold", site.energyPreferredAction(), "the new slot's first run keeps its hysteresis incumbent")

	// A run that straddled the boundary supersedes nothing.
	site.finishOptimizerAttempt(validateEnergyPlanInputs(firstEnd, now, EnergyIntelligenceSettings{}, EnergyIntelligenceSettings{}))
	site.updateBatteryMode(false, rate)
	assert.Equal(t, api.BatteryHold, site.GetBatteryMode())

	// The new slot's first result replaces the carried decision.
	now = firstEnd.Add(27 * time.Second)
	boundaryDecision(site, api.BatteryHold, 0, firstEnd.Add(15*time.Minute))
	now = energyDecisionExpiry(firstEnd).Add(time.Second)
	site.updateBatteryMode(false, rate)
	assert.Equal(t, api.BatteryHold, site.GetBatteryMode())
}

func TestEnergyDecisionReleasesWhenNoResultArrivesWithinGrace(t *testing.T) {
	firstEnd := time.Date(2026, 10, 3, 21, 0, 0, 0, time.UTC)
	now := firstEnd.Add(-33 * time.Second)
	site, batCon := boundaryControlSite(t, &now)
	rate := api.Rate{Value: .2}

	batCon.EXPECT().SetBatteryMode(api.BatteryHold)
	boundaryDecision(site, api.BatteryHold, 0, firstEnd)
	site.updateBatteryMode(false, rate)

	now = energyDecisionExpiry(firstEnd).Add(-time.Nanosecond)
	site.updateBatteryMode(false, rate)
	assert.Equal(t, api.BatteryHold, site.GetBatteryMode())

	now = energyDecisionExpiry(firstEnd)
	batCon.EXPECT().SetBatteryMode(api.BatteryNormal)
	site.updateBatteryMode(false, rate)
	assert.Equal(t, api.BatteryNormal, site.GetBatteryMode())
	assert.Empty(t, site.energyPreferredAction())

	// Any other superseded plan still releases immediately.
	now = firstEnd.Add(10 * time.Minute)
	batCon.EXPECT().SetBatteryMode(api.BatteryHold)
	boundaryDecision(site, api.BatteryHold, 0, firstEnd.Add(15*time.Minute))
	site.updateBatteryMode(false, rate)
	site.finishOptimizerAttempt(validateEnergyPlanInputs(firstEnd.Add(15*time.Minute), now, EnergyIntelligenceSettings{}, EnergyIntelligenceSettings{Robust: true}))
	batCon.EXPECT().SetBatteryMode(api.BatteryNormal)
	site.updateBatteryMode(false, rate)
	assert.Equal(t, api.BatteryNormal, site.GetBatteryMode())
}

func TestEnergyChargeDecisionVetoedAtBoundaryReleases(t *testing.T) {
	firstEnd := time.Date(2026, 10, 3, 14, 45, 0, 0, time.UTC)
	now := firstEnd.Add(-20 * time.Second)
	site, batCon := boundaryControlSite(t, &now)

	batCon.EXPECT().SetBatteryMode(api.BatteryCharge)
	boundaryDecision(site, api.BatteryCharge, .2, firstEnd)
	site.updateBatteryMode(false, api.Rate{Value: .2})

	// Same price in the new slot: the carried charge decision stands.
	now = firstEnd.Add(5 * time.Second)
	site.updateBatteryMode(false, api.Rate{Value: .2})
	assert.Equal(t, api.BatteryCharge, site.GetBatteryMode())

	// The new slot's price vetoes it inside the grace period.
	batCon.EXPECT().SetBatteryMode(api.BatteryNormal)
	site.updateBatteryMode(false, api.Rate{Value: .5})
	assert.Equal(t, api.BatteryNormal, site.GetBatteryMode())
}
