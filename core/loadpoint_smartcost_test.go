package core

import (
	"testing"

	"github.com/benbjohnson/clock"
	coresettings "github.com/evcc-io/evcc/core/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testSmartCostLoadpoint(t *testing.T) *Loadpoint {
	t.Helper()
	return &Loadpoint{
		log:      util.NewLogger("foo"),
		clock:    clock.NewMock(),
		settings: coresettings.NewDatabaseSettingsAdapter(t.Name()),
	}
}

// TestSmartCostLimitPercentileExclusive guards the "same setting, two forms"
// contract: an absolute limit and a percentile limit are never both active at
// once, whichever was written last wins and silently clears the other. A
// config round-trip that only echoes back the still-nil field must not wipe
// out the one that is actually set (mirrors the optimizer round-trip guard
// for the absolute limit).
func TestSmartCostLimitPercentileExclusive(t *testing.T) {
	lp := testSmartCostLoadpoint(t)

	abs := 0.2
	require.NoError(t, lp.SetSmartCostLimit(&abs))
	assert.Equal(t, &abs, lp.GetSmartCostLimit())
	assert.Nil(t, lp.GetSmartCostLimitPercentile())

	pct := 30.0
	require.NoError(t, lp.SetSmartCostLimitPercentile(&pct))
	assert.Equal(t, &pct, lp.GetSmartCostLimitPercentile())
	assert.Nil(t, lp.GetSmartCostLimit(), "setting a percentile clears the absolute limit")

	// writing nil (the unset field of a config round-trip) must not disturb
	// the limit that is actually configured
	require.NoError(t, lp.SetSmartCostLimit(nil))
	assert.Equal(t, &pct, lp.GetSmartCostLimitPercentile())

	require.NoError(t, lp.SetSmartCostLimit(&abs))
	assert.Equal(t, &abs, lp.GetSmartCostLimit())
	assert.Nil(t, lp.GetSmartCostLimitPercentile(), "setting an absolute limit clears the percentile")
}

func TestSmartCostLimitPercentileRange(t *testing.T) {
	lp := testSmartCostLoadpoint(t)

	for _, v := range []float64{0, -1, 100.1, 500} {
		assert.Error(t, lp.SetSmartCostLimitPercentile(&v), "percentile %v out of (0,100] should be rejected", v)
	}

	ok := 100.0
	assert.NoError(t, lp.SetSmartCostLimitPercentile(&ok))
}

// TestCheckSmartLimitPercentile is the control-loop counterpart to
// TestPercentileLimit: a percentile-configured loadpoint must actually gate
// charging based on the live forward rate window, not just resolve a number.
func TestCheckSmartLimitPercentile(t *testing.T) {
	lp := testSmartCostLoadpoint(t)

	pct := 25.0
	require.NoError(t, lp.SetSmartCostLimitPercentile(&pct))

	// ratesAt starts the first slot at time.Now(), so it covers the current instant
	rr := ratesAt(0.10, 0.20, 0.30, 0.40, 0.50)

	active, _ := lp.checkSmartLimit(resolveSmartCostLimit(lp, rr), rr, true)
	assert.True(t, active, "cheapest slot must be below a P25 threshold")
}
