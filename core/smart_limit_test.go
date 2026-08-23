package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
)

func ratesAt(values ...float64) api.Rates {
	rr := make(api.Rates, 0, len(values))
	start := time.Now()
	for i, v := range values {
		rr = append(rr, api.Rate{
			Start: start.Add(time.Duration(i) * time.Hour),
			End:   start.Add(time.Duration(i+1) * time.Hour),
			Value: v,
		})
	}
	return rr
}

func TestPercentileLimit(t *testing.T) {
	t.Run("too few samples returns nil", func(t *testing.T) {
		assert.Nil(t, percentileLimit(50, nil))
		assert.Nil(t, percentileLimit(50, ratesAt(0.1, 0.2, 0.3)))
	})

	t.Run("resolves the requested percentile of the window", func(t *testing.T) {
		rr := ratesAt(0.10, 0.20, 0.30, 0.40, 0.50)

		// P0 -> cheapest slot, P100 -> priciest slot
		v := percentileLimit(0, rr)
		assert.NotNil(t, v)
		assert.InDelta(t, 0.10, *v, 0.001)

		v = percentileLimit(100, rr)
		assert.NotNil(t, v)
		assert.InDelta(t, 0.50, *v, 0.001)
	})

	t.Run("flat week still resolves a threshold from the spread", func(t *testing.T) {
		// the motivating case: on a flat-price week an absolute limit either
		// never fires or always does, but a percentile still finds the split
		rr := ratesAt(0.19, 0.20, 0.20, 0.21, 0.22)
		v := percentileLimit(30, rr)
		assert.NotNil(t, v)
		assert.InDelta(t, 0.20, *v, 0.02)
	})

	t.Run("negative prices do not confuse the ranking", func(t *testing.T) {
		rr := ratesAt(-0.05, -0.02, 0.01, 0.10, 0.30)
		v := percentileLimit(25, rr)
		assert.NotNil(t, v)
		assert.InDelta(t, -0.02, *v, 0.001)
	})
}

func TestResolveSmartCostLimit(t *testing.T) {
	ctrl := gomock.NewController(t)
	rr := ratesAt(0.10, 0.20, 0.30, 0.40, 0.50)

	t.Run("percentile configured takes precedence and is resolved against the window", func(t *testing.T) {
		lp := loadpoint.NewMockAPI(ctrl)
		pct := 0.0
		lp.EXPECT().GetSmartCostLimitPercentile().Return(&pct)

		got := resolveSmartCostLimit(lp, rr)
		assert.NotNil(t, got)
		assert.InDelta(t, 0.10, *got, 0.001)
	})

	t.Run("no percentile falls back to the absolute limit unchanged", func(t *testing.T) {
		lp := loadpoint.NewMockAPI(ctrl)
		abs := 0.25
		lp.EXPECT().GetSmartCostLimitPercentile().Return(nil)
		lp.EXPECT().GetSmartCostLimit().Return(&abs)

		got := resolveSmartCostLimit(lp, rr)
		assert.Same(t, &abs, got)
	})

	t.Run("neither configured resolves to nil", func(t *testing.T) {
		lp := loadpoint.NewMockAPI(ctrl)
		lp.EXPECT().GetSmartCostLimitPercentile().Return(nil)
		lp.EXPECT().GetSmartCostLimit().Return(nil)

		assert.Nil(t, resolveSmartCostLimit(lp, rr))
	})
}
