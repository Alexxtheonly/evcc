package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/site"
	"github.com/evcc-io/evcc/server/db"
)

// energyPricesHandler serves one site-local calendar month, including daily
// buckets of that same replay. No arbitrary day range can reset the battery.
func energyPricesHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db.Instance == nil {
			jsonError(w, http.StatusBadRequest, errors.New("database offline"))
			return
		}
		report, err := metrics.ComputeEnergyPrices(r.Context(), r.URL.Query().Get("month"), time.Now(), staticFeedInPrice(site.GetTariff(api.TariffUsageFeedIn)))
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, metrics.ErrEnergyPriceMonth) {
				status = http.StatusBadRequest
			}
			jsonError(w, status, err)
			return
		}
		payload, err := json.Marshal(report)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, errors.New("historical arithmetic cannot be represented as finite JSON"))
			return
		}
		w.Header().Set("Cache-Control", "private, max-age=0")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}
}
