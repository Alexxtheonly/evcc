package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/types"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
)

func TestGridChargeJustified(t *testing.T) {
	limit := 0.20
	negLimit := -0.10

	for _, tc := range []struct {
		name     string
		pn       []float32 // currency/Wh
		limit    *float64  // currency/kWh
		expected bool
	}{
		{"no forecast", nil, nil, false},
		{"single slot", []float32{0.0002}, nil, false},
		{"spread covers losses", []float32{0.0002, 0.00025}, nil, true}, // 0.25 >= 0.20/0.81
		{"spread too small", []float32{0.0002, 0.00024}, nil, false},    // 0.24 < 0.20/0.81
		{"limit exceeded", []float32{0.00021, 0.0004}, &limit, false},
		{"limit respected", []float32{0.00019, 0.0004}, &limit, true},
		// dividing a negative price by eta² would lower the bar instead of raising
		// it — energy at zero or negative cost is always worth storing
		{"negative price justified", []float32{-0.00005, -0.00006, -0.00007}, nil, true},
		{"zero price justified", []float32{0, -0.00006}, nil, true},
		{"negative price above negative limit", []float32{-0.00005, -0.00006}, &negLimit, false},
	} {
		assert.Equal(t, tc.expected, gridChargeJustified(tc.pn, tc.limit), tc.name)
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

func TestOptimizerDecisionFromSuggestions(t *testing.T) {
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
		{"conflict", map[string]types.Suggestion{"battery:a": {Action: "hold"}, "battery:b": {Action: "normal"}}, []batteryDetail{bat("a"), bat("b")}, optimizerDecision{}},
		{"vehicle ignored", map[string]types.Suggestion{"loadpoint:0": {Action: "charge"}}, []batteryDetail{vehicle}, optimizerDecision{}},
	} {
		d := optimizerDecisionFromSuggestions(tc.suggestions, tc.details, req, res, nil)
		assert.Equal(t, tc.expected.mode, d.mode, tc.name)
		assert.Equal(t, tc.expected.chargeVetoed, d.chargeVetoed, tc.name)
		assert.InDelta(t, tc.expected.price, d.price, 1e-6, tc.name)
	}

	charge := map[string]types.Suggestion{"battery:a": {Action: "charge"}}
	details := []batteryDetail{bat("a")}

	// charge is vetoed when the spread cannot recover the round-trip losses
	flatReq, flatRes := chargeReqRes([]float32{0.0002, 0.0002}, 5000, []float32{6375, 4990}, []float32{0, 1375})
	d := optimizerDecisionFromSuggestions(charge, details, flatReq, flatRes, nil)
	assert.Equal(t, api.BatteryUnknown, d.mode, "charge without spread")
	assert.True(t, d.chargeVetoed, "charge without spread sets veto")

	// charge is vetoed when the spread passes but the plan's payback is too thin:
	// buy 0.20, give back at 0.25 < (0.20+0.03)/0.81 = 0.284
	thinReq, thinRes := chargeReqRes([]float32{0.0002, 0.00025}, 5000, []float32{6375, 4990}, []float32{0, 1375})
	d = optimizerDecisionFromSuggestions(charge, details, thinReq, thinRes, nil)
	assert.Equal(t, api.BatteryUnknown, d.mode, "thin payback")
	assert.True(t, d.chargeVetoed, "thin payback sets veto")

	// missing battery results cannot justify a charge
	d = optimizerDecisionFromSuggestions(charge, details, optimizer.OptimizationInput{TimeSeries: optimizer.TimeSeries{PN: pn}}, optimizer.OptimizationResult{}, nil)
	assert.Equal(t, api.BatteryUnknown, d.mode, "missing results")
	assert.True(t, d.chargeVetoed, "missing results set veto")
}

// testBatteryController is a battery meter with control capability
type testBatteryController struct{ api.Meter }

func (testBatteryController) SetBatteryMode(api.BatteryMode) error { return nil }

func controlSite() *Site {
	return &Site{
		log:           util.NewLogger("foo"),
		batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{}, api.Meter(&testBatteryController{}))},
	}
}

func TestRequiredBatteryModeOptimizer(t *testing.T) {
	// fresh optimizer mode is followed
	site := controlSite()
	site.optimizerBatteryControl = true
	site.optimizerBatteryMode = api.BatteryCharge
	site.optimizerBatteryModeUpdated = time.Now()
	assert.Equal(t, api.BatteryCharge, site.requiredBatteryMode(false, api.Rate{}), "fresh mode followed")

	// mode already active: not required again
	site.batteryMode = api.BatteryCharge
	assert.Equal(t, api.BatteryUnknown, site.requiredBatteryMode(false, api.Rate{}), "active mode not re-required")

	// stale optimizer result falls back to normal
	site.optimizerBatteryModeUpdated = time.Now().Add(-optimizerBatteryModeValidity - time.Second)
	assert.Equal(t, api.BatteryNormal, site.requiredBatteryMode(false, api.Rate{}), "stale mode reverts to normal")

	// control disabled: stored mode ignored
	site = controlSite()
	site.optimizerBatteryMode = api.BatteryHold
	site.optimizerBatteryModeUpdated = time.Now()
	assert.Equal(t, api.BatteryUnknown, site.requiredBatteryMode(false, api.Rate{}), "disabled control ignored")

	// external mode wins over optimizer mode
	site = controlSite()
	site.optimizerBatteryControl = true
	site.optimizerBatteryMode = api.BatteryCharge
	site.optimizerBatteryModeUpdated = time.Now()
	site.batteryModeExternal = api.BatteryHold
	assert.Equal(t, api.BatteryHold, site.requiredBatteryMode(false, api.Rate{}), "external mode wins")
}

func TestRequiredBatteryModeChargePriceGuard(t *testing.T) {
	// a charge decided at 0.10 must not keep running once the live rate has
	// stepped to 0.35 — runs are not slot-aligned, so a price step can pass
	// mid-decision
	site := controlSite()
	site.optimizerBatteryControl = true
	site.optimizerBatteryMode = api.BatteryCharge
	site.optimizerBatteryModeUpdated = time.Now()
	site.optimizerChargePrice = 0.10

	assert.Equal(t, api.BatteryCharge, site.requiredBatteryMode(false, api.Rate{Value: 0.10}), "decision price honored")
	assert.Equal(t, api.BatteryCharge, site.requiredBatteryMode(false, api.Rate{Value: 0.1005}), "tolerance honored")
	assert.Equal(t, api.BatteryUnknown, site.requiredBatteryMode(false, api.Rate{Value: 0.35}), "price step drops charge")

	// the grid charge limit also binds the running decision
	limit := 0.22
	site.batteryGridChargeLimit = &limit
	site.optimizerChargePrice = 0.30
	assert.Equal(t, api.BatteryUnknown, site.requiredBatteryMode(false, api.Rate{Value: 0.25}), "limit drops charge")

	// non-charge modes are not price-guarded
	site.optimizerBatteryMode = api.BatteryHold
	assert.Equal(t, api.BatteryHold, site.requiredBatteryMode(false, api.Rate{Value: 0.35}), "hold unaffected by price")
}

func TestRequiredBatteryModeChargeVeto(t *testing.T) {
	// when a fresh optimizer run declined to grid charge, the legacy price
	// threshold (batteryGridChargeActive) must not charge anyway — otherwise the
	// veto is INVERTED into the very charge it rejected
	site := controlSite()
	site.optimizerBatteryControl = true
	site.optimizerChargeVetoed = true
	site.optimizerBatteryModeUpdated = time.Now()

	assert.Equal(t, api.BatteryUnknown, site.requiredBatteryMode(true, api.Rate{Value: 0.20}), "fresh veto suppresses threshold fallback")

	// without a veto the legacy fallback keeps working
	site.optimizerChargeVetoed = false
	assert.Equal(t, api.BatteryCharge, site.requiredBatteryMode(true, api.Rate{Value: 0.20}), "no veto keeps fallback")

	// a stale veto no longer suppresses — the optimizer is gone, legacy takes over
	site.optimizerChargeVetoed = true
	site.optimizerBatteryModeUpdated = time.Now().Add(-optimizerBatteryModeValidity - time.Second)
	assert.Equal(t, api.BatteryCharge, site.requiredBatteryMode(true, api.Rate{Value: 0.20}), "stale veto releases fallback")

	// veto requires control to be enabled
	site.optimizerBatteryControl = false
	site.optimizerBatteryModeUpdated = time.Now()
	assert.Equal(t, api.BatteryCharge, site.requiredBatteryMode(true, api.Rate{Value: 0.20}), "disabled control ignores veto")
}

// backdatePending ages a pending candidate enough for confirmation
func backdatePending(site *Site) {
	site.Lock()
	if site.optimizerBatteryModePending != api.BatteryUnknown {
		site.optimizerBatteryModePendingSince = time.Now().Add(-optimizerBatteryModeConfirmDelay - time.Minute)
	}
	site.Unlock()
}

func TestOptimizerBatteryModeDamping(t *testing.T) {
	newSite := func() *Site {
		site := controlSite()
		site.optimizerBatteryControl = true
		return site
	}

	// an escalation needs two runs far enough apart
	site := newSite()
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge, price: 0.2})
	assert.Equal(t, api.BatteryUnknown, site.optimizerBatteryMode, "first observation not applied")
	backdatePending(site)
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge, price: 0.2})
	assert.Equal(t, api.BatteryCharge, site.optimizerBatteryMode, "second observation applies")
	assert.Equal(t, 0.2, site.optimizerChargePrice, "decision price recorded")

	// reverting is never delayed
	site.setOptimizerBatteryMode(optimizerDecision{})
	assert.Equal(t, api.BatteryUnknown, site.optimizerBatteryMode, "error/veto reverts immediately")

	// a forced re-run seconds after the first observation sees the same data and
	// is not independent evidence
	site = newSite()
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge})
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge})
	assert.Equal(t, api.BatteryUnknown, site.optimizerBatteryMode, "immediate re-run does not confirm")

	// pending goes stale between runs: treated as a fresh observation again
	site = newSite()
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge})
	site.Lock()
	site.optimizerBatteryModePendingSince = time.Now().Add(-optimizerBatteryModeValidity - time.Minute)
	site.Unlock()
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge})
	assert.Equal(t, api.BatteryUnknown, site.optimizerBatteryMode, "stale candidate does not confirm")

	// an applied active mode that runs keep disagreeing with is de-escalated
	// instead of frozen — a frozen charge would keep buying on an old observation
	site = newSite()
	site.optimizerBatteryMode = api.BatteryCharge
	site.optimizerBatteryModeUpdated = time.Now()
	site.optimizerBatteryModeConfirmedAt = time.Now().Add(-optimizerBatteryModeDisagreementLimit - time.Minute)
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryHold})
	assert.Equal(t, api.BatteryNormal, site.optimizerBatteryMode, "persistent disagreement de-escalates")
}

func TestOptimizerBatteryModeFlapping(t *testing.T) {
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

	site := controlSite()
	site.optimizerBatteryControl = true

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
	site = controlSite()
	site.optimizerBatteryControl = true
	site.optimizerBatteryMode = api.BatteryHold
	site.optimizerBatteryModeUpdated = time.Now()
	site.optimizerBatteryModeConfirmedAt = time.Now()

	for _, m := range []api.BatteryMode{api.BatteryNormal, api.BatteryHold, api.BatteryNormal, api.BatteryHold} {
		site.setOptimizerBatteryMode(optimizerDecision{mode: m})
		backdatePending(site)
		assert.Equal(t, api.BatteryHold, site.optimizerBatteryMode, "applied idle mode stays put during flap")
	}

	// a genuine regime change still lands, one slot later
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryNormal})
	backdatePending(site)
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryNormal})
	assert.Equal(t, api.BatteryNormal, site.optimizerBatteryMode, "two agreeing runs change the mode")
}

func TestOptimizerBatteryModePendingCleared(t *testing.T) {
	// toggling control must clear pending state in both directions — otherwise a
	// pre-toggle observation confirms a mode right after re-enabling
	site := controlSite()
	site.optimizerBatteryControl = true
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge})
	backdatePending(site)

	assert.NoError(t, site.SetOptimizerBatteryControl(false))
	assert.NoError(t, site.SetOptimizerBatteryControl(true))

	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge})
	assert.Equal(t, api.BatteryUnknown, site.optimizerBatteryMode, "pre-toggle observation must not confirm")

	// external takeover clears pending as well
	site = controlSite()
	site.optimizerBatteryControl = true
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge})
	backdatePending(site)

	assert.NoError(t, site.SetBatteryModeExternal(api.BatteryHold))

	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge})
	assert.Equal(t, api.BatteryUnknown, site.optimizerBatteryMode, "pre-takeover observation must not confirm")

	// disabled control stores nothing, including the veto
	site = controlSite()
	site.setOptimizerBatteryMode(optimizerDecision{mode: api.BatteryCharge, chargeVetoed: true})
	assert.Equal(t, api.BatteryUnknown, site.optimizerBatteryMode, "disabled control stores no mode")
	assert.False(t, site.optimizerChargeVetoed, "disabled control stores no veto")
}
