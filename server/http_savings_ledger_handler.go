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
		var refused *metrics.ErrBeforeTariffStart
		switch {
		case errors.As(err, &refused):
			jsonError(w, http.StatusUnprocessableEntity, err)
		case errors.Is(err, metrics.ErrLedgerRangeTooLarge), errors.Is(err, metrics.ErrLedgerRangeUnaligned):
			jsonError(w, http.StatusBadRequest, err)
		default:
			jsonError(w, http.StatusInternalServerError, err)
		}
		return
	}

	// data only changes at the next slot boundary - same header /history/energy sets
	// (server/http_history_handler.go)
	maxAge := time.Until(time.Now().Truncate(tariff.SlotDuration).Add(tariff.SlotDuration))
	w.Header().Set("Cache-Control", fmt.Sprintf("private, max-age=%d", int(maxAge.Seconds())))

	jsonWrite(w, ledger)
}
