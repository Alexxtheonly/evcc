package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/metrics"
	"github.com/stretchr/testify/require"
)

// TestSavingsLedgerErrorStatus covers A17: metrics.ErrLoadpointNoChargeMeter (a
// configured loadpoint that has never written a single meters row - see that error's
// doc comment) was missing from this switch and fell to the default case, reporting
// HTTP 500 for what is a refusal (ADR-011 rule 4), not a server fault. Every other
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
		// D1: a reversed or identical from/to used to be a plain errors.New in
		// buildLedgerSlots (core/metrics/ledger_slots.go), unrecognised here, so it fell
		// to the default 500 - reproduced live via
		// GET /api/savingsledger?from=...T00:00+02:00&to=...(earlier)T00:00+02:00.
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
