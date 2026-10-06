package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/site"
	"github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/require"
)

type planHistoryTestLoadpoint struct {
	loadpoint.API
	title string
}

func (lp planHistoryTestLoadpoint) GetTitle() string { return lp.title }

type planHistoryTestSite struct {
	site.API
	titles []string
}

func (s planHistoryTestSite) Loadpoints() []loadpoint.API {
	res := make([]loadpoint.API, 0, len(s.titles))
	for _, title := range s.titles {
		res = append(res, planHistoryTestLoadpoint{title: title})
	}
	return res
}

func TestEnergyPlanHistoryHandler(t *testing.T) {
	previous := db.Instance
	t.Cleanup(func() { db.Instance = previous })
	handler := energyPlanHistoryHandler(planHistoryTestSite{})
	request := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}

	db.Instance = nil
	w := request("/api/energyplan/history")
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "database offline")

	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())

	for _, hours := range []string{"0", "49", "-6", "abc", "1.5"} {
		require.Equal(t, http.StatusBadRequest, request("/api/energyplan/history?hours="+hours).Code, hours)
	}

	w = request("/api/energyplan/history?hours=6")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))
	var res metrics.PlanHistory
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	require.Len(t, res.Forecast, 24, "six hours of empty 15min slots")
	require.Equal(t, 6*time.Hour, res.To.Sub(res.From))
	require.Nil(t, res.Forecast[0].HomeWh, "no data is a gap, not zero")
	require.True(t, res.Forecast[0].Measured)
	require.NotNil(t, res.Devices)

	w = request("/api/energyplan/history")
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	require.Len(t, res.Forecast, 96, "24 hours by default")
}

func TestLoadpointPlanKeys(t *testing.T) {
	keys := loadpointPlanKeys(planHistoryTestSite{titles: []string{"Carport", "", "Garage", "Garage", "Heat pump"}})
	require.Equal(t, map[string]string{"Carport": "loadpoint:0", "Heat pump": "loadpoint:4"}, keys)
}
