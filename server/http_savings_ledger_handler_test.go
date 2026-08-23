package server

import (
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
		{"battery physics unavailable", metrics.ErrBatteryPhysicsUnavailable, http.StatusUnprocessableEntity},
		{"soc gap", metrics.ErrSocGap, http.StatusUnprocessableEntity},
		{"battery rate ceiling unavailable", metrics.ErrBatteryRateCeilingUnavailable, http.StatusUnprocessableEntity},
		{"loadpoint has no charge meter", metrics.ErrLoadpointNoChargeMeter, http.StatusUnprocessableEntity},
		{"wrapped loadpoint no charge meter", errors.New("wrap: " + metrics.ErrLoadpointNoChargeMeter.Error()), http.StatusInternalServerError}, // errors.New doesn't wrap - documents that only errors.Is-compatible wrapping is recognised
		{"unrecognised error", errors.New("boom"), http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, savingsLedgerErrorStatus(tt.err))
		})
	}
}
