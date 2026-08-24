package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/tariff"
	"github.com/stretchr/testify/require"
)

// TestSavingsLedgerErrorStatus: an error missing from this switch falls to the default
// case, reporting HTTP 500 for what is a refusal, not a server fault - which is what
// happened to metrics.ErrLoadpointNoChargeMeter (a configured loadpoint that has never
// written a single meters row - see that error's doc comment). Every other
// refusal ComputeLedger can return already maps to 422/400; this asserts the whole
// table, so a future error added to ComputeLedger without a case here shows up as a
// 500 in this test rather than in production.
func TestSavingsLedgerErrorStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"before tariff start", &metrics.ErrBeforeTariffStart{Earliest: time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)}, http.StatusUnprocessableEntity},
		{"range too large", metrics.ErrLedgerRangeTooLarge, http.StatusBadRequest},
		{"range unaligned", metrics.ErrLedgerRangeUnaligned, http.StatusBadRequest},
		// a reversed or identical from/to is a malformed request: without its own
		// sentinel it falls to the default 500.
		{"range inverted (reversed from/to)", metrics.ErrLedgerRangeInverted, http.StatusBadRequest},
		{"battery physics unavailable", metrics.ErrBatteryPhysicsUnavailable, http.StatusUnprocessableEntity},
		{"soc gap", metrics.ErrSocGap, http.StatusUnprocessableEntity},
		{"battery rate ceiling unavailable", metrics.ErrBatteryRateCeilingUnavailable, http.StatusUnprocessableEntity},
		{"loadpoint has no charge meter", metrics.ErrLoadpointNoChargeMeter, http.StatusUnprocessableEntity},
		{"no grid meter configured", metrics.ErrNoGridMeter, http.StatusUnprocessableEntity},
		{"no home meter configured", metrics.ErrNoHomeMeter, http.StatusUnprocessableEntity},
		{"wrapped loadpoint no charge meter", errors.New("wrap: " + metrics.ErrLoadpointNoChargeMeter.Error()), http.StatusInternalServerError}, // errors.New doesn't wrap - documents that only errors.Is-compatible wrapping is recognised
		{"unrecognised error", errors.New("boom"), http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, savingsLedgerErrorStatus(tt.err))
		})
	}
}

// TestSavingsLedgerErrorBody covers the "earliest" field savingsLedgerErrorBody adds
// for ErrBeforeTariffStart: the frontend's only way to learn what window would NOT be
// refused (see savingsLedgerChain.ts's clampWindowToEarliest). Asserts the field is
// present and RFC3339-correct when Earliest is set, and absent - not merely empty - in
// the zero-value case ("no priced tariff slots recorded yet"), and that every other
// error on this endpoint keeps the plain body with no earliest key at all.
func TestSavingsLedgerErrorBody(t *testing.T) {
	earliest := time.Date(2026, 8, 21, 12, 30, 0, 0, time.FixedZone("CEST", 2*60*60))

	t.Run("non-zero earliest is included as RFC3339", func(t *testing.T) {
		body := savingsLedgerErrorBody(&metrics.ErrBeforeTariffStart{Earliest: earliest})

		b, err := json.Marshal(body)
		require.NoError(t, err)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(b, &decoded))
		require.Equal(t, "no tariff data before 2026-08-21T12:30:00+02:00", decoded["error"])
		require.Equal(t, earliest.Format(time.RFC3339), decoded["earliest"])
	})

	t.Run("zero-value earliest omits the field entirely", func(t *testing.T) {
		body := savingsLedgerErrorBody(&metrics.ErrBeforeTariffStart{})

		b, err := json.Marshal(body)
		require.NoError(t, err)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(b, &decoded))
		require.Equal(t, "no priced tariff slots recorded yet", decoded["error"])
		_, present := decoded["earliest"]
		require.False(t, present, "earliest key must be absent, not just empty, when there is no instant to give")
	})

	t.Run("an unrelated error keeps the plain body with no earliest key", func(t *testing.T) {
		body := savingsLedgerErrorBody(errors.New("boom"))

		b, err := json.Marshal(body)
		require.NoError(t, err)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(b, &decoded))
		require.Equal(t, "boom", decoded["error"])
		_, present := decoded["earliest"]
		require.False(t, present)
	})
}

// TestStaticFeedInPrice is the outer half of the ledger's feed-in guard: only a tariff
// that declares its price time-invariant may stand in for a slot whose feed-in price
// was never recorded. A time-varying tariff's value today says nothing about a past
// slot, so it must not be offered at all - core/metrics' feedInFallback can only
// refuse what it is given, it cannot tell a static price from a forecast one.
func TestStaticFeedInPrice(t *testing.T) {
	require.Nil(t, staticFeedInPrice(nil), "no feed-in tariff configured")

	static, err := tariff.NewFixedFromConfig(map[string]any{"price": 0.0786})
	require.NoError(t, err)
	require.Equal(t, api.TariffTypePriceStatic, static.Type())

	got := staticFeedInPrice(static)
	require.NotNil(t, got)
	require.InDelta(t, 0.0786, *got, 1e-9)

	// the same provider with zones varies by time of day - not a lookup any more
	zoned, err := tariff.NewFixedFromConfig(map[string]any{
		"price": 0.0786,
		"zones": []map[string]any{{"price": 0.12, "hours": "10-16"}},
	})
	require.NoError(t, err)
	require.NotEqual(t, api.TariffTypePriceStatic, zoned.Type())
	require.Nil(t, staticFeedInPrice(zoned), "a time-varying feed-in tariff must never back-fill a past slot")
}
