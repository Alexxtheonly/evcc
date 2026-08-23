package tariff

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/tariff/solcast"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSolcastRatesPreservesResolution ensures half-hourly forecasts are kept at their
// native resolution instead of being averaged into hourly buckets, which previously
// discarded the intra-hour ramp Solcast provides.
func TestSolcastRatesPreservesResolution(t *testing.T) {
	loc := time.UTC

	forecasts := []solcast.Forecast{
		{PvEstimate: 1.0, PeriodEnd: time.Date(2026, 8, 23, 10, 30, 0, 0, loc), Period: solcast.Duration(30 * time.Minute)},
		{PvEstimate: 2.0, PeriodEnd: time.Date(2026, 8, 23, 11, 0, 0, 0, loc), Period: solcast.Duration(30 * time.Minute)},
	}

	rates := solcastRates(forecasts, util.NewLogger("test"))
	require.Len(t, rates, 2)

	require.Equal(t, time.Date(2026, 8, 23, 10, 0, 0, 0, loc), rates[0].Start.UTC())
	require.Equal(t, time.Date(2026, 8, 23, 10, 30, 0, 0, loc), rates[0].End.UTC())
	require.Equal(t, 1000.0, rates[0].Value)

	require.Equal(t, time.Date(2026, 8, 23, 10, 30, 0, 0, loc), rates[1].Start.UTC())
	require.Equal(t, time.Date(2026, 8, 23, 11, 0, 0, 0, loc), rates[1].End.UTC())
	require.Equal(t, 2000.0, rates[1].Value)
}

// TestSolcastRatesDropsUnusablePeriod ensures an entry with a zero, negative or
// otherwise unusable Period is dropped rather than turned into a zero-length or
// inverted rate - Period is load-bearing (Start = PeriodEnd - Period), and an
// entry missing "period" in the response unmarshals to a zero Duration silently,
// since UnmarshalJSON is never called for a key that's simply absent.
func TestSolcastRatesDropsUnusablePeriod(t *testing.T) {
	loc := time.UTC

	forecasts := []solcast.Forecast{
		{PvEstimate: 1.0, PeriodEnd: time.Date(2026, 8, 23, 10, 30, 0, 0, loc), Period: solcast.Duration(30 * time.Minute)},
		{PvEstimate: 2.0, PeriodEnd: time.Date(2026, 8, 23, 11, 0, 0, 0, loc), Period: solcast.Duration(0)},             // absent/zero period
		{PvEstimate: 3.0, PeriodEnd: time.Date(2026, 8, 23, 11, 30, 0, 0, loc), Period: solcast.Duration(-time.Minute)}, // malformed/negative
		{PvEstimate: 4.0, PeriodEnd: time.Date(2026, 8, 23, 12, 0, 0, 0, loc), Period: solcast.Duration(30 * time.Minute)},
	}

	rates := solcastRates(forecasts, util.NewLogger("test"))
	require.Len(t, rates, 2, "only entries with a usable period are kept")

	require.Equal(t, time.Date(2026, 8, 23, 10, 0, 0, 0, loc), rates[0].Start.UTC())
	require.Equal(t, 1000.0, rates[0].Value)

	require.Equal(t, time.Date(2026, 8, 23, 11, 30, 0, 0, loc), rates[1].Start.UTC())
	require.Equal(t, 4000.0, rates[1].Value)
}

// TestSolcastRatesCarriesBand ensures the P10/P90 percentile band survives the
// conversion to api.Rate, scaled to W like Value, and that an entry without a band
// (as returned by Solcast plans/sites that don't include it) leaves Low/High nil
// rather than defaulting them to zero.
func TestSolcastRatesCarriesBand(t *testing.T) {
	loc := time.UTC
	p10, p90 := 0.5, 1.8

	forecasts := []solcast.Forecast{
		{PvEstimate: 1.0, PvEstimate10: &p10, PvEstimate90: &p90, PeriodEnd: time.Date(2026, 8, 23, 10, 30, 0, 0, loc), Period: solcast.Duration(30 * time.Minute)},
		{PvEstimate: 2.0, PeriodEnd: time.Date(2026, 8, 23, 11, 0, 0, 0, loc), Period: solcast.Duration(30 * time.Minute)}, // no band
	}

	rates := solcastRates(forecasts, util.NewLogger("test"))
	require.Len(t, rates, 2)

	require.NotNil(t, rates[0].Low)
	require.NotNil(t, rates[0].High)
	assert.Equal(t, 500.0, *rates[0].Low)
	assert.Equal(t, 1800.0, *rates[0].High)

	assert.Nil(t, rates[1].Low, "no band in the response: not defaulted to zero")
	assert.Nil(t, rates[1].High, "no band in the response: not defaulted to zero")
}
