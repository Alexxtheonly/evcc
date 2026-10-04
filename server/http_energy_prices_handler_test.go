package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/site"
	"github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/require"
)

type energyPriceTestSite struct{ site.API }

func (energyPriceTestSite) GetTariff(api.TariffUsage) api.Tariff { return nil }

func TestEnergyPricesHandler(t *testing.T) {
	previous := db.Instance
	t.Cleanup(func() { db.Instance = previous })
	handler := energyPricesHandler(energyPriceTestSite{})
	request := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	db.Instance = nil
	require.Equal(t, http.StatusBadRequest, request("/api/energyprices").Code)
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())
	require.Equal(t, http.StatusBadRequest, request("/api/energyprices?month=not-a-month").Code)
	now := time.Now()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	require.Equal(t, http.StatusBadRequest, request("/api/energyprices?month="+first.AddDate(0, 1, 0).Format("2006-01")).Code)
	month := first.AddDate(0, -1, 0).Format("2006-01")
	w := request("/api/energyprices?month=" + month)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))
	require.True(t, json.Valid(w.Body.Bytes()))
	var report metrics.EnergyPriceReport
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &report))
	require.Equal(t, month, report.Month)
	require.Nil(t, report.Totals.Grid.WeightedPrice)
	require.NotEmpty(t, report.Days)
}
