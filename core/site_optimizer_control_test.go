package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		site.setOptimizerBatteryMode(m, 0)
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
		site.setOptimizerBatteryMode(m, 0)
		backdatePending(site)
		assert.Equal(t, api.BatteryHold, site.optimizerBatteryMode, "applied idle mode stays put during flap")
	}

	// a genuine regime change still lands, once two runs agree
	site.setOptimizerBatteryMode(api.BatteryNormal, 0)
	backdatePending(site)
	site.setOptimizerBatteryMode(api.BatteryNormal, 0)
	assert.Equal(t, api.BatteryNormal, site.optimizerBatteryMode, "two agreeing runs change the mode")
}

// TestOptimizerBatteryModePendingCleared verifies that toggling automatic mode
// clears pending state in both directions — otherwise a pre-toggle observation
// could confirm a mode right after re-enabling.
func TestOptimizerBatteryModePendingCleared(t *testing.T) {
	enableAutomatic(t)

	site := &Site{log: util.NewLogger("foo")}
	site.setOptimizerBatteryMode(api.BatteryCharge, 0)
	assert.Equal(t, api.BatteryCharge, site.optimizerBatteryModePending)

	site.ResetOptimizerBatteryMode()
	assert.Equal(t, api.BatteryUnknown, site.optimizerBatteryModePending)
	assert.True(t, site.optimizerBatteryModePendingSince.IsZero())
	assert.Equal(t, api.BatteryUnknown, site.optimizerBatteryMode)

	// a stale pending candidate from before the reset does not immediately
	// confirm just because a fresh run happens to agree with it
	site.setOptimizerBatteryMode(api.BatteryCharge, 0)
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
	site.setOptimizerBatteryMode(api.BatteryCharge, 0.10)

	// confirm the mode so it is actually applied, not just pending
	backdatePending(site)
	site.setOptimizerBatteryMode(api.BatteryCharge, 0.10)
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
	site.setOptimizerBatteryMode(api.BatteryHold, 0)
	backdatePending(site)
	site.setOptimizerBatteryMode(api.BatteryHold, 0)
	mode, ok = site.batterySuggestionMode(api.Rate{Value: 999})
	assert.True(t, ok)
	assert.Equal(t, api.BatteryHold, mode)
}
