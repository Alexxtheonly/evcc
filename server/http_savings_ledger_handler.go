package server

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/site"
	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
)

// savingsLedgerHandler serves the savings ledger for a period: the
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
func savingsLedgerHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db.Instance == nil {
			jsonError(w, http.StatusBadRequest, errors.New("database offline"))
			return
		}

		from, to, err := timeRange(r)
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		ledger, err := metrics.ComputeLedger(r.Context(), from, to, staticFeedInPrice(site.GetTariff(api.TariffUsageFeedIn)))
		if err != nil {
			w.WriteHeader(savingsLedgerErrorStatus(err))
			jsonWrite(w, savingsLedgerErrorBody(err))
			return
		}

		// data only changes at the next slot boundary - same header /history/energy sets
		// (server/http_history_handler.go)
		maxAge := time.Until(time.Now().Truncate(tariff.SlotDuration).Add(tariff.SlotDuration))
		w.Header().Set("Cache-Control", fmt.Sprintf("private, max-age=%d", int(maxAge.Seconds())))

		jsonWrite(w, ledger)
	}
}

// staticFeedInPrice returns the feed-in tariff's currently configured price, but ONLY
// when that tariff declares itself api.TariffTypePriceStatic - a declaration that the
// price does not vary with time. That lets the ledger price a slot whose feed-in value
// was never recorded (see core/metrics' feedInFallback, which additionally requires the
// period's own record to corroborate the value before using it). Any other tariff type
// varies with time, so today's value says nothing about a past slot: nil, and those
// slots stay excluded.
func staticFeedInPrice(t api.Tariff) *float64 {
	if t == nil || t.Type() != api.TariffTypePriceStatic {
		return nil
	}

	v, err := tariff.Now(t)
	if err != nil {
		return nil
	}
	return &v
}

// savingsLedgerErrorBody builds the JSON error body for a ComputeLedger error. Pulled
// out of savingsLedgerHandler for the same testability reason as savingsLedgerErrorStatus
// below (see its doc comment) - and because ErrBeforeTariffStart is the one error on
// this endpoint that needs more than the plain util.ErrorAsJson shape every other
// handler in this file uses: an "earliest" field carrying the earliest instant the
// tariffs table has a price for, so the frontend can retry with a window that won't
// also be refused (see savingsLedgerChain.ts's clampWindowToEarliest). Only added when
// Earliest is non-zero - the zero-value case ("no priced tariff slots recorded yet",
// see the type's doc comment) has no instant to give, so it falls through to the plain
// body like every other refusal.
func savingsLedgerErrorBody(err error) any {
	var refused *metrics.ErrBeforeTariffStart
	if errors.As(err, &refused) && !refused.Earliest.IsZero() {
		return struct {
			Error    string `json:"error"`
			Earliest string `json:"earliest"`
		}{
			Error:    err.Error(),
			Earliest: refused.Earliest.Format(time.RFC3339),
		}
	}
	return util.ErrorAsJson(err)
}

// savingsLedgerErrorStatus maps a ComputeLedger error to an HTTP status. Pulled out
// of savingsLedgerHandler so the mapping itself is unit-testable without driving the
// full metrics/db stack (see http_savings_ledger_handler_test.go). Every refusal
// needs its own case: one missing falls to the default 500, reporting a refusal as a
// server fault instead of the 422 every other refusal in this switch gets.
func savingsLedgerErrorStatus(err error) int {
	var refused *metrics.ErrBeforeTariffStart
	switch {
	case errors.As(err, &refused):
		return http.StatusUnprocessableEntity
	// malformed request shape: an out-of-bounds, unaligned, or reversed/identical
	// [from,to).
	case errors.Is(err, metrics.ErrLedgerRangeTooLarge),
		errors.Is(err, metrics.ErrLedgerRangeUnaligned),
		errors.Is(err, metrics.ErrLedgerRangeInverted):
		return http.StatusBadRequest
	// ComputeLedger degrades a battery-physics refusal to a null Chain rather than
	// failing the whole request (see Ledger's doc comment), so these three only
	// reach here via a caller that skips that degradation - still not a server
	// fault - a refusal, not a crash - so 422 not 500.
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
	// the site itself has no grid or home meter configured at all - a configuration
	// problem the ledger refuses to work around, not a server fault.
	case errors.Is(err, metrics.ErrNoGridMeter), errors.Is(err, metrics.ErrNoHomeMeter):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}
