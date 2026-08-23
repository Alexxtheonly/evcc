package soc

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
)

func TestRemainingChargeDuration(t *testing.T) {
	ctrl := gomock.NewController(t)
	vehicle := api.NewMockVehicle(ctrl)
	// 8.5 kWh userBatCap => 10 kWh virtualBatCap (at 85% efficiency)
	vehicle.EXPECT().Capacity().Return(float64(8.5))

	ce := NewEstimator(util.NewLogger("foo"), vehicle)
	ce.vehicleSoc = 20.0

	chargePower := 1000.0
	targetSoc := 80.0

	if remaining := ce.RemainingChargeDuration(targetSoc, chargePower); remaining != 6*time.Hour {
		t.Errorf("wrong remaining charge duration: %v", remaining)
	}
}

func TestSocEstimation(t *testing.T) {
	ctrl := gomock.NewController(t)
	vehicle := api.NewMockVehicle(ctrl)

	// 8.5 kWh user battery capacity is converted to initial value of 10 kWh virtual capacity (at 85% efficiency)
	vehicle.EXPECT().Capacity().Return(8.5).AnyTimes()

	ce := NewEstimator(util.NewLogger("foo"), vehicle)

	tc := []struct {
		chargedEnergy   float64
		vehicleSoc      float64
		estimatedSoc    float64
		virtualCapacity float64
	}{
		{0, 0.0, 0.0, 10000},
		{0, 20.0, 20.0, 10000},
		{123, 20.0, 21.23, 10000},
		{1000, 20.0, 30.0, 10000},
		{1100, 31.0, 31.0, 10000},
		{1200, 32.0, 32.0, 10000},
		{1900, 39.0, 39.0, 10000},
		{2000, 40.0, 40.0, 10000},
		{6000, 80.0, 80.0, 10000},
		{0, 25.0, 25.0, 10000},
		{2500, 25.0, 50.0, 10000},
		{0, 50.0, 50.0, 10000}, // -10000
		{4990, 50.0, 99.9, 10000},
		{5000, 50.0, 100.0, 10000},
		{5001, 50.0, 100.0, 10000},
		{0, 20.0, 20.0, 10000},
		{1000, 30.0, 30.0, 10000},
		{1000, 50.0, 50.0, 8500}, // cap virtual capacity minimum to physical capacity
	}

	for _, tc := range tc {
		t.Logf("%+v", tc)

		soc := ce.Soc(&tc.vehicleSoc, tc.chargedEnergy)

		// validate soc/capacity estimate
		assert.Equal(t, tc.estimatedSoc, soc, "estimated soc")
		assert.Equal(t, tc.virtualCapacity, ce.virtualCapacity(), "virtual capacity")

		// validate duration estimate
		chargePower := 1e3
		targetSoc := 100.0
		remainingHours := (float64(targetSoc) - soc) / 100 * tc.virtualCapacity / chargePower
		remainingDuration := time.Duration(float64(time.Hour) * remainingHours).Round(time.Second)

		assert.Equal(t, remainingDuration, ce.RemainingChargeDuration(targetSoc, chargePower), "remaining duration")
	}
}

func TestMissingSoc(t *testing.T) {
	ctrl := gomock.NewController(t)
	vehicle := api.NewMockVehicle(ctrl)
	vehicle.EXPECT().Capacity().Return(8.5)

	ce := NewEstimator(util.NewLogger("foo"), vehicle)

	soc := 20.0
	assert.Equal(t, 20.0, ce.Soc(&soc, 0))
	assert.Equal(t, 21.0, ce.Soc(&soc, 100))

	// missing soc keeps the estimate and must not corrupt the sampled state
	assert.Equal(t, 21.0, ce.Soc(nil, 200))
	assert.Equal(t, 22.0, ce.Soc(&soc, 200))
}

func TestImprovedEstimatorRemainingChargeDuration(t *testing.T) {
	ctrl := gomock.NewController(t)
	vehicle := api.NewMockVehicle(ctrl)

	// https://github.com/evcc-io/evcc/pull/7510#issuecomment-1512688548
	// Updated for 85% charge efficiency (previously 90%)
	tc := []struct {
		capacity    float64
		soc         float64
		targetsoc   float64
		chargePower float64
		duration    time.Duration
	}{
		{0.75, 10, 60, 300, 1*time.Hour + 28*time.Minute + 14*time.Second},
		{0.75, 50, 100, 300, 1*time.Hour + 28*time.Minute + 14*time.Second},
		{17, 10, 60, 7 * 1e3, 1*time.Hour + 25*time.Minute + 43*time.Second},
		{17, 50, 100, 7 * 1e3, 1*time.Hour + 33*time.Minute + 35*time.Second},
		{50, 10, 60, 11 * 1e3, 2*time.Hour + 40*time.Minute + 26*time.Second},
		{50, 50, 100, 11 * 1e3, 3*time.Hour + 7*time.Minute + 43*time.Second},
		{80, 10, 60, 22 * 1e3, 2*time.Hour + 8*time.Minute + 21*time.Second},
		{80, 50, 100, 22 * 1e3, 2*time.Hour + 58*time.Minute + 34*time.Second},
	}

	for _, tc := range tc {
		t.Log(tc)

		vehicle.EXPECT().Capacity().Return(tc.capacity)

		ce := NewEstimator(util.NewLogger("foo"), vehicle)
		ce.vehicleSoc = tc.soc

		assert.Equal(t, tc.duration, ce.RemainingChargeDuration(tc.targetsoc, tc.chargePower))
	}
}

// TestPlausibleEnergyPerSocStep tests the contract: step (Wh metered per soc%) is plausible
// only for an implied efficiency of capacity/(100*step) within [50%, 100%]. Efficiency can
// never exceed 100% (delivered energy can't be less than what the battery actually stores,
// so step can never go below capacity/100), and evcc treats anything below a 50%-efficiency
// session as more likely a bad reading than a real vehicle.
func TestPlausibleEnergyPerSocStep(t *testing.T) {
	const capacity = 50000.0               // 50 kWh
	const perStepAt100 = capacity / 100    // 500 Wh: the 100%-efficiency floor
	const perStepAt50 = perStepAt100 / 0.5 // 1000 Wh: the 50%-efficiency ceiling

	// implausible: non-positive step or capacity
	assert.False(t, PlausibleEnergyPerSocStep(0, capacity))
	assert.False(t, PlausibleEnergyPerSocStep(-100, capacity))
	assert.False(t, PlausibleEnergyPerSocStep(500, 0))

	// plausible: efficiency from 100% (the floor, perStepAt100) down to 50% (the ceiling,
	// perStepAt50)
	assert.True(t, PlausibleEnergyPerSocStep(perStepAt100, capacity)) // 100% efficiency
	assert.True(t, PlausibleEnergyPerSocStep(600, capacity))          // ~83% efficiency
	assert.True(t, PlausibleEnergyPerSocStep(perStepAt50, capacity))  // 50% efficiency, at the ceiling

	// implausible: below the 100%-efficiency floor - a real regression case (50 kWh vehicle,
	// energyPerSocStep=300 implies 167% efficiency) that a buggy bound of
	// perStepAt100*minPlausibleEfficiency (250, not perStepAt100 itself) used to accept
	assert.False(t, PlausibleEnergyPerSocStep(300, capacity))
	assert.False(t, PlausibleEnergyPerSocStep(perStepAt100-1, capacity)) // just over 100% efficiency

	// implausible: above the 50%-efficiency ceiling
	assert.False(t, PlausibleEnergyPerSocStep(perStepAt50+1, capacity)) // just under 50% efficiency
	assert.False(t, PlausibleEnergyPerSocStep(5000, capacity))          // wildly implausible
}

func TestBlendEnergyPerSocStep(t *testing.T) {
	// a single session moves the estimate by at most 30%, never fully overwrites it
	assert.InDelta(t, 100.0, BlendEnergyPerSocStep(100, 100), 1e-9)
	assert.InDelta(t, 103.0, BlendEnergyPerSocStep(100, 110), 1e-9)
	assert.InDelta(t, 70.0, BlendEnergyPerSocStep(100, 0), 1e-9)
}

func TestEstimatorSeed(t *testing.T) {
	ctrl := gomock.NewController(t)
	vehicle := api.NewMockVehicle(ctrl)
	vehicle.EXPECT().Capacity().Return(50.0).AnyTimes() // 50 kWh -> 50000 Wh

	ce := NewEstimator(util.NewLogger("foo"), vehicle)
	def := ce.EnergyPerSocStep()
	assert.False(t, ce.Learned())

	// implausible seed is ignored, default is kept
	ce.Seed(1)
	assert.Equal(t, def, ce.EnergyPerSocStep())

	// plausible seed is adopted
	ce.Seed(600)
	assert.Equal(t, 600.0, ce.EnergyPerSocStep())

	// seeding is not the same as learning from a live session
	assert.False(t, ce.Learned())
}

func TestEstimatorLearned(t *testing.T) {
	ctrl := gomock.NewController(t)
	vehicle := api.NewMockVehicle(ctrl)
	vehicle.EXPECT().Capacity().Return(10.0).AnyTimes() // 10 kWh

	ce := NewEstimator(util.NewLogger("foo"), vehicle)
	assert.False(t, ce.Learned())

	soc := 20.0
	ce.Soc(&soc, 0)
	assert.False(t, ce.Learned(), "socDiff not yet above the recalculation threshold")

	soc = 35.0 // socDiff 15 > 10
	ce.Soc(&soc, 1500)
	assert.True(t, ce.Learned())
	assert.InDelta(t, 1500.0/15, ce.EnergyPerSocStep(), 1e-9)
}

func TestPlausibleChargeTaper(t *testing.T) {
	tc := []struct {
		minPower, maxPower float64
		want               bool
	}{
		{1000, 11000, true},   // typical single-phase home wallbox taper
		{11000, 1000, false},  // plateau below tail: physically backwards
		{1000, 1000, false},   // no taper at all
		{0, 11000, false},     // tail at or below the implausibility floor
		{1000, 200000, false}, // plateau above the generous EVSE ceiling
		{-500, 11000, false},  // negative reading, e.g. a sign error
	}

	for _, c := range tc {
		assert.Equal(t, c.want, PlausibleChargeTaper(c.minPower, c.maxPower), "min=%.0f max=%.0f", c.minPower, c.maxPower)
	}
}

// TestEstimatorSeedChargeTaper mirrors TestEstimatorSeed for the charge-power taper: an
// implausible pair is ignored (default kept), a plausible one is adopted and actually changes
// the duration estimate, not just the stored fields.
func TestEstimatorSeedChargeTaper(t *testing.T) {
	ctrl := gomock.NewController(t)
	vehicle := api.NewMockVehicle(ctrl)
	vehicle.EXPECT().Capacity().Return(50.0).AnyTimes() // 50 kWh

	ce := NewEstimator(util.NewLogger("foo"), vehicle)
	ce.vehicleSoc = 10

	before := ce.RemainingChargeDuration(60, 11000)

	// implausible pair (plateau below tail) is ignored, default taper is kept
	ce.SeedChargeTaper(11000, 1000)
	assert.Equal(t, before, ce.RemainingChargeDuration(60, 11000))

	// plausible pair is adopted: a lower learned plateau (11kW vs the generic 50kW) means the
	// requested 11kW charge power now sits at the learned ceiling instead of far below a
	// theoretical one, changing the taper point and thus the estimate
	ce.SeedChargeTaper(1200, 11000)
	after := ce.RemainingChargeDuration(60, 11000)
	assert.NotEqual(t, before, after)
}
