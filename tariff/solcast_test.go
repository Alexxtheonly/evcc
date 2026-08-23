package tariff

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/tariff/solcast"
	"github.com/evcc-io/evcc/util"
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
