package tariff

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/tariff/solcast"
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

	rates := solcastRates(forecasts)
	require.Len(t, rates, 2)

	require.Equal(t, time.Date(2026, 8, 23, 10, 0, 0, 0, loc), rates[0].Start.UTC())
	require.Equal(t, time.Date(2026, 8, 23, 10, 30, 0, 0, loc), rates[0].End.UTC())
	require.Equal(t, 1000.0, rates[0].Value)

	require.Equal(t, time.Date(2026, 8, 23, 10, 30, 0, 0, loc), rates[1].Start.UTC())
	require.Equal(t, time.Date(2026, 8, 23, 11, 0, 0, 0, loc), rates[1].End.UTC())
	require.Equal(t, 2000.0, rates[1].Value)
}
