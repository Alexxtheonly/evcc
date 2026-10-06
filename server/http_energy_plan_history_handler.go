package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/site"
	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
)

// energyPlanHistoryHandler serves the measured slots before the current plan, shaped
// like the plan so the energy plan chart can draw past and plan on one axis.
// Read-only history like /history/energy, registered beside it.
func energyPlanHistoryHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db.Instance == nil {
			jsonError(w, http.StatusBadRequest, errors.New("database offline"))
			return
		}

		hours := 24
		if s := r.URL.Query().Get("hours"); s != "" {
			var err error
			if hours, err = strconv.Atoi(s); err != nil {
				jsonError(w, http.StatusBadRequest, metrics.ErrPlanHistoryRange)
				return
			}
		}
		from, to, err := metrics.PlanHistoryWindow(time.Now(), hours)
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		res, err := metrics.ComputePlanHistory(r.Context(), from, to, loadpointPlanKeys(site))
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err)
			return
		}

		// data only changes at the next slot boundary
		maxAge := time.Until(time.Now().Truncate(tariff.SlotDuration).Add(tariff.SlotDuration))
		w.Header().Set("Cache-Control", fmt.Sprintf("private, max-age=%d", int(maxAge.Seconds())))

		jsonWrite(w, res)
	}
}

// loadpointPlanKeys maps loadpoint titles - the metrics entity title - to the plan's
// loadpoint:<index> key. A title shared by two loadpoints maps to neither.
func loadpointPlanKeys(site site.API) map[string]string {
	keys := make(map[string]string)
	shared := make(map[string]bool)
	for i, lp := range site.Loadpoints() {
		title := lp.GetTitle()
		if title == "" {
			continue
		}
		if _, ok := keys[title]; ok {
			shared[title] = true
		}
		keys[title] = fmt.Sprintf("loadpoint:%d", i)
	}
	for title := range shared {
		delete(keys, title)
	}
	return keys
}
