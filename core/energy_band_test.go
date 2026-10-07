package core

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/vehicle"
	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/server/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// liveBandInput is Calypso on 2026-10-07: 55 kWh at 36 %, floor 30 %, band 50 %,
// reference = 30-day median 0.4007 EUR/kWh minus 10 %, horizon minimum 0.3136 EUR/kWh.
func liveBandInput() cheapBandInput {
	price := 0.4007 * 0.9
	return cheapBandInput{
		capacity: 55, soc: 36, minSoc: 30, limitSoc: 100, bandSoc: 50, minRun: 30,
		mode: api.ModePV, ref: cheapBandReference{Price: &price, Source: "median"},
		pa: terminalStorageValue(0.0003136), etaC: eta,
	}
}

func TestCheapBandConvertsReferenceToStoredEnergy(t *testing.T) {
	in := liveBandInput()
	band := cheapBand(in)
	require.NotNil(t, band)
	require.True(t, band.active)

	assert.Equal(t, 27500.0, band.sBand, "50 % of 55 kWh in stored Wh")
	assert.Equal(t, 1800.0, band.minRun)
	assert.Zero(t, band.elapsed)

	// one AC Wh bought at exactly the reference stores eta Wh worth pa + pBand: ratio 1
	breakEven := eta * (float64(in.pa) + band.pBand) * 1e3
	assert.InDelta(t, 1, breakEven / *in.ref.Price, 1e-9)
	// realistic magnitudes: 0.36063 EUR/kWh against a 0.000279 EUR/Wh terminal value
	assert.InDelta(t, 0.000121, band.pBand, 0.000001)
}

func TestCheapBandStates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(*cheapBandInput)
		want   string
		active bool
		top    int
	}{
		{"active", func(*cheapBandInput) {}, bandStatusWaitingCheap, true, 50},
		{"below floor still sends the band", func(in *cheapBandInput) { in.soc = 25 }, bandStatusWaitingCheap, true, 50},
		{"limit soc caps the top", func(in *cheapBandInput) { in.limitSoc = 45 }, bandStatusWaitingCheap, true, 45},
		{"reached", func(in *cheapBandInput) { in.soc = 50 }, bandStatusReached, false, 50},
		{"top not above floor", func(in *cheapBandInput) { in.minSoc = 50 }, bandStatusInactive, false, 50},
		{"limit below floor", func(in *cheapBandInput) { in.limitSoc = 30 }, bandStatusInactive, false, 30},
		{"mode now", func(in *cheapBandInput) { in.mode = api.ModeNow }, bandStatusInactive, false, 50},
		{"mode off", func(in *cheapBandInput) { in.mode = api.ModeOff }, bandStatusInactive, false, 50},
		{"minpv", func(in *cheapBandInput) { in.mode = api.ModeMinPV }, bandStatusWaitingCheap, true, 50},
		{"unknown soc", func(in *cheapBandInput) { in.soc = 0 }, bandStatusInactive, false, 50},
		{"no reference", func(in *cheapBandInput) { in.ref.Price = nil }, bandStatusNoReference, false, 50},
		{"reference below stored value", func(in *cheapBandInput) { p := 0.25; in.ref.Price = &p }, bandStatusNothingCheap, false, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := liveBandInput()
			tc.modify(&in)
			band := cheapBand(in)
			require.NotNil(t, band)
			assert.Equal(t, tc.want, band.Status)
			assert.Equal(t, tc.active, band.active)
			assert.Equal(t, tc.top, band.TopSoc)
		})
	}

	in := liveBandInput()
	in.bandSoc = 0
	assert.Nil(t, cheapBand(in), "no band configured")
}

// liveRequest is a reduced copy of the 2026-10-07 18:19 request: Calypso and the BYD.
func liveRequest() energyRequest {
	return energyRequest{
		OptimizationInput: optimizer.OptimizationInput{EtaC: eta, EtaD: eta, TimeSeries: optimizer.TimeSeries{
			Dt: []int{627, 900, 900, 900}, Gt: []float32{310, 290, 280, 300}, Ft: []float32{33, 37, 0, 0},
			PN: []float32{.0005002, .0005037, .0003136, .0003175}, PE: []float32{.0000786, .0000786, .0000786, .0000786},
		}},
		Batteries: []energyBattery{
			{BatteryConfig: optimizer.BatteryConfig{ChargeFromGrid: true, CMin: 1380, CMax: 11040, PA: 0.00027941758, SInitial: 19800, SMax: 55000, SMin: 16500, CPriority: 1}},
			{BatteryConfig: optimizer.BatteryConfig{ChargeFromGrid: true, CMax: 6000, DMax: 6294.56, PA: 0.00027941758, SCapacity: 19320, SInitial: 2434.32, SMax: 18354, SMin: 966}},
		},
	}
}

// golden is the wire request the base revision 713b69065 marshals for liveRequest,
// generated there before ADR-017; a band that is not active must not change one byte.
const golden = `{"eta_c":0.9,"eta_d":0.9,"grid":{},"strategy":{},"time_series":{"dt":[627,900,900,900],"ft":[33,37,0,0],"gt":[310,290,280,300],"p_E":[0.0000786,0.0000786,0.0000786,0.0000786],"p_N":[0.0005002,0.0005037,0.0003136,0.0003175]},"batteries":[{"c_max":11040,"c_min":1380,"c_priority":1,"charge_from_grid":true,"d_max":0,"p_a":0.00027941758,"s_initial":19800,"s_max":55000,"s_min":16500},{"c_max":6000,"c_min":0,"charge_from_grid":true,"d_max":6294.56,"p_a":0.00027941758,"s_capacity":19320,"s_initial":2434.32,"s_max":18354,"s_min":966}]}`

func TestCheapBandUnsetKeepsRequestByteIdentical(t *testing.T) {
	in := liveBandInput()
	reached, noRef, off := in, in, in
	reached.soc = 55
	noRef.ref.Price = nil
	off.mode = api.ModeNow

	for name, band := range map[string]*energyBand{
		"unset": nil, "reached": cheapBand(reached), "no reference": cheapBand(noRef), "mode now": cheapBand(off),
	} {
		t.Run(name, func(t *testing.T) {
			req := liveRequest()
			applyCheapBand(&req.Batteries[0], band)
			encoded, err := json.Marshal(req)
			require.NoError(t, err)
			assert.Equal(t, golden, string(encoded))
			assert.Empty(t, energyRequestFeatures(req), "no new companion capability required")

			res := optimizer.OptimizationResult{GridImport: []float32{0, 0, 3000, 0}, GridExport: make([]float32, 4), Batteries: []optimizer.BatteryResult{
				{DischargingPower: make([]float32, 4), StateOfCharge: []float32{19800, 19800, 22500, 22500}},
				{DischargingPower: make([]float32, 4), StateOfCharge: make([]float32, 4)},
			}}
			assert.Zero(t, cheapBandValue(req.Batteries[0], 22500))
			before := energyCost(liveRequest(), res)
			assert.Equal(t, before, energyCost(req, res))
		})
	}
}

func TestApplyCheapBand(t *testing.T) {
	req := liveRequest()
	band := cheapBand(liveBandInput())
	applyCheapBand(&req.Batteries[0], band)
	applyCheapBand(&req.Batteries[1], nil)

	require.NotNil(t, req.Batteries[0].SBand)
	assert.Equal(t, 27500.0, *req.Batteries[0].SBand)
	assert.Equal(t, band.pBand, *req.Batteries[0].PBand)
	assert.Equal(t, 1800.0, *req.Batteries[0].CMinRun)
	assert.Nil(t, req.Batteries[0].CRunElapsed, "no run in progress")
	assert.Nil(t, req.Batteries[1].SBand)
	assert.Equal(t, []string{"battery_band", "minimum_run"}, energyRequestFeatures(req))

	encoded, err := json.Marshal(req.Batteries[0])
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"s_band":27500`)
	assert.Contains(t, string(encoded), `"c_min_run":1800`)

	// the band top never exceeds what the companion accepts
	req = liveRequest()
	req.Batteries[0].SMax = 20000
	applyCheapBand(&req.Batteries[0], band)
	assert.Equal(t, 20000.0, *req.Batteries[0].SBand)

	// no minimum run without on/off binaries, and none when switched off
	req = liveRequest()
	req.Batteries[0].CMin = 0
	applyCheapBand(&req.Batteries[0], band)
	assert.Nil(t, req.Batteries[0].CMinRun)
	in := liveBandInput()
	in.minRun = 0
	req = liveRequest()
	applyCheapBand(&req.Batteries[0], cheapBand(in))
	assert.Nil(t, req.Batteries[0].CMinRun)
	assert.Equal(t, []string{"battery_band"}, energyRequestFeatures(req))
}

func TestEnergyCostCountsBandValue(t *testing.T) {
	req := liveRequest()
	applyCheapBand(&req.Batteries[0], cheapBand(liveBandInput()))
	pBand := *req.Batteries[0].PBand
	// 3000 Wh bought at 0.3136 stores 2700 Wh, all below the 27500 Wh top
	assert.InDelta(t, 2700*pBand, cheapBandValue(req.Batteries[0], 22500), 1e-9)
	// above the top only the part up to it counts
	assert.InDelta(t, (27500-19800)*pBand, cheapBandValue(req.Batteries[0], 30000), 1e-9)
}

func TestExplainCheapBand(t *testing.T) {
	start := time.Date(2026, 10, 7, 18, 19, 0, 0, time.UTC)
	timestamps := []time.Time{start, start.Add(627 * time.Second), start.Add(1527 * time.Second), start.Add(2427 * time.Second)}
	req := liveRequest()
	result := func(charge, imp []float32) optimizer.OptimizationResult {
		return optimizer.OptimizationResult{GridImport: imp, GridExport: make([]float32, 4), Batteries: []optimizer.BatteryResult{{ChargingPower: charge}, {}}}
	}
	for _, tc := range []struct {
		name        string
		charge, imp []float32
		sInitial    float32
		want        string
		next        int
	}{
		{"charging now", []float32{1900, 0, 0, 0}, []float32{2200, 300, 280, 300}, 19800, bandStatusCharging, -1},
		{"cheap slot later", []float32{0, 0, 2760, 0}, []float32{310, 290, 3040, 300}, 19800, bandStatusWaitingCheap, 2},
		{"solar later", []float32{0, 0, 0, 2000}, []float32{310, 290, 280, 100}, 19800, bandStatusWaitingSolar, 3},
		{"nothing cheap", []float32{0, 0, 0, 0}, []float32{310, 290, 280, 300}, 19800, bandStatusNothingCheap, -1},
		{"tiny residual is not a plan", []float32{0, 5, 0, 0}, []float32{310, 295, 280, 300}, 19800, bandStatusNothingCheap, -1},
		{"floor first", []float32{1900, 0, 0, 0}, []float32{2200, 300, 280, 300}, 13750, bandStatusBelowMinimum, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			band := cheapBand(liveBandInput())
			req.Batteries[0].SInitial = tc.sInitial
			explainCheapBand(band, req, result(tc.charge, tc.imp), timestamps, 0)
			assert.Equal(t, tc.want, band.Status)
			if tc.next < 0 {
				assert.Nil(t, band.NextStart)
				return
			}
			require.NotNil(t, band.NextStart)
			assert.Equal(t, timestamps[tc.next], *band.NextStart)
			assert.InDelta(t, float64(req.TimeSeries.PN[tc.next])*1e3, *band.NextPrice, 1e-6)
		})
	}
}

func TestCheapBandReference(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	site := &Site{log: util.NewLogger("band-test")}
	now := time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC)

	cfg := EnergyIntelligenceSettings{}
	ref := site.cheapBandReference(cfg, now)
	assert.Nil(t, ref.Price, "no history is no reference")
	assert.Equal(t, 30, ref.Days)

	// 95 slots are less than a day of history
	for i := range 95 {
		price := 0.30 + float64(i%3)*0.05 // 0.30 0.35 0.40, median 0.35
		require.NoError(t, metrics.PersistTariffs(now.Add(-time.Duration(i+1)*15*time.Minute), &price, nil, nil, nil))
	}
	site.cheapBand = cheapBandCache{}
	assert.Nil(t, site.cheapBandReference(cfg, now).Price)

	price := 0.35
	require.NoError(t, metrics.PersistTariffs(now.Add(-96*15*time.Minute), &price, nil, nil, nil))
	site.cheapBand = cheapBandCache{}
	ref = site.cheapBandReference(cfg, now)
	require.NotNil(t, ref.Price)
	assert.Equal(t, 96, ref.Samples)
	assert.InDelta(t, 0.35, *ref.Median, 1e-9)
	assert.InDelta(t, 0.315, *ref.Price, 1e-9, "10 % below the median")
	assert.Equal(t, 1.0, ref.Covered, "a 30-day window with one day of history says so")

	// cached for the day
	late := 0.10
	require.NoError(t, metrics.PersistTariffs(now.Add(-97*15*time.Minute), &late, nil, nil, nil))
	assert.Equal(t, 96, site.cheapBandReference(cfg, now.Add(time.Hour)).Samples)

	// 89 days back: inside 90 days, outside 30
	old := 0.90
	require.NoError(t, metrics.PersistTariffs(now.AddDate(0, 0, -89), &old, nil, nil, nil))
	margin, days := 0.0, 90
	cfg.CheapBandMargin, cfg.CheapBandDays = &margin, &days
	ref = site.cheapBandReference(cfg, now)
	require.NotNil(t, ref.Price)
	assert.InDelta(t, 0.35, *ref.Price, 1e-9, "margin 0 is a value, not the default")
	assert.Equal(t, 98, ref.Samples, "the 90-day window reaches the old slot")
	assert.Equal(t, 89.0, ref.Covered)
	assert.Equal(t, 90, ref.Days)

	fixed := 0.28
	cfg.CheapBandPrice = &fixed
	ref = site.cheapBandReference(cfg, now)
	assert.Equal(t, "fixed", ref.Source)
	assert.Equal(t, 0.28, *ref.Price)
}

func TestEnergyIntelligenceCheapBandValidation(t *testing.T) {
	base := EnergyIntelligenceSettings{SettlementMode: "simulation"}
	require.NoError(t, base.validate())
	zero, ninety, sixty := 0, 90, 60
	negative, high, ok := -1.0, 91.0, 10.0
	for name, cfg := range map[string]EnergyIntelligenceSettings{
		"days 0":       {SettlementMode: "simulation", CheapBandDays: &zero},
		"days 60":      {SettlementMode: "simulation", CheapBandDays: &sixty},
		"margin < 0":   {SettlementMode: "simulation", CheapBandMargin: &negative},
		"margin > 90":  {SettlementMode: "simulation", CheapBandMargin: &high},
		"price absurd": {SettlementMode: "simulation", CheapBandPrice: &high},
	} {
		assert.Error(t, cfg.validate(), name)
	}
	valid := EnergyIntelligenceSettings{SettlementMode: "simulation", CheapBandDays: &ninety, CheapBandMargin: &ok, CheapBandPrice: &negative}
	assert.NoError(t, valid.validate())
}

// TestCheapBandSolverIntegration drives a real companion with the request evcc builds.
func TestCheapBandSolverIntegration(t *testing.T) {
	url := os.Getenv("EVCC_ENERGY_SOLVER_TEST_URL")
	if url == "" {
		t.Skip("set EVCC_ENERGY_SOLVER_TEST_URL to the isolated companion")
	}
	t.Setenv("OPTIMIZER_URI", url)
	client, err := optimizer.NewClientWithResponses(url)
	require.NoError(t, err)
	site := &Site{log: util.NewLogger("band-test"), optimizerClient: client}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, site.requireEnergyCapabilities(ctx, "battery_band", "minimum_run"))

	// 8 quarter hours: two cheap slots below the 0.36063 reference, the rest above it
	prices := []float32{.0005, .0004, .00033, .00034, .00045, .00039, .00037, .00048}
	req := energyRequest{OptimizationInput: optimizer.OptimizationInput{EtaC: eta, EtaD: eta, Strategy: optimizer.OptimizerStrategy{
		ChargingStrategy: optimizer.OptimizerStrategyChargingStrategy("none"), DischargingStrategy: optimizer.OptimizerStrategyDischargingStrategy("none"),
	}}}
	for _, p := range prices {
		req.TimeSeries.Dt = append(req.TimeSeries.Dt, 900)
		req.TimeSeries.Gt = append(req.TimeSeries.Gt, 300)
		req.TimeSeries.Ft = append(req.TimeSeries.Ft, 0)
		req.TimeSeries.PN = append(req.TimeSeries.PN, p)
		req.TimeSeries.PE = append(req.TimeSeries.PE, .0000786)
	}
	in := liveBandInput()
	in.pa = terminalStorageValue(.00033)
	vehicle := energyBattery{BatteryConfig: optimizer.BatteryConfig{ChargeFromGrid: true, CMin: 1380, CMax: 11040, PA: in.pa, SInitial: 19800, SMax: 55000, SMin: 16500}}
	req.Batteries = []energyBattery{vehicle}

	unset, err := site.solveEnergy(ctx, req)
	require.NoError(t, err)
	assert.InDelta(t, 0, sum(unset.Batteries[0].ChargingPower), .1, "without a band nothing is cheap enough")

	applyCheapBand(&req.Batteries[0], cheapBand(in))
	res, err := site.solveEnergy(ctx, req)
	require.NoError(t, err)
	for t2, charge := range res.Batteries[0].ChargingPower {
		if charge > .1 {
			assert.Less(t, float64(prices[t2])*1e3, *in.ref.Price, "slot %d charged above the reference", t2)
		}
	}
	// both cheap slots at full power: 2 x 2760 Wh AC, 4968 Wh stored
	assert.InDelta(t, 5520, sum(res.Batteries[0].ChargingPower), .5)
	assert.LessOrEqual(t, res.Batteries[0].StateOfCharge[7], float32(27500.5))

	// a run 10 minutes into its 30-minute minimum continues through the expensive first
	// slots at minimum power instead of stopping
	band := cheapBand(in)
	start := time.Now().Add(-10 * time.Minute)
	setCheapBandRun(band, &cheapBandRun{start: start}, vehicle.BatteryConfig, start.Add(10*time.Minute))
	req.Batteries[0] = vehicle
	applyCheapBand(&req.Batteries[0], band)
	require.NotNil(t, req.Batteries[0].CRunElapsed)
	res, err = site.solveEnergy(ctx, req)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, float64(res.Batteries[0].ChargingPower[0]), 1380.0/4-.1)
	assert.GreaterOrEqual(t, float64(res.Batteries[0].ChargingPower[1]), 1380.0/4-.1)

	// a full home battery holds while the car charges in its band, unless allowed
	home := energyBattery{BatteryConfig: optimizer.BatteryConfig{ChargeFromGrid: true, CMax: 6000, DMax: 6000, PA: in.pa, SCapacity: 19320, SInitial: 18000, SMax: 19320, SMin: 966}}
	for _, fromBattery := range []bool{false, true} {
		in.fromBattery = fromBattery
		req.Batteries = []energyBattery{vehicle, home}
		applyCheapBand(&req.Batteries[0], cheapBand(in))
		res, err := site.solveEnergy(ctx, req)
		require.NoError(t, err)
		var fed float64
		for t2, charge := range res.Batteries[0].ChargingPower {
			if charge > .1 {
				fed += float64(res.Batteries[1].DischargingPower[t2])
			}
		}
		if fromBattery {
			assert.Greater(t, fed, 100.0, "allowed: the battery covers slots the car charges in")
		} else {
			assert.InDelta(t, 0, fed, .1, "default: the battery holds while the car charges")
		}
	}
}

func sum(v []float32) float64 {
	var res float64
	for _, x := range v {
		res += float64(x)
	}
	return res
}

// TestLoadpointRequestCheapBand runs the configured band through loadpointRequest: the
// solver battery itself, floor included, stays exactly as it was, the band rides alongside.
func TestLoadpointRequestCheapBand(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))

	now := time.Now()
	for i := range 2 * 96 {
		price := 0.4007
		require.NoError(t, metrics.PersistTariffs(now.Add(-time.Duration(i+1)*15*time.Minute).Truncate(15*time.Minute), &price, nil, nil, nil))
	}

	ctrl := gomock.NewController(t)
	v := api.NewMockVehicle(ctrl)
	v.EXPECT().Capacity().Return(55.0).AnyTimes()
	v.EXPECT().GetTitle().Return("Calypso").AnyTimes()
	require.NoError(t, config.Vehicles().Add(config.NewStaticDevice(config.Named{Name: "calypso"}, api.Vehicle(v))))

	lp := loadpoint.NewMockAPI(ctrl)
	lp.EXPECT().GetVehicle().Return(v).AnyTimes()
	lp.EXPECT().GetSoc().Return(36.0).AnyTimes()
	lp.EXPECT().EffectiveMinSoc().Return(30).AnyTimes()
	lp.EXPECT().EffectiveLimitSoc().Return(100).AnyTimes()
	lp.EXPECT().GetLimitEnergy().Return(0.0).AnyTimes()
	lp.EXPECT().GetChargedEnergy().Return(0.0).AnyTimes()
	lp.EXPECT().GetTitle().Return("Wallbox Links").AnyTimes()
	lp.EXPECT().EffectiveMinPower().Return(1380.0).AnyTimes()
	lp.EXPECT().EffectiveMaxPower().Return(11040.0).AnyTimes()
	lp.EXPECT().GetMode().Return(api.ModePV).AnyTimes()
	lp.EXPECT().GetStatus().Return(api.StatusB).AnyTimes()
	lp.EXPECT().GetSmartCostLimit().Return(nil).AnyTimes()
	lp.EXPECT().EffectivePlanStrategy().Return(api.PlanStrategy{}).AnyTimes()
	lp.EXPECT().GetPlanGoal().Return(0.0, false).AnyTimes()
	lp.EXPECT().EffectivePriority().Return(0).AnyTimes()

	site := &Site{log: util.NewLogger("band-test")}
	without, detail := site.loadpointRequest(lp, 8, 15*time.Minute, nil, 0.0003136)
	assert.Nil(t, detail.band, "band not configured")

	settings.SetInt("vehicle.calypso."+keys.CheapBandSoc, 50)
	with, detail := site.loadpointRequest(lp, 8, 15*time.Minute, nil, 0.0003136)
	assert.Equal(t, without, with, "the band never changes the battery the solver sees")
	assert.Equal(t, float32(16500), with.SMin, "floor stays a soft minimum at minSoc")
	require.NotNil(t, detail.band)
	require.True(t, detail.band.active)
	assert.Equal(t, 27500.0, detail.band.sBand)
	assert.Equal(t, vehicle.DefaultCheapBandMinRun*60.0, detail.band.minRun)
	require.NotNil(t, detail.band.Reference.Price)
	assert.InDelta(t, 0.4007*0.9, *detail.band.Reference.Price, 1e-9)
	assert.InDelta(t, 1, eta*(float64(terminalStorageValue(0.0003136))+detail.band.pBand)*1e3 / *detail.band.Reference.Price, 1e-9)

	settings.SetInt("vehicle.calypso."+keys.CheapBandSoc, 0)
	_, detail = site.loadpointRequest(lp, 8, 15*time.Minute, nil, 0.0003136)
	assert.Nil(t, detail.band, "0 turns the band off")
}

// TestRobustBandPartialEVAction: a band vehicle planned at partial power in a slot at or
// below the reference runs at full power, capped to what still fits; above it, or while a
// run is only held to its minimum, the planned power stands.
func TestRobustBandPartialEVAction(t *testing.T) {
	req := liveRequest()
	req.TimeSeries.PN[0] = .0003136 // below the 0.36063 reference
	details := requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeVehicle, mode: api.ModePV, band: cheapBand(liveBandInput())}, {Type: batteryTypeBattery}}}
	full := 11040.0 * 627 / 3600
	base := optimizer.OptimizationResult{Batteries: []optimizer.BatteryResult{
		{ChargingPower: []float32{900}, DischargingPower: []float32{0}},
		{ChargingPower: []float32{0}, DischargingPower: []float32{0}},
	}}

	actions, err := executableFirstActions(req, details, base)
	require.NoError(t, err)
	require.NotEmpty(t, actions)
	for _, action := range actions {
		assert.InDelta(t, full, *action.request.Batteries[0].FirstStepCharge, 1e-6)
	}

	// only the room below the ceiling can be stored
	near := liveRequest()
	near.TimeSeries.PN[0] = .0003136
	near.Batteries[0].SInitial = 54900
	actions, err = executableFirstActions(near, details, base)
	require.NoError(t, err)
	assert.InDelta(t, 100/eta, *actions[0].request.Batteries[0].FirstStepCharge, 1e-3)

	// above the reference the planned power stands, held run or not
	req.TimeSeries.PN[0] = .0005002
	actions, err = executableFirstActions(req, details, base)
	require.NoError(t, err)
	assert.InDelta(t, 900, *actions[0].request.Batteries[0].FirstStepCharge, 1e-6)
}

func TestTrackCheapBandRun(t *testing.T) {
	ctrl := gomock.NewController(t)
	status, soc := api.StatusC, 36.0
	lp := loadpoint.NewMockAPI(ctrl)
	lp.EXPECT().GetStatus().DoAndReturn(func() api.ChargeStatus { return status }).AnyTimes()
	lp.EXPECT().GetSoc().DoAndReturn(func() float64 { return soc }).AnyTimes()
	lp.EXPECT().EffectiveMinSoc().Return(30).AnyTimes()

	site := &Site{}
	start := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	cycle := func(now time.Time) *cheapBandRun {
		runs := make(map[int]cheapBandRun)
		run := site.trackCheapBandRun(0, lp, runs, now)
		site.cheapBandRuns = runs
		return run
	}

	run := cycle(start)
	require.NotNil(t, run)
	assert.Equal(t, start, run.start)
	assert.False(t, run.fromFloor)
	assert.Equal(t, start, cycle(start.Add(10*time.Minute)).start, "the start of a run in progress is kept")

	status = api.StatusB
	assert.Nil(t, cycle(start.Add(11*time.Minute)), "not charging: no run")
	status, soc = api.StatusC, 25
	run = cycle(start.Add(12 * time.Minute))
	assert.Equal(t, start.Add(12*time.Minute), run.start, "a new run starts fresh")
	assert.True(t, run.fromFloor)
}

// TestCheapBandStorageFeed: by default home batteries hold while the car charges in its
// band; the setting allows feeding, and the floor's charging is never restricted.
func TestCheapBandStorageFeed(t *testing.T) {
	for _, tc := range []struct {
		name        string
		fromBattery bool
		soc         float64
		want        *bool
	}{
		{"default refuses", false, 36, lo.ToPtr(false)},
		{"setting allows", true, 36, nil},
		{"floor charging is not band charging", false, 25, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := liveBandInput()
			in.fromBattery, in.soc = tc.fromBattery, tc.soc
			req := liveRequest()
			applyCheapBand(&req.Batteries[0], cheapBand(in))
			assert.Equal(t, tc.want, req.Batteries[0].BandStorageFeed)
			assert.Nil(t, req.Batteries[1].BandStorageFeed)
			encoded, err := json.Marshal(req.Batteries[0])
			require.NoError(t, err)
			assert.Equal(t, tc.want != nil, strings.Contains(string(encoded), `"band_storage_feed":false`))
		})
	}
}
