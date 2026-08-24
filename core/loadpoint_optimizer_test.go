package core

import (
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/core/types"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
)

// TestGateInertWithoutAutomatic pins the advisory-mode assumption that #ed613246e's site.go
// gate (added on top of the #30541 battery-boost carve-out) is a no-op while the optimizer is
// not automatic: optimizerControlled() checks lp.site.Automatic() first and short-circuits to
// false, so gate() returns nil regardless of a live, fresh suggestion - and surplusCharge(nil,
// ...) is false, so updatePower's priority-adjustment gate never opens. This must keep holding
// even though setBatteryForecast/applyOptimizerResult run unconditionally (no Automatic() gate
// of their own) whenever the optimizer is enabled and sponsored: the forecast is still
// computed in advisory mode, it just no longer reaches a control input - sitePower's
// batteryWillRefillToday relaxation is Automatic()-gated too now, and the surplus gate here
// stays closed regardless.
func TestGateInertWithoutAutomatic(t *testing.T) {
	c := clock.NewMock()
	c.Set(time.Now())

	lp := &Loadpoint{
		log:   util.NewLogger("lp"),
		clock: c,
		site:  &mockSite{automatic: false},
	}

	// a fresh, active surplus-charge suggestion - the exact shape that would open the gate
	// if the optimizer were automatic
	lp.setSuggestion(&types.Suggestion{Action: actionCharge, Charge: 5000, Grid: 0})

	assert.Nil(t, lp.gate(), "gate must stay nil while Automatic() is false, even with a fresh suggestion")
	assert.False(t, surplusCharge(lp.gate(), 11000), "surplusCharge(nil, ...) must be false, keeping updatePower's priority-adjustment gate closed")
}
