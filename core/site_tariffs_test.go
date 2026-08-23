package core

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForecastSlotEnergy(t *testing.T) {
	slot := time.Unix(1735689600, 0).Truncate(tariff.SlotDuration)
	rate := func(i int, power float64) api.Rate {
		return api.Rate{
			Start: slot.Add(time.Duration(i) * tariff.SlotDuration),
			End:   slot.Add(time.Duration(i+1) * tariff.SlotDuration),
			Value: power,
		}
	}

	rr := api.Rates{rate(0, 4000), rate(1, 8000)}

	// trapezoidal like the published curve, (4kW + 8kW) / 2 * 15min,
	// and identical anywhere inside the slot
	assert.Equal(t, 1.5, forecastSlotEnergy(rr, slot))
	assert.Equal(t, 1.5, forecastSlotEnergy(rr, slot.Add(time.Minute)))

	// the last sample has no successor to integrate towards, as for the daily totals
	assert.Equal(t, 0.0, forecastSlotEnergy(rr, slot.Add(tariff.SlotDuration)))

	// beyond the forecast horizon
	assert.Equal(t, 0.0, forecastSlotEnergy(rr, slot.Add(2*tariff.SlotDuration)))
}

func TestForecastRates(t *testing.T) {
	start := time.Unix(1735689600, 0)

	for _, tc := range []struct {
		desc  string
		rates api.Rates
		want  string
	}{
		{desc: "nil", rates: nil, want: "null"},
		{desc: "empty", rates: api.Rates{}, want: "null"},
		{
			desc: "slots",
			rates: api.Rates{
				{Start: start, End: start.Add(time.Hour), Value: 0.25},
				{Start: start.Add(time.Hour), End: start.Add(2 * time.Hour), Value: -0.1},
			},
			want: "[[1735689600,1735693200,0.25],[1735693200,1735696800,-0.1]]",
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			b, err := json.Marshal(forecastRates(tc.rates))
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(b))
		})
	}
}

func TestTimeseriesMarshal(t *testing.T) {
	start := time.Unix(1735689600, 0)

	for _, tc := range []struct {
		desc string
		ts   timeseries
		want string
	}{
		{desc: "nil", ts: nil, want: "null"},
		{desc: "empty", ts: timeseries{}, want: "[]"},
		{
			desc: "entries",
			ts: timeseries{
				{Timestamp: start, Value: 1000},
				{Timestamp: start.Add(time.Hour), Value: 0},
			},
			want: "[[1735689600,1000],[1735693200,0]]",
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			b, err := json.Marshal(tc.ts)
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(b))

			b, err = tc.ts.MarshalBytes()
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(b))
		})
	}
}

func TestPercentileOf(t *testing.T) {
	// n values of v
	fill := func(n int, v float64) []float64 {
		s := make([]float64, n)
		for i := range s {
			s[i] = v
		}
		return s
	}

	t.Run("too few samples returns false", func(t *testing.T) {
		_, ok := percentileOf(nil, 0.5, solarScaleMinSamples)
		assert.False(t, ok)

		_, ok = percentileOf(fill(solarScaleMinSamples-1, 0.9), 0.5, solarScaleMinSamples)
		assert.False(t, ok)
	})

	t.Run("stable cluster", func(t *testing.T) {
		v, ok := percentileOf(fill(20, 0.9), 0.5, solarScaleMinSamples)
		assert.True(t, ok)
		assert.InDelta(t, 0.9, v, 0.001)
	})

	// P50 rejects outlier days for free: a broken forecast feed (recent ratio
	// ~2.3) and a metering outage (ratio ~0.16) do not move the result as
	// long as they stay a minority of the window.
	t.Run("outlier days do not move P50", func(t *testing.T) {
		ratios := fill(20, 0.9)                   // healthy installation bias
		ratios = append(ratios, fill(4, 2.3)...)  // broken forecast feed
		ratios = append(ratios, fill(8, 0.16)...) // metering outage

		v, ok := percentileOf(ratios, 0.5, solarScaleMinSamples)
		assert.True(t, ok)
		assert.InDelta(t, 0.9, v, 0.001)
	})

	t.Run("higher percentile shifts toward the upper tail", func(t *testing.T) {
		ratios := append(fill(15, 0.8), fill(15, 1.2)...)

		p50, _ := percentileOf(ratios, 0.5, solarScaleMinSamples)
		p90, _ := percentileOf(ratios, 0.9, solarScaleMinSamples)
		assert.Less(t, p50, p90)
	})
}

// TestSolarScaleAt covers the interpolation solarScaleAt builds over
// querySolarScaleByLead's per-lead-time buckets: the case this exists for
// (§29) is a slot far in the future getting corrected with far-future bias
// instead of the near-zero-lead nowcast every slot used to share.
func TestSolarScaleAt(t *testing.T) {
	site := &Site{log: util.NewLogger("foo")}

	t.Run("no lead-time history falls back to flat nowcast, unchanged from before this existed", func(t *testing.T) {
		site.solarScaleByLeadCached = func() (map[int]float64, error) { return nil, nil }

		scaleAt := site.solarScaleAt(0.9)
		assert.Equal(t, 0.9, scaleAt(0))
		assert.Equal(t, 0.9, scaleAt(6*time.Hour))
		assert.Equal(t, 0.9, scaleAt(48*time.Hour))
	})

	t.Run("interpolates between nowcast and archived lead buckets", func(t *testing.T) {
		site.solarScaleByLeadCached = func() (map[int]float64, error) {
			return map[int]float64{
				int(time.Hour.Minutes()):      0.8,
				int(24 * time.Hour.Minutes()): 0.6,
			}, nil
		}

		scaleAt := site.solarScaleAt(1.0)
		assert.Equal(t, 1.0, scaleAt(0), "the slot starting now uses the nowcast anchor")
		assert.InDelta(t, 0.9, scaleAt(30*time.Minute), 0.001, "halfway between the nowcast and 1h anchors")
		assert.InDelta(t, 0.8, scaleAt(time.Hour), 0.001)
		assert.InDelta(t, 0.6, scaleAt(24*time.Hour), 0.001)
		assert.InDelta(t, 0.6, scaleAt(48*time.Hour), 0.001, "beyond the last anchor clamps to it rather than extrapolating")
	})

	t.Run("a missing bucket is skipped, not treated as zero", func(t *testing.T) {
		// only the 24h bucket has enough history; 1h and 6h are absent
		site.solarScaleByLeadCached = func() (map[int]float64, error) {
			return map[int]float64{int(24 * time.Hour.Minutes()): 0.5}, nil
		}

		scaleAt := site.solarScaleAt(1.0)
		assert.InDelta(t, 0.75, scaleAt(12*time.Hour), 0.001, "interpolates straight from lead=0 to the 24h anchor")
	})

	t.Run("a query error falls back to the flat nowcast instead of failing the run", func(t *testing.T) {
		site.solarScaleByLeadCached = func() (map[int]float64, error) { return nil, errors.New("boom") }

		scaleAt := site.solarScaleAt(0.7)
		assert.Equal(t, 0.7, scaleAt(12*time.Hour))
	})
}

func TestScaleAndPruneByLead(t *testing.T) {
	now := time.Now()
	rr := api.Rates{
		{Start: now, Value: 10},
		{Start: now.Add(time.Hour), Value: 10},
		{Start: now.Add(2 * time.Hour), Value: 10},
	}

	// scale halves per hour of lead, so each successive slot gets a smaller share
	scaleAt := func(lead time.Duration) float64 { return 1 - lead.Seconds()/(2*time.Hour).Seconds()*0.5 }

	got := scaleAndPruneByLead(rr, now, scaleAt, 3)
	require.Len(t, got, 3)
	assert.InDelta(t, 10, got[0], 0.001)
	assert.InDelta(t, 7.5, got[1], 0.001)
	assert.InDelta(t, 5, got[2], 0.001)
}
