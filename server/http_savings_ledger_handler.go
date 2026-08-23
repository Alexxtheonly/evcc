package server

import (
	"errors"
	"net/http"

	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/server/db"
)

// savingsLedgerHandler serves the ADR-011 savings ledger for a period: the
// realised-grid-cost figure, the W0..W3 world chain (both settlement modes, plus the
// routing/timing split when a battery is configured), and the per-slot battery-mode
// decision replay. Shaped like the neighbouring /api/db/metrics endpoints
// (server/http_db_metrics_handler.go) - the same mandatory from/to via timeRange, the
// same jsonError/jsonWrite pattern - but registered on the unauthenticated site API
// alongside /history/energy and /tariff, since this is read-only history like those,
// not a destructive operation like the /api/db routes.
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

	ledger, err := metrics.ComputeLedger(from, to)
	if err != nil {
		var refused *metrics.ErrBeforeTariffStart
		if errors.As(err, &refused) {
			jsonError(w, http.StatusUnprocessableEntity, err)
			return
		}
		jsonError(w, http.StatusInternalServerError, err)
		return
	}

	jsonWrite(w, ledger)
}
