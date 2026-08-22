package core

import (
	"sync"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/types"
	"github.com/evcc-io/evcc/util"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGridChargeJustified(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pn       []float32 // currency/Wh
		expected bool
	}{
		{"no forecast", nil, false},
		{"single slot", []float32{0.0002}, false},
		{"spread covers losses", []float32{0.0002, 0.00025}, true}, // 0.25 >= 0.20/0.81
		{"spread too small", []float32{0.0002, 0.00024}, false},    // 0.24 < 0.20/0.81
		// dividing a negative price by eta² would lower the bar instead of raising
		// it — energy at zero or negative cost is always worth storing
		{"negative price justified", []float32{-0.00005, -0.00006, -0.00007}, true},
		{"zero price justified", []float32{0, -0.00006}, true},
	} {
		assert.Equal(t, tc.expected, gridChargeJustified(tc.pn), tc.name)
	}
}

func TestChargePaybackJustified(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pn        []float32 // currency/Wh
		soc       []float32 // Wh at end of slot
		discharge []float32 // Wh per slot, AC-side
		sInitial  float32   // Wh
		expected  bool
	}{
		// buy 0.10, give back at 0.35: 0.35 >= (0.10+0.03)/0.81 = 0.160
		{"fat margin allowed", []float32{0.0001, 0.00035}, []float32{6375, 4990}, []float32{0, 1375}, 5000, true},
		// buy 0.20, give back at 0.25: the eta² spread passes (0.25 >= 0.247) but
		// 0.25 < (0.20+0.03)/0.81 = 0.284 — margin evaporates in the risk buffer
		{"thin margin blocked", []float32{0.0002, 0.00025}, []float32{6375, 4990}, []float32{0, 1375}, 5000, false},
		// the energy charged now is given back in the FIRST discharge window (0.24,
		// SoC back at initial after t=1); a later 0.40 slot must not rescue the
		// decision the way a whole-plan average would
		{"payback priced at first soc return", []float32{0.0002, 0.00024, 0.0004}, []float32{6375, 4990, 3000}, []float32{0, 1385, 1990}, 5000, false},
		// SoC returns without discharge accounting for it
		{"no discharge at soc return blocked", []float32{0.0002, 0.0004}, []float32{5100, 4900}, []float32{0, 0}, 5000, false},
		// plan keeps the energy past the horizon: its value rests on the terminal
		// value, which is below the buy price by construction
		{"never returns blocked", []float32{0.0001, 0.00035, 0.00035}, []float32{8000, 7500, 7000}, []float32{0, 400, 400}, 5000, false},
		{"negative price always justified", []float32{-0.00005, 0.0001}, []float32{6000, 4000}, []float32{0, 2000}, 5000, true},
		{"single slot blocked", []float32{0.0002}, []float32{6000}, []float32{0}, 5000, false},
		{"truncated series blocked", []float32{0.0001, 0.00035, 0.00035}, []float32{6375}, []float32{0}, 5000, false},
	} {
		assert.Equal(t, tc.expected, chargePaybackJustified(tc.pn, tc.soc, tc.discharge, tc.sInitial), tc.name)
	}
}

// chargeReqRes builds a single-battery optimizer request/response pair
func chargeReqRes(pn []float32, sInitial float32, soc, discharge []float32) (optimizer.OptimizationInput, optimizer.OptimizationResult) {
	req := optimizer.OptimizationInput{
		TimeSeries: optimizer.TimeSeries{PN: pn},
		Batteries:  []optimizer.BatteryConfig{{SInitial: sInitial}},
	}
	res := optimizer.OptimizationResult{
		Batteries: []optimizer.BatteryResult{{StateOfCharge: soc, DischargingPower: discharge}},
	}
	return req, res
}

func TestBatteryModeCandidate(t *testing.T) {
	pn := []float32{0.0002, 0.0004} // cheap now, expensive later

	bat := func(name string) batteryDetail {
		return batteryDetail{Type: batteryTypeBattery, Name: name, controllable: true}
	}

	lpID := 0
	vehicle := batteryDetail{Type: batteryTypeVehicle, loadpoint: &lpID, controllable: true}

	// plan that pays a slot-0 grid charge back at 0.40
	req, res := chargeReqRes(pn, 5000, []float32{6375, 4990}, []float32{0, 1375})

	for _, tc := range []struct {
		name        string
		suggestions map[string]types.Suggestion
		details     []batteryDetail
		expected    optimizerDecision
	}{
		{"no batteries", nil, nil, optimizerDecision{}},
		{"hold", map[string]types.Suggestion{"battery:a": {Action: "hold"}}, []batteryDetail{bat("a")}, optimizerDecision{mode: api.BatteryHold}},
		{"holdcharge", map[string]types.Suggestion{"battery:a": {Action: "holdcharge"}}, []batteryDetail{bat("a")}, optimizerDecision{mode: api.BatteryHoldCharge}},
		{"normal", map[string]types.Suggestion{"battery:a": {Action: "normal"}}, []batteryDetail{bat("a")}, optimizerDecision{mode: api.BatteryNormal}},
		{"discharge maps to normal", map[string]types.Suggestion{"battery:a": {Action: "discharge"}}, []batteryDetail{bat("a")}, optimizerDecision{mode: api.BatteryNormal}},
		{"charge with spread and payback", map[string]types.Suggestion{"battery:a": {Action: "charge"}}, []batteryDetail{bat("a")}, optimizerDecision{mode: api.BatteryCharge, price: 0.2}},
		{"conflict", map[string]types.Suggestion{"battery:a": {Action: "hold"}, "battery:b": {Action: "normal"}}, []batteryDetail{bat("a"), bat("b")}, optimizerDecision{vetoReason: vetoReasonForcedIdle}},
		{"vehicle ignored", map[string]types.Suggestion{"loadpoint:0": {Action: "charge"}}, []batteryDetail{vehicle}, optimizerDecision{}},
	} {
		d := batteryModeCandidate(tc.suggestions, req, res, tc.details)
		assert.Equal(t, tc.expected.mode, d.mode, tc.name)
		assert.Equal(t, tc.expected.chargeVetoed, d.chargeVetoed, tc.name)
		assert.Equal(t, tc.expected.vetoReason, d.vetoReason, tc.name)
		assert.InDelta(t, tc.expected.price, d.price, 1e-6, tc.name)
	}

	charge := map[string]types.Suggestion{"battery:a": {Action: "charge"}}
	details := []batteryDetail{bat("a")}

	// charge is vetoed when the spread cannot recover the round-trip losses
	flatReq, flatRes := chargeReqRes([]float32{0.0002, 0.0002}, 5000, []float32{6375, 4990}, []float32{0, 1375})
	d := batteryModeCandidate(charge, flatReq, flatRes, details)
	assert.Equal(t, api.BatteryUnknown, d.mode, "charge without spread")
	assert.True(t, d.chargeVetoed, "charge without spread sets veto")
	assert.Equal(t, vetoReasonPayback, d.vetoReason)

	// charge is vetoed when the spread passes but the plan's payback is too thin:
	// buy 0.20, give back at 0.25 < (0.20+0.03)/0.81 = 0.284
	thinReq, thinRes := chargeReqRes([]float32{0.0002, 0.00025}, 5000, []float32{6375, 4990}, []float32{0, 1375})
	d = batteryModeCandidate(charge, thinReq, thinRes, details)
	assert.Equal(t, api.BatteryUnknown, d.mode, "thin payback")
	assert.True(t, d.chargeVetoed, "thin payback sets veto")

	// missing battery results cannot justify a charge
	d = batteryModeCandidate(charge, optimizer.OptimizationInput{TimeSeries: optimizer.TimeSeries{PN: pn}}, optimizer.OptimizationResult{}, details)
	assert.Equal(t, api.BatteryUnknown, d.mode, "missing results")
	assert.True(t, d.chargeVetoed, "missing results set veto")
}

// backdatePending ages a pending mode candidate past optimizerBatteryModeConfirmDelay
// so a test can confirm it without waiting on time.Now() alone.
func backdatePending(site *Site) {
	site.Lock()
	if site.optimizerBatteryModePending != api.BatteryUnknown {
		// age it past the confirm delay but comfortably inside the validity
		// window (which only exceeds the confirm delay by a minute), so it
		// confirms regardless of the few microseconds a real clock adds
		site.optimizerBatteryModePendingSince = time.Now().Add(-optimizerBatteryModeConfirmDelay - 30*time.Second)
	}
	site.Unlock()
}

// TestOptimizerBatteryModeFlapping is a comparative test: the undamped legacy
// behavior (apply whatever the last run derived) flaps the battery on every
// run of a hold/normal alternation typical of a degenerate LP optimum on a
// flat price plateau. setOptimizerBatteryMode's damping must not follow it.
func TestOptimizerBatteryModeFlapping(t *testing.T) {
	enableAutomatic(t)

	// hold/normal alternation on a flat price plateau. The undamped legacy
	// behavior applies every derived mode — 8 switches in 8 runs. The damped
	// logic must not follow the flap.
	sequence := []api.BatteryMode{
		api.BatteryHold, api.BatteryNormal, api.BatteryHold, api.BatteryNormal,
		api.BatteryHold, api.BatteryNormal, api.BatteryHold, api.BatteryNormal,
	}

	legacySwitches := 0
	legacyMode := api.BatteryUnknown
	for _, m := range sequence {
		if m != legacyMode {
			legacySwitches++
		}
		legacyMode = m
	}
	assert.Equal(t, 8, legacySwitches, "legacy behavior flaps on every run")

	site := &Site{log: util.NewLogger("foo")}

	switches := 0
	prev := site.optimizerBatteryMode
	for _, m := range sequence {
		site.setOptimizerBatteryMode(optimizerDecision{mode: m})
		// age the pending candidate so confirmation is never blocked on time alone
		backdatePending(site)
		if site.optimizerBatteryMode != prev {
			switches++
		}
		prev = site.optimizerBatteryMode
	}
	assert.Equal(t, 0, switches, "damped control does not follow the flap")

	// a flap around an already-applied idle mode keeps that mode stable
	site = &Site{log: util.NewLogger("foo")}
	site.optimizerBatteryMode = api.BatteryHold
	site.optimizerBatteryModeUpdated = time.Now()
	site.optimizerBatteryModeConfirmedAt = time.Now()

	for _, m := range []api.BatteryMode{api.BatteryNormal, api.BatteryHold, api.BatteryNormal, api.BatteryHold} {
		site.setOptimizerBatteryMode(optimizerDecision{mode: m})
		backdatePending(site)
		assert.Equal(t, api.BatteryHold, site.optimizerBatteryMode, "applied idle mode stays put during flap")
	}

	// a genuine regime change still lands, once two runs agree
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryNormal})
	backdatePending(site)
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryNormal})
	assert.Equal(t, api.BatteryNormal, site.optimizerBatteryMode, "two agreeing runs change the mode")
}

// TestOptimizerBatteryModePendingCleared verifies that toggling automatic mode
// clears pending state in both directions — otherwise a pre-toggle observation
// could confirm a mode right after re-enabling.
func TestOptimizerBatteryModePendingCleared(t *testing.T) {
	enableAutomatic(t)

	site := &Site{log: util.NewLogger("foo")}
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge})
	assert.Equal(t, api.BatteryCharge, site.optimizerBatteryModePending)

	site.ResetOptimizerBatteryMode()
	assert.Equal(t, api.BatteryUnknown, site.optimizerBatteryModePending)
	assert.True(t, site.optimizerBatteryModePendingSince.IsZero())
	assert.Equal(t, api.BatteryUnknown, site.optimizerBatteryMode)

	// a stale pending candidate from before the reset does not immediately
	// confirm just because a fresh run happens to agree with it
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge})
	assert.Equal(t, api.BatteryUnknown, site.optimizerBatteryMode, "first post-reset run only re-opens the pending window")
}

// TestBatterySuggestionModeChargeStaleness covers the charge-only staleness
// guard: a charge decision carries the price it was based on, and a live rate
// that has stepped past it invalidates the decision immediately — well before
// the generic optimizerBatteryModeValidity window would. A stale hold is
// free, so no equivalent guard exists for it.
func TestBatterySuggestionModeChargeStaleness(t *testing.T) {
	enableAutomatic(t)

	site := &Site{log: util.NewLogger("foo")}
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge, price: 0.10})

	// confirm the mode so it is actually applied, not just pending
	backdatePending(site)
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge, price: 0.10})
	require.Equal(t, api.BatteryCharge, site.optimizerBatteryMode)

	// the rate the decision was based on: still followed
	mode, ok := site.batterySuggestionMode(api.Rate{Value: 0.10})
	assert.True(t, ok)
	assert.Equal(t, api.BatteryCharge, mode)

	// within tolerance: still followed
	mode, ok = site.batterySuggestionMode(api.Rate{Value: 0.10 + optimizerChargePriceTolerance/2})
	assert.True(t, ok)
	assert.Equal(t, api.BatteryCharge, mode)

	// the live rate stepped past the price the decision was based on: dropped,
	// even though the decision itself is still fresh (optimizerBatteryModeUpdated
	// was just set above)
	mode, ok = site.batterySuggestionMode(api.Rate{Value: 0.20})
	assert.False(t, ok)
	assert.Equal(t, api.BatteryUnknown, mode)

	// a hold decision is not price-gated: a stale hold is free
	site = &Site{log: util.NewLogger("foo")}
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryHold})
	backdatePending(site)
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryHold})
	mode, ok = site.batterySuggestionMode(api.Rate{Value: 999})
	assert.True(t, ok)
	assert.Equal(t, api.BatteryHold, mode)
}

// lastOptimizerDecision drains pending params and returns the last published
// optimizerDecision, for asserting what a test action published.
func lastOptimizerDecision(t *testing.T, params chan util.Param) optimizerDecisionPublish {
	t.Helper()

	var last optimizerDecisionPublish
	found := false
	for {
		select {
		case p := <-params:
			if p.Key == keys.OptimizerDecision {
				last = p.Val.(optimizerDecisionPublish)
				found = true
			}
		default:
			require.True(t, found, "optimizerDecision was not published")
			return last
		}
	}
}

func TestOptimizerDecisionPublish(t *testing.T) {
	enableAutomatic(t)

	// mode and veto reason round-trip through the published payload
	params := make(chan util.Param, 64)
	site := &Site{log: util.NewLogger("foo"), valueChan: params}

	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge, price: 0.2})
	d := lastOptimizerDecision(t, params)
	assert.Equal(t, api.BatteryUnknown, d.Mode, "first observation not yet applied")
	assert.False(t, d.ChargeVetoed)
	assert.Equal(t, vetoReasonDamping, d.VetoReason, "pending charge candidate explained as damping")

	backdatePending(site)
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge, price: 0.2})
	d = lastOptimizerDecision(t, params)
	assert.Equal(t, api.BatteryCharge, d.Mode, "mode round-trips through publish")
	assert.Equal(t, 0.2, d.Price, "price round-trips through publish")

	site.setOptimizerBatteryMode(optimizerDecision{chargeVetoed: true, vetoReason: vetoReasonPayback})
	d = lastOptimizerDecision(t, params)
	assert.True(t, d.ChargeVetoed, "veto round-trips through publish")
	assert.Equal(t, vetoReasonPayback, d.VetoReason, "veto reason round-trips through publish")
}

func TestUpdateOptimizerLiveRateVeto(t *testing.T) {
	// live-rate divergence is annotation only: it must not touch the applied
	// mode, only explain why a stale charge decision is not currently in effect
	params := make(chan util.Param, 64)
	site := &Site{log: util.NewLogger("foo"), valueChan: params}
	site.optimizerBatteryMode = api.BatteryCharge
	site.optimizerBatteryModeUpdated = time.Now()
	site.optimizerChargePrice = 0.10

	site.updateOptimizerLiveRateVeto(api.Rate{Value: 0.10})
	assert.Equal(t, vetoReasonNone, site.optimizerVetoReason, "decision price honored, no veto")

	site.updateOptimizerLiveRateVeto(api.Rate{Value: 0.35})
	assert.Equal(t, vetoReasonLiveRate, site.optimizerVetoReason, "price step annotated as liveRate")
	assert.Equal(t, api.BatteryCharge, site.optimizerBatteryMode, "annotation does not change the stored mode")
	assert.False(t, site.optimizerChargeVetoed, "annotation does not set the payback-gate veto flag")
	d := lastOptimizerDecision(t, params)
	assert.Equal(t, vetoReasonLiveRate, d.VetoReason, "liveRate reason published")

	// rate back in range clears the annotation
	site.updateOptimizerLiveRateVeto(api.Rate{Value: 0.10})
	assert.Equal(t, vetoReasonNone, site.optimizerVetoReason, "annotation clears once the rate recovers")

	// non-charge modes are not price-guarded
	site.optimizerBatteryMode = api.BatteryHold
	site.updateOptimizerLiveRateVeto(api.Rate{Value: 0.35})
	assert.Equal(t, vetoReasonNone, site.optimizerVetoReason, "hold is not price-guarded")
}

func TestUpdateOptimizerLiveRateVetoConcurrent(t *testing.T) {
	// the RLock fast path and the write-lock re-check must agree with each
	// other under concurrent calls — run under -race
	site := &Site{log: util.NewLogger("foo"), valueChan: make(chan util.Param, 256)}
	site.optimizerBatteryMode = api.BatteryCharge
	site.optimizerBatteryModeUpdated = time.Now()
	site.optimizerChargePrice = 0.10

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rate := 0.10
			if i%2 == 0 {
				rate = 0.35
			}
			site.updateOptimizerLiveRateVeto(api.Rate{Value: rate})
		}(i)
	}
	wg.Wait()
}
