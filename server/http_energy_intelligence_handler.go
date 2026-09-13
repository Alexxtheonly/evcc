package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/evcc-io/evcc/core"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/server/db"
	"github.com/gorilla/mux"
	"gorm.io/gorm"
)

func energyIntelligenceHandler(site *core.Site) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			var cfg core.EnergyIntelligenceSettings
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&cfg); err != nil {
				jsonError(w, http.StatusBadRequest, err)
				return
			}
			if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
				jsonError(w, http.StatusBadRequest, errors.New("expected one JSON object"))
				return
			}
			if err := site.SetEnergyIntelligenceSettings(cfg); err != nil {
				jsonError(w, http.StatusBadRequest, err)
				return
			}
		}
		jsonWrite(w, site.GetEnergyIntelligenceSettings())
	}
}

func optimizerSnapshotHandler(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(mux.Vars(r)["id"], 10, 64)
	if err != nil || id == 0 {
		jsonError(w, http.StatusBadRequest, errors.New("invalid snapshot id"))
		return
	}
	if db.Instance == nil {
		jsonError(w, http.StatusServiceUnavailable, errors.New("database offline"))
		return
	}
	snapshot, err := metrics.GetOptimizerSnapshot(id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status = http.StatusNotFound
		}
		jsonError(w, status, err)
		return
	}
	jsonWrite(w, snapshot)
}

func deleteEnergyIntelligenceHistory(remove func(time.Time, time.Time) (int64, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db.Instance == nil {
			jsonError(w, http.StatusBadRequest, errors.New("database offline"))
			return
		}
		from, to, err := timeRange(r)
		if err != nil || !to.After(from) {
			jsonError(w, http.StatusBadRequest, errors.New("invalid history interval"))
			return
		}
		rows, err := remove(from, to)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err)
			return
		}
		jsonWrite(w, deleteResult{rows})
	}
}
