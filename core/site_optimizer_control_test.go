package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/types"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
)

func TestGridChargeJustified(t *testing.T) {
	limit := 0.20

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
	} {
		assert.Equal(t, tc.expected, gridChargeJustified(tc.pn, tc.limit), tc.name)
	}
}

func TestOptimizerBatteryModeFromSuggestions(t *testing.T) {
	pn := []float32{0.0002, 0.0004} // cheap now, expensive later

	bat := func(name string) batteryDetail {
		return batteryDetail{Type: batteryTypeBattery, Name: name, controllable: true}
	}

	lpID := 0
	vehicle := batteryDetail{Type: batteryTypeVehicle, loadpoint: &lpID, controllable: true}

	for _, tc := range []struct {
		name        string
		suggestions map[string]types.Suggestion
		details     []batteryDetail
		expected    api.BatteryMode
	}{
		{"no batteries", nil, nil, api.BatteryUnknown},
		{"hold", map[string]types.Suggestion{"battery:a": {Action: "hold"}}, []batteryDetail{bat("a")}, api.BatteryHold},
		{"holdcharge", map[string]types.Suggestion{"battery:a": {Action: "holdcharge"}}, []batteryDetail{bat("a")}, api.BatteryHoldCharge},
		{"normal", map[string]types.Suggestion{"battery:a": {Action: "normal"}}, []batteryDetail{bat("a")}, api.BatteryNormal},
		{"discharge maps to normal", map[string]types.Suggestion{"battery:a": {Action: "discharge"}}, []batteryDetail{bat("a")}, api.BatteryNormal},
		{"charge with spread", map[string]types.Suggestion{"battery:a": {Action: "charge"}}, []batteryDetail{bat("a")}, api.BatteryCharge},
		{"agreement", map[string]types.Suggestion{"battery:a": {Action: "hold"}, "battery:b": {Action: "hold"}}, []batteryDetail{bat("a"), bat("b")}, api.BatteryHold},
		{"conflict", map[string]types.Suggestion{"battery:a": {Action: "hold"}, "battery:b": {Action: "normal"}}, []batteryDetail{bat("a"), bat("b")}, api.BatteryUnknown},
		{"vehicle ignored", map[string]types.Suggestion{"loadpoint:0": {Action: "charge"}}, []batteryDetail{vehicle}, api.BatteryUnknown},
	} {
		assert.Equal(t, tc.expected, optimizerBatteryModeFromSuggestions(tc.suggestions, tc.details, pn, nil), tc.name)
	}

	// charge is withheld when the spread cannot recover the round-trip losses
	flat := []float32{0.0002, 0.0002}
	assert.Equal(t, api.BatteryUnknown, optimizerBatteryModeFromSuggestions(
		map[string]types.Suggestion{"battery:a": {Action: "charge"}}, []batteryDetail{bat("a")}, flat, nil,
	), "charge without spread")
}

func TestRequiredBatteryModeOptimizer(t *testing.T) {
	var bat api.Meter = &struct{ api.Meter }{}

	newSite := func() *Site {
		return &Site{
			log:           util.NewLogger("foo"),
			batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{}, bat)},
		}
	}

	// fresh optimizer mode is followed
	site := newSite()
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
	site = newSite()
	site.optimizerBatteryMode = api.BatteryHold
	site.optimizerBatteryModeUpdated = time.Now()
	assert.Equal(t, api.BatteryUnknown, site.requiredBatteryMode(false, api.Rate{}), "disabled control ignored")

	// external mode wins over optimizer mode
	site = newSite()
	site.optimizerBatteryControl = true
	site.optimizerBatteryMode = api.BatteryCharge
	site.optimizerBatteryModeUpdated = time.Now()
	site.batteryModeExternal = api.BatteryHold
	assert.Equal(t, api.BatteryHold, site.requiredBatteryMode(false, api.Rate{}), "external mode wins")
}
