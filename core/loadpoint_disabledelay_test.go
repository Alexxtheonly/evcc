package core

import (
	"errors"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
)

func testDisableDelayLoadpoint(t *testing.T, tariff api.Tariff) *Loadpoint {
	t.Helper()
	lp := &Loadpoint{
		log: util.NewLogger("foo"),
		Disable: loadpoint.ThresholdConfig{
			Delay: 3 * time.Minute,
		},
	}
	lp.site = &mockSite{tariff: tariff}
	return lp
}

// TestEffectiveDisableDelayNoSite covers the loadpoint construction gap the existing
// TestPVHysteresis fixtures rely on: pvMaxCurrent must not panic before a site is attached.
func TestEffectiveDisableDelayNoSite(t *testing.T) {
	lp := &Loadpoint{
		log: util.NewLogger("foo"),
		Disable: loadpoint.ThresholdConfig{
			Delay: 3 * time.Minute,
		},
	}
	assert.Equal(t, 3*time.Minute, lp.effectiveDisableDelay())
}

func TestEffectiveDisableDelay(t *testing.T) {
	now := time.Now()

	t.Run("no tariff configured: unchanged", func(t *testing.T) {
		lp := testDisableDelayLoadpoint(t, nil)
		assert.Equal(t, 3*time.Minute, lp.effectiveDisableDelay())
	})

	t.Run("tariff errors: unchanged", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		tariff := api.NewMockTariff(ctrl)
		tariff.EXPECT().Rates().Return(nil, errors.New("boom"))

		lp := testDisableDelayLoadpoint(t, tariff)
		assert.Equal(t, 3*time.Minute, lp.effectiveDisableDelay())
	})

	t.Run("current slot already near zero: unchanged (dusk, not a cloud)", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		tariff := api.NewMockTariff(ctrl)
		tariff.EXPECT().Rates().Return(api.Rates{
			{Start: now.Add(-time.Minute), End: now.Add(14 * time.Minute), Value: 0},
		}, nil)

		lp := testDisableDelayLoadpoint(t, tariff)
		assert.Equal(t, 3*time.Minute, lp.effectiveDisableDelay())
	})

	t.Run("forecast declining: unchanged", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		tariff := api.NewMockTariff(ctrl)
		tariff.EXPECT().Rates().Return(api.Rates{
			{Start: now.Add(-time.Minute), End: now.Add(14 * time.Minute), Value: 2000},
			{Start: now.Add(29 * time.Minute), End: now.Add(44 * time.Minute), Value: 500}, // < 70% of 2000
		}, nil)

		lp := testDisableDelayLoadpoint(t, tariff)
		assert.Equal(t, 3*time.Minute, lp.effectiveDisableDelay())
	})

	t.Run("forecast recovering: extended, doubled since 3min is within the cap", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		tariff := api.NewMockTariff(ctrl)
		tariff.EXPECT().Rates().Return(api.Rates{
			{Start: now.Add(-time.Minute), End: now.Add(14 * time.Minute), Value: 2000},
			{Start: now.Add(29 * time.Minute), End: now.Add(44 * time.Minute), Value: 1600}, // 80% of 2000
		}, nil)

		lp := testDisableDelayLoadpoint(t, tariff)
		assert.Equal(t, 6*time.Minute, lp.effectiveDisableDelay())
	})

	t.Run("extension is capped regardless of the configured delay", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		tariff := api.NewMockTariff(ctrl)
		tariff.EXPECT().Rates().Return(api.Rates{
			{Start: now.Add(-time.Minute), End: now.Add(14 * time.Minute), Value: 2000},
			{Start: now.Add(29 * time.Minute), End: now.Add(44 * time.Minute), Value: 2000},
		}, nil)

		lp := testDisableDelayLoadpoint(t, tariff)
		lp.Disable.Delay = 25 * time.Minute

		assert.Equal(t, 35*time.Minute, lp.effectiveDisableDelay(), "extension capped at maxDisableDelayExtension, not doubled")
	})
}
