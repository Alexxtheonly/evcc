package core

import (
	"testing"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func testBatteryControlSite(t *testing.T) *Site {
	t.Helper()

	ctrl := gomock.NewController(t)
	batCon := api.NewMockBatteryController(ctrl)

	var bat api.Meter = &struct {
		api.Meter
		api.BatteryController
	}{
		BatteryController: batCon,
	}

	return &Site{
		log:           util.NewLogger("foo"),
		batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{Name: "bat"}, bat)},
	}
}

// TestBatteryGridChargeLimitPercentileExclusive mirrors
// TestSmartCostLimitPercentileExclusive at the site level: the absolute and
// percentile grid charge limits are the same setting in different forms and
// writing one clears the other.
func TestBatteryGridChargeLimitPercentileExclusive(t *testing.T) {
	site := testBatteryControlSite(t)

	abs := 0.2
	require.NoError(t, site.SetBatteryGridChargeLimit(&abs))
	assert.Equal(t, &abs, site.GetBatteryGridChargeLimit())
	assert.Nil(t, site.GetBatteryGridChargeLimitPercentile())

	pct := 40.0
	require.NoError(t, site.SetBatteryGridChargeLimitPercentile(&pct))
	assert.Equal(t, &pct, site.GetBatteryGridChargeLimitPercentile())
	assert.Nil(t, site.GetBatteryGridChargeLimit(), "setting a percentile clears the absolute limit")

	require.NoError(t, site.SetBatteryGridChargeLimit(&abs))
	assert.Equal(t, &abs, site.GetBatteryGridChargeLimit())
	assert.Nil(t, site.GetBatteryGridChargeLimitPercentile(), "setting an absolute limit clears the percentile")
}

func TestBatteryGridChargeLimitPercentileRange(t *testing.T) {
	site := testBatteryControlSite(t)

	for _, v := range []float64{0, -1, 100.1} {
		assert.Error(t, site.SetBatteryGridChargeLimitPercentile(&v), "percentile %v out of (0,100] should be rejected", v)
	}
}

func TestResolveBatteryGridChargeLimit(t *testing.T) {
	site := testBatteryControlSite(t)

	rr := ratesAt(0.10, 0.20, 0.30, 0.40, 0.50)

	pct := 1.0
	require.NoError(t, site.SetBatteryGridChargeLimitPercentile(&pct))
	got := site.resolveBatteryGridChargeLimit(rr)
	assert.NotNil(t, got)
	assert.InDelta(t, 0.10, *got, 0.001)

	abs := 0.35
	require.NoError(t, site.SetBatteryGridChargeLimit(&abs))
	got = site.resolveBatteryGridChargeLimit(rr)
	assert.Equal(t, &abs, got)
}
