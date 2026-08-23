package tariff

import (
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type tariff struct {
	rates api.Rates
}

func (t *tariff) Rates() (api.Rates, error) {
	return t.rates, nil
}

func (t *tariff) Type() api.TariffType {
	return api.TariffTypeSolar
}

func TestCombined(t *testing.T) {
	clock := clock.NewMock()
	rate := func(start int, val float64) api.Rate {
		return api.Rate{
			Start: clock.Now().Add(time.Duration(start) * time.Hour),
			End:   clock.Now().Add(time.Duration(start+1) * time.Hour),
			Value: val,
		}
	}

	a := &tariff{api.Rates{rate(1, 1), rate(2, 2)}}
	b := &tariff{api.Rates{rate(2, 2), rate(3, 3)}}
	c := &combined{[]api.Tariff{a, b}}

	rr, err := c.Rates()
	require.NoError(t, err)
	assert.Equal(t, api.Rates{rate(1, 1), rate(2, 4), rate(3, 3)}, rr)
}

func TestCombinedUnsorted(t *testing.T) {
	clock := clock.NewMock()
	rate := func(start int, val float64) api.Rate {
		return api.Rate{
			Start: clock.Now().Add(time.Duration(start) * time.Hour),
			End:   clock.Now().Add(time.Duration(start+1) * time.Hour),
			Value: val,
		}
	}

	// b covers a disjoint range not adjacent to a's rates after concatenation
	a := &tariff{api.Rates{rate(1, 1), rate(2, 2)}}
	b := &tariff{api.Rates{rate(3, 3)}}
	c := &tariff{api.Rates{rate(1, 10), rate(2, 20)}}
	comb := &combined{[]api.Tariff{a, b, c}}

	rr, err := comb.Rates()
	require.NoError(t, err)
	assert.Equal(t, api.Rates{rate(1, 11), rate(2, 22), rate(3, 3)}, rr)
}

func TestCombinedBand(t *testing.T) {
	clock := clock.NewMock()
	low := func(v float64) *float64 { return &v }
	high := func(v float64) *float64 { return &v }

	rate := func(start int, val float64, l, h *float64) api.Rate {
		return api.Rate{
			Start: clock.Now().Add(time.Duration(start) * time.Hour),
			End:   clock.Now().Add(time.Duration(start+1) * time.Hour),
			Value: val,
			Low:   l,
			High:  h,
		}
	}

	// both planes carry a band for slot 1: combined into a summed band, not dropped
	a := &tariff{api.Rates{rate(1, 1000, low(600), high(1400))}}
	b := &tariff{api.Rates{rate(1, 500, low(300), high(700))}}

	// slot 2: only one plane has a band - the aggregate is unknown, not a partial sum
	c := &tariff{api.Rates{rate(2, 200, low(100), high(300))}}
	d := &tariff{api.Rates{rate(2, 800, nil, nil)}}

	comb := &combined{[]api.Tariff{a, b, c, d}}

	rr, err := comb.Rates()
	require.NoError(t, err)
	require.Len(t, rr, 2)

	assert.Equal(t, 1500.0, rr[0].Value)
	require.NotNil(t, rr[0].Low)
	require.NotNil(t, rr[0].High)
	assert.Equal(t, 900.0, *rr[0].Low, "band summed across both planes, not dropped")
	assert.Equal(t, 2100.0, *rr[0].High)

	assert.Equal(t, 1000.0, rr[1].Value)
	assert.Nil(t, rr[1].Low, "one plane missing its band: aggregate is unknown, not a partial sum")
	assert.Nil(t, rr[1].High)
}

func BenchmarkCombined(bench *testing.B) {
	clock := clock.NewMock()
	rate := func(start int, val float64) api.Rate {
		return api.Rate{
			Start: clock.Now().Add(time.Duration(start) * time.Hour),
			End:   clock.Now().Add(time.Duration(start+1) * time.Hour),
			Value: val,
		}
	}

	a := &tariff{api.Rates{rate(1, 1), rate(2, 2)}}
	b := &tariff{api.Rates{rate(2, 2), rate(3, 3)}}
	c := &combined{[]api.Tariff{a, b}}

	for bench.Loop() {
		c.Rates()
	}
}
