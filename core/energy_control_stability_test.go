package core

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/server/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestCurrentSlotSuggestionIgnoresGridRounding(t *testing.T) {
	detail := batteryDetail{Type: batteryTypeBattery}
	cfg := optimizer.BatteryConfig{SInitial: 4000, SMin: 1000, SMax: 19000}
	for _, hours := range []float64{.25, 44.0 / 3600} {
		for _, charging := range []bool{true, false} {
			res := optimizer.BatteryResult{ChargingPower: []float32{0}, DischargingPower: []float32{0}}
			if charging {
				res.ChargingPower[0] = float32(104.5573 * hours)
			} else {
				res.DischargingPower[0] = float32(246.6815 * hours)
			}
			s := currentSlotSuggestion(detail, cfg, res, 0.00001, 0.00001, hours)
			assert.Equal(t, api.BatteryNormal.String(), s.Action, "rounding is not a grid-charge/export command")
			if charging {
				s = currentSlotSuggestion(detail, cfg, res, float32(hours), 0, hours)
				assert.Equal(t, api.BatteryCharge.String(), s.Action, "real 1W import remains a grid action")
			}
		}
	}
}

func controlTestActions(seconds int) (energyRequest, []energyFirstAction) {
	req := energyRequest{OptimizationInput: optimizer.OptimizationInput{TimeSeries: optimizer.TimeSeries{Dt: []int{seconds}, PN: []float32{.0002, .0004}}}}
	var actions []energyFirstAction
	for _, mode := range []string{"normal", "hold", "charge"} {
		charge, discharge := 0.0, 0.0
		if mode == "normal" {
			discharge = 246.6815 * float64(seconds) / 3600
		} else if mode == "charge" {
			charge = 6000 * float64(seconds) / 3600
		}
		actions = append(actions, energyFirstAction{name: mode, request: energyRequest{Batteries: []energyBattery{{FirstStepCharge: &charge, FirstStepDischarge: &discharge}}}})
	}
	return req, actions
}

func TestEnergyControlCapturedCostTie(t *testing.T) {
	// Captured 2026-10-03 13:49:46 CEST; all feasible actions matter to the regret baseline.
	costs := [][]float64{
		{1.0489319572195503, 3.137807779942462, -.7327395221248175},
		{1.045820022, 3.132275196, -.732628098},
		{1.038118631, 3.078497242, -.678947225},
	}
	req, actions := controlTestActions(614)
	_, raw := selectEnergyAction(req, actions, costs, "")
	require.Equal(t, "hold", raw.SelectedAction)
	i, sticky := selectEnergyAction(req, actions, costs, "charge")
	assert.Equal(t, "charge", actions[i].name)
	assert.Equal(t, "economicTie", sticky.Reason)
	require.NotNil(t, sticky.RegretGap)
	assert.InDelta(t, .00001434, *sticky.RegretGap, .00000001)
	assert.Less(t, *sticky.RegretGap, *sticky.Materiality)
	_, shorter := controlTestActions(44)
	assert.InDelta(t, 44.0/614, energyActionMateriality(req, shorter[1], shorter[2])/energyActionMateriality(req, actions[1], actions[2]), 1e-9)
	for i := range req.TimeSeries.PN {
		req.TimeSeries.PN[i] *= 100
	}
	for _, row := range costs {
		for i := range row {
			row[i] *= 100
		}
	}
	_, scaled := selectEnergyAction(req, actions, costs, "charge")
	assert.Equal(t, sticky.SelectedAction, scaled.SelectedAction, "currency denomination cannot change control")
	assert.InDelta(t, 100**sticky.Materiality, *scaled.Materiality, 1e-6)
}

func TestEnergyControlSelectorActuatesAndReleases(t *testing.T) {
	enableAutomatic(t)
	now := time.Date(2026, 10, 3, 11, 48, 0, 0, time.UTC)
	ctrl := gomock.NewController(t)
	batCon := api.NewMockBatteryController(ctrl)
	var bat api.Meter = &struct {
		api.Meter
		api.BatteryController
	}{BatteryController: batCon}
	site := &Site{log: util.NewLogger("test"), optimizerNow: func() time.Time { return now }, batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{Name: "bat"}, bat)}}
	req, actions := controlTestActions(900)
	apply := func(costs [][]float64, feasible []energyFirstAction) {
		t.Helper()
		i, economics := selectEnergyAction(req, feasible, costs, site.energyPreferredAction())
		require.GreaterOrEqual(t, i, 0)
		economics.validUntil = now.Add(44 * time.Second)
		mode, err := api.BatteryModeString(feasible[i].name)
		require.NoError(t, err)
		site.setOptimizerBatteryMode(optimizerDecision{mode: mode, suggestedMode: mode, economics: economics, price: .2})
		site.updateBatteryMode(false, api.Rate{Value: .2})
	}
	// A meaningful cheap opportunity must actuate within a single control cycle.
	batCon.EXPECT().SetBatteryMode(api.BatteryCharge)
	apply([][]float64{{2, 4, 0}, {2, 4, 0}, {1, 3, 0}}, actions)
	assert.Equal(t, api.BatteryCharge, site.GetBatteryMode())
	assert.Equal(t, api.BatteryUnknown, site.optimizerBatteryModePending)
	now = now.Add(30 * time.Second)
	apply([][]float64{{2, 4, .0001}, {2, 4, 0}, {2, 4, .00001}}, actions)
	assert.Equal(t, api.BatteryCharge, site.GetBatteryMode(), "tiny regret reversals do not restart control")
	// A material change is not blocked by a dwell timer.
	batCon.EXPECT().SetBatteryMode(api.BatteryHold)
	apply([][]float64{{2, 2, 2}, {1, 1, 1}, {3, 3, 3}}, actions)
	assert.Equal(t, api.BatteryHold, site.GetBatteryMode())
	// An unavailable incumbent is never retained for hysteresis.
	batCon.EXPECT().SetBatteryMode(api.BatteryNormal)
	apply([][]float64{{1, 1, 1}}, actions[:1])
	assert.Equal(t, "preferredUnavailable", site.optimizerEconomics.Reason)
	batCon.EXPECT().SetBatteryMode(api.BatteryCharge)
	apply([][]float64{{2, 4, 0}, {2, 4, 0}, {1, 3, 0}}, actions)
	// Freshness ends at the modeled first-slot boundary, not six minutes later.
	now = now.Add(44 * time.Second)
	batCon.EXPECT().SetBatteryMode(api.BatteryNormal)
	site.updateBatteryMode(false, api.Rate{Value: .2})
	assert.Equal(t, api.BatteryNormal, site.GetBatteryMode())
	assert.Empty(t, site.energyPreferredAction())
}

func TestEnergyControlProvenanceAndPendingClock(t *testing.T) {
	enableAutomatic(t)
	now := time.Date(2026, 10, 3, 11, 0, 0, 0, time.UTC)
	params := make(chan util.Param, 64)
	site := &Site{log: util.NewLogger("test"), optimizerNow: func() time.Time { return now }, valueChan: params}
	// Cached robust diagnostics alone cannot bypass confirmation.
	site.energyInsights.Scenarios = &energyScenarios{Status: "evaluated", Control: &energyControlEconomics{SelectedAction: "charge"}}
	d := optimizerDecision{mode: api.BatteryCharge, suggestedMode: api.BatteryCharge, price: .2}
	site.setOptimizerBatteryMode(d)
	pub := lastOptimizerDecision(t, params)
	require.NotNil(t, pub.EligibleAt)
	assert.Equal(t, now.Add(5*time.Minute), *pub.EligibleAt)
	assert.Equal(t, "confirmation", pub.ControlStrategy)
	assert.Nil(t, pub.Economics)
	now = now.Add(-time.Minute)
	site.setOptimizerBatteryMode(d)
	assert.Equal(t, "clockChanged", lastOptimizerDecision(t, params).ResetReason)
	now = now.Add(7 * time.Minute)
	site.setOptimizerBatteryMode(d)
	assert.Equal(t, "candidateExpired", lastOptimizerDecision(t, params).ResetReason)
	now = now.Add(5 * time.Minute)
	site.setOptimizerBatteryMode(d)
	pub = lastOptimizerDecision(t, params)
	assert.Equal(t, api.BatteryCharge, pub.Mode)
	assert.Nil(t, pub.PendingSince)
	// Successful robust provenance clears a legacy pending mode immediately.
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryHold, suggestedMode: api.BatteryHold})
	d.economics = &energyControlEconomics{SelectedAction: "charge", validUntil: now.Add(time.Minute)}
	site.setOptimizerBatteryMode(d)
	pub = lastOptimizerDecision(t, params)
	assert.Equal(t, "economic", pub.ControlStrategy)
	assert.Nil(t, pub.PendingMode)
	assert.Equal(t, int64(60000), pub.ValidFor)
	_, err := json.Marshal(pub)
	require.NoError(t, err)
	// Price, economic veto, loss of advice, and disable remain authoritative.
	_, ok := site.batterySuggestionMode(api.Rate{Value: .3})
	assert.False(t, ok)
	site.setOptimizerBatteryMode(optimizerDecision{economics: d.economics, chargeVetoed: true, vetoReason: vetoReasonPayback})
	assert.Equal(t, api.BatteryUnknown, site.optimizerBatteryMode)
	site.setOptimizerBatteryMode(d)
	site.setOptimizerBatteryMode(optimizerDecision{})
	assert.Nil(t, site.optimizerEconomics)
	settings.SetBool(keys.OptimizerAutomatic, false)
	site.setOptimizerBatteryMode(d)
	assert.Equal(t, api.BatteryUnknown, site.optimizerBatteryMode)
	site.ResetOptimizerBatteryMode()
	assert.Nil(t, site.optimizerEconomics)
	assert.True(t, site.optimizerModeExpiresAt.IsZero())
}

func TestEnergyControlPublishesDeviceLimitAndDisagreement(t *testing.T) {
	enableAutomatic(t)
	ctrl := gomock.NewController(t)
	full, fullControl := batteryControlMock(ctrl, 90, 80)
	params := make(chan util.Param, 64)
	site := &Site{log: util.NewLogger("test"), valueChan: params, batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{Name: "full"}, full)}}
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge, suggestedMode: api.BatteryCharge, economics: &energyControlEconomics{SelectedAction: "charge"}})
	fullControl.EXPECT().SetBatteryMode(api.BatteryHold)
	site.updateBatteryMode(false, api.Rate{})
	pub := lastOptimizerDecision(t, params)
	assert.Equal(t, api.BatteryCharge, site.GetBatteryMode(), "site requested charge")
	assert.Equal(t, api.BatteryHold, pub.AppliedMode, "device limit actually commanded hold")
	assert.Empty(t, site.energyPreferredAction(), "a limited device is not a confirmed applied charge incumbent")
	empty, emptyControl := batteryControlMock(ctrl, 50, 80)
	site.batteryMeters = append(site.batteryMeters, config.NewStaticDevice(config.Named{Name: "empty"}, empty))
	emptyControl.EXPECT().SetBatteryMode(api.BatteryCharge)
	require.NoError(t, site.applyBatteryMode(api.BatteryCharge))
	assert.Equal(t, api.BatteryUnknown, lastOptimizerDecision(t, params).AppliedMode, "different successful commands have no common mode")
}

func TestEnergyControlCanonicalEquivalentActions(t *testing.T) {
	req := energyRequest{OptimizationInput: optimizer.OptimizationInput{EtaC: .9, EtaD: .9, TimeSeries: optimizer.TimeSeries{Dt: []int{44}, Gt: []float32{0}, Ft: []float32{0}}}, Batteries: []energyBattery{{BatteryConfig: optimizer.BatteryConfig{SInitial: 4000, SMin: 1000, SMax: 19000, CMax: 6000, DMax: 6000, ChargeFromGrid: true}}}}
	base := optimizer.OptimizationResult{Batteries: []optimizer.BatteryResult{{ChargingPower: []float32{0}, DischargingPower: []float32{0}}}}
	actions, err := executableFirstActions(req, requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeBattery}}}, base)
	require.NoError(t, err)
	require.Len(t, actions, 2)
	assert.Equal(t, "normal", actions[0].name, "equivalent idle modes canonicalize to normal")
	assert.Equal(t, "charge", actions[1].name)
}
