package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
)

func TestFitHeatingDegree(t *testing.T) {
	t.Run("too few samples: no fit", func(t *testing.T) {
		samples := make([]metrics.TemperatureSample, heatingDegreeMinSamples-1)
		for i := range samples {
			samples[i] = metrics.TemperatureSample{Energy: 500, Temperature: float64(i % 10)}
		}
		_, ok := fitHeatingDegree(samples)
		assert.False(t, ok)
	})

	t.Run("clear linear relationship: fit adopted with the right slope and sign", func(t *testing.T) {
		samples := make([]metrics.TemperatureSample, heatingDegreeMinSamples)
		const trueSlope = 25.0 // Wh per heating-degree
		for i := range samples {
			temp := -10.0 + float64(i%40) // -10..29°C
			hdd := heatingDegree(temp)
			samples[i] = metrics.TemperatureSample{Energy: 400 + trueSlope*hdd, Temperature: temp}
		}

		fit, ok := fitHeatingDegree(samples)
		assert.True(t, ok)
		assert.InDelta(t, trueSlope, fit.slope, 0.01, "colder must mean more energy: positive slope on heating-degree")
	})

	t.Run("load unrelated to temperature: no fit even with plenty of samples", func(t *testing.T) {
		samples := make([]metrics.TemperatureSample, heatingDegreeMinSamples)
		for i := range samples {
			temp := -10.0 + float64(i%40) // varies
			energy := 500.0
			if i%2 == 0 {
				energy = 1000.0 // varies too, but independently of temp's ordering
			}
			samples[i] = metrics.TemperatureSample{Energy: energy, Temperature: temp}
		}

		_, ok := fitHeatingDegree(samples)
		assert.False(t, ok, "no heat pump / no temperature-sensitive load must not pass the fit gate")
	})

	t.Run("no temperature variance in the window: no fit", func(t *testing.T) {
		samples := make([]metrics.TemperatureSample, heatingDegreeMinSamples)
		for i := range samples {
			samples[i] = metrics.TemperatureSample{Energy: float64(400 + i%50), Temperature: 5.0}
		}
		_, ok := fitHeatingDegree(samples)
		assert.False(t, ok)
	})
}

func TestHeatingDegreeFitAdjust(t *testing.T) {
	fit := heatingDegreeFit{slope: 10, meanHDD: 5}

	// exactly average conditions: no correction
	assert.Equal(t, 0.0, fit.adjust(10)) // hdd(10) = 5 = meanHDD
	// colder than average: positive correction
	assert.InDelta(t, 150.0, fit.adjust(-5), 1e-9) // hdd(-5) = 20, (20-5)*10
	// warmer than average: negative correction
	assert.InDelta(t, -50.0, fit.adjust(20), 1e-9) // hdd(20) = 0, (0-5)*10
}

func TestApplyHeatingDegree(t *testing.T) {
	site := &Site{log: util.NewLogger("foo")}
	profile := []float64{1.0, 1.0, 1.0}

	t.Run("no temperature tariff configured: profile unchanged", func(t *testing.T) {
		site.tariffs = &tariff.Tariffs{}
		got := site.applyHeatingDegree(profile)
		assert.Equal(t, profile, got)
	})

	ctrl := gomock.NewController(t)
	temp := api.NewMockTariff(ctrl)
	temp.EXPECT().Type().Return(api.TariffTypeTemperature).AnyTimes()

	t.Run("fit not confident: profile unchanged, Rates() not even consulted", func(t *testing.T) {
		site.tariffs = &tariff.Tariffs{Temperature: temp}
		site.heatingDegreeCached = func() (heatingDegreeResult, error) { return heatingDegreeResult{}, nil }

		got := site.applyHeatingDegree(profile)
		assert.Equal(t, profile, got)
	})

	t.Run("query error: profile unchanged", func(t *testing.T) {
		site.tariffs = &tariff.Tariffs{Temperature: temp}
		site.heatingDegreeCached = func() (heatingDegreeResult, error) { return heatingDegreeResult{}, assert.AnError }

		got := site.applyHeatingDegree(profile)
		assert.Equal(t, profile, got)
	})

	t.Run("confident fit and a forecast: each slot corrected by its own forecast temperature, capped at the base value", func(t *testing.T) {
		fit := heatingDegreeFit{slope: 500, meanHDD: 5} // 500 Wh per heating-degree
		site.tariffs = &tariff.Tariffs{Temperature: temp}
		site.heatingDegreeCached = func() (heatingDegreeResult, error) {
			return heatingDegreeResult{fit: fit, ok: true}, nil
		}

		start := time.Now().Truncate(tariff.SlotDuration)
		temp.EXPECT().Rates().Return(api.Rates{
			{Start: start, End: start.Add(tariff.SlotDuration), Value: 5},                                  // hdd=10, +5*500=2500Wh -> +2.5kWh raw, capped to +1.0kWh (base)
			{Start: start.Add(tariff.SlotDuration), End: start.Add(2 * tariff.SlotDuration), Value: 15},    // hdd=0, -5*500=-2500Wh -> -2.5kWh raw, capped to -1.0kWh
			{Start: start.Add(2 * tariff.SlotDuration), End: start.Add(3 * tariff.SlotDuration), Value: 0}, // hdd=15, +10*500=5000Wh -> +5kWh raw, capped to +1.0kWh
		}, nil)

		// each slot has a 1.0kWh base: an uncapped correction would put these at 3.5/-1.5/6.0 -
		// #27's cap limits the swing to +/-100% of the slot's own base value
		got := site.applyHeatingDegree([]float64{1.0, 1.0, 1.0})
		assert.InDelta(t, 2.0, got[0], 1e-9, "raw +2.5kWh capped to +1.0kWh (the base)")
		assert.Equal(t, 0.0, got[1], "raw -2.5kWh capped to -1.0kWh, base+correction clamped at zero")
		assert.InDelta(t, 2.0, got[2], 1e-9, "raw +5.0kWh capped to +1.0kWh (the base)")
	})

	t.Run("correction within the cap is applied uncapped", func(t *testing.T) {
		fit := heatingDegreeFit{slope: 500, meanHDD: 5} // 500 Wh per heating-degree
		site.tariffs = &tariff.Tariffs{Temperature: temp}
		site.heatingDegreeCached = func() (heatingDegreeResult, error) {
			return heatingDegreeResult{fit: fit, ok: true}, nil
		}

		start := time.Now().Truncate(tariff.SlotDuration)
		temp.EXPECT().Rates().Return(api.Rates{
			// hdd=6, (6-5)*500=500Wh -> +0.5kWh, well inside +/-2.0kWh (the base) - not capped
			{Start: start, End: start.Add(tariff.SlotDuration), Value: 9},
		}, nil)

		got := site.applyHeatingDegree([]float64{2.0})
		assert.InDelta(t, 2.5, got[0], 1e-9)
	})
}
