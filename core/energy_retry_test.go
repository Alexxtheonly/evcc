package core

import (
	"errors"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/types"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnergyInputChangesRetryWithoutCachingOrStaleAdvice(t *testing.T) {
	now := time.Now()
	planned := EnergyIntelligenceSettings{SettlementMode: "simulation"}
	current := planned
	current.Robust = true
	id := 0
	for name, err := range map[string]error{
		"settings":     validateEnergyPlanInputs(now.Add(time.Minute), now, planned, current),
		"expired":      validateEnergyPlanInputs(now, now, planned, planned),
		"device":       (&Site{}).validateEnergyDevices(requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeVehicle, loadpoint: &id}}}),
		"disconnected": (&Site{loadpoints: []*Loadpoint{{status: api.StatusA}}}).validateEnergyDevices(requestDetails{BatteryDetails: []batteryDetail{{Type: batteryTypeVehicle, loadpoint: &id}}}),
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, err)
			assert.ErrorIs(t, err, errOptimizerReplan)
			assert.ErrorIs(t, err, errOptimizerNotReady)
			params := make(chan util.Param, 64)
			site := &Site{log: util.NewLogger("retry-test"), valueChan: params, optimizerUpdated: now,
				suggestions: map[string]types.Suggestion{"battery:test": {Action: "charge"}}, optimizerHealthOk: true}
			site.finishOptimizerAttempt(err)
			assert.True(t, site.optimizerUpdated.IsZero(), "transient refusal must not close the advisory gate for 15 minutes")
			assert.Empty(t, site.suggestions, "superseded advice must not remain actionable")
			assert.False(t, site.optimizerHealthOk)
			found := false
			for len(params) > 0 {
				if p := <-params; p.Key == "optimizerInsights" {
					found = true
					assert.Equal(t, "unavailable", p.Val.(optimizerInsights).Status)
				}
			}
			assert.True(t, found, "waiting reason should be visible")
		})
	}
	assert.NoError(t, validateEnergyPlanInputs(now.Add(time.Minute), now, planned, planned))
	site := &Site{log: util.NewLogger("retry-test")}
	site.finishOptimizerAttempt(errors.New("persistent solver failure"))
	assert.False(t, site.optimizerUpdated.IsZero(), "persistent errors retain bounded advisory cadence")
	startup := &Site{optimizerHealthOk: true}
	startup.finishOptimizerAttempt(errOptimizerNotReady)
	assert.True(t, startup.optimizerUpdated.IsZero())
	assert.True(t, startup.optimizerHealthOk, "measurement startup keeps its existing silent-retry behavior")
}
