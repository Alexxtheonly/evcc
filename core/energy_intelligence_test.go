package core

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnergySettingsRejectInvalidEconomics(t *testing.T) {
	for _, value := range []float64{-0.01, math.NaN(), math.Inf(1)} {
		cfg := EnergyIntelligenceSettings{SettlementMode: "simulation", BatteryWear: map[string]float64{"db:11": value}}
		require.Error(t, cfg.validate())
	}
	cfg := EnergyIntelligenceSettings{SettlementMode: "simulation", BatteryWear: map[string]float64{"db:11": 0}}
	require.NoError(t, cfg.validate())
	cfg.SettlementMode = "flat-rate-guessed"
	require.Error(t, cfg.validate())
	cfg.SettlementMode = "simulation"
	invalid := 1.01
	cfg.BatteryEfficiency = map[string]EnergyEfficiencySetting{"db:11": {ChargeEfficiency: &invalid, DischargeEfficiency: &invalid}}
	require.Error(t, cfg.validate())
	cfg.BatteryEfficiency = nil
	cfg.BatteryEnergyPlane = map[string]string{"db:11": "dc"}
	require.NoError(t, cfg.validate())
	cfg.BatteryEnergyPlane["db:11"] = "probably-ac"
	require.Error(t, cfg.validate())
}

func TestMinimaxRegretKeepsCommonAction(t *testing.T) {
	// Charging wins on the expected day, but costs more than holding in a sunny
	// future. Each row is ONE action evaluated under all three futures.
	costs := [][]float64{{2, 5, 10}, {4, 6, 6}, {5, 8, 8}}
	assert.Equal(t, 1, minimaxRegret(costs))
	assert.Equal(t, -1, minimaxRegret(nil))
}
