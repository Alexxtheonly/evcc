package server

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
)

// savingsLedgerHandler serves the ADR-011 savings ledger for a period: the
// realised-grid-cost figure, the W0..W3 world chain (both settlement modes, plus the
// routing/timing split when a battery is configured), and the per-slot battery-mode
// decision replay. Shaped like the neighbouring /api/db/metrics endpoints
// (server/http_db_metrics_handler.go) - the same mandatory from/to via timeRange, the
// same jsonError/jsonWrite pattern - but registered on the unauthenticated site API
// alongside /history/energy and /tariff, since this is read-only history like those,
// not a destructive operation like the /api/db routes.
//
// ComputeLedger runs upwards of a dozen queries against a database with a single
// connection (server/db/db.go's SetMaxOpenConns(1)), and this endpoint carries no auth
// (see the doc comment above) - r.Context() is threaded through every query so an
// abandoned request (client gone, or metrics.ErrLedgerRangeTooLarge rejecting the
// range up front) doesn't run to completion queued behind persist()/control-slot
// writes for no reader.
func savingsLedgerHandler(w http.ResponseWriter, r *http.Request) {
	if db.Instance == nil {
		jsonError(w, http.StatusBadRequest, errors.New("database offline"))
		return
	}

	from, to, err := timeRange(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err)
		return
	}

	ledger, err := metrics.ComputeLedger(r.Context(), from, to)
	if err != nil {
		jsonError(w, savingsLedgerErrorStatus(err), err)
		return
	}

	// data only changes at the next slot boundary - same header /history/energy sets
	// (server/http_history_handler.go)
	maxAge := time.Until(time.Now().Truncate(tariff.SlotDuration).Add(tariff.SlotDuration))
	w.Header().Set("Cache-Control", fmt.Sprintf("private, max-age=%d", int(maxAge.Seconds())))

	jsonWrite(w, ledger)
}

// savingsLedgerErrorStatus maps a ComputeLedger error to an HTTP status. Pulled out
// of savingsLedgerHandler so the mapping itself is unit-testable without driving the
// full metrics/db stack (see http_savings_ledger_handler_test.go) - a case missing
// here previously fell to the default 500, which is exactly what happened to
// metrics.ErrLoadpointNoChargeMeter: a refusal (ADR-011 rule 4: a configured
// loadpoint with no charge-meter history) reported as a server fault instead of the
// 422 every other refusal in this switch gets.
func savingsLedgerErrorStatus(err error) int {
	var refused *metrics.ErrBeforeTariffStart
	switch {
	case errors.As(err, &refused):
		return http.StatusUnprocessableEntity
	case errors.Is(err, metrics.ErrLedgerRangeTooLarge), errors.Is(err, metrics.ErrLedgerRangeUnaligned):
		return http.StatusBadRequest
	// ComputeLedger degrades a battery-physics refusal to a null Chain rather than
	// failing the whole request (see Ledger's doc comment), so these three only
	// reach here via a caller that skips that degradation - still not a server
	// fault (ADR-011 rule 4: a refusal, not a crash), so 422 not 500.
	case errors.Is(err, metrics.ErrBatteryPhysicsUnavailable),
		errors.Is(err, metrics.ErrSocGap),
		errors.Is(err, metrics.ErrBatteryRateCeilingUnavailable):
		return http.StatusUnprocessableEntity
	// a configured loadpoint with no charge-meter history at all - refusing to
	// model a car-free household rather than silently guessing one (see
	// ErrLoadpointNoChargeMeter's doc comment). A request-shape/data problem, not a
	// server fault.
	case errors.Is(err, metrics.ErrLoadpointNoChargeMeter):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}
