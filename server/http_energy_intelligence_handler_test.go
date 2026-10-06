package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/evcc-io/evcc/core"
	"github.com/evcc-io/evcc/server/db/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnergyConfigRejectsMalformedRequests(t *testing.T) {
	for _, body := range []string{`{"unknown":true}`, `{"settlementMode":"simulation"} {}`, `null`, `[]`, strings.Repeat("x", 17000)} {
		r := httptest.NewRequest(http.MethodPut, "/api/config/energyintelligence", strings.NewReader(body))
		w := httptest.NewRecorder()
		energyIntelligenceHandler(nil)(w, r)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	}
}

// TestSolarShadingSettingToggle: the setting persists through the existing
// energy settings endpoint, is off when absent (settings saved before it
// existed) and is reflected read-only by the solar shading endpoint.
func TestSolarShadingSettingToggle(t *testing.T) {
	site := core.NewSite()
	t.Cleanup(func() { _ = settings.SetJson("energyIntelligence", map[string]any{"settlementMode": "simulation"}) })

	// saved by an earlier version: no solarShading field
	require.NoError(t, settings.SetJson("energyIntelligence", map[string]any{"robust": true, "settlementMode": "interval"}))
	assert.False(t, site.GetEnergyIntelligenceSettings().SolarShading)

	put := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/config/energyintelligence", strings.NewReader(body))
		w := httptest.NewRecorder()
		energyIntelligenceHandler(site)(w, r)
		return w
	}
	shadingStatus := func() map[string]any {
		w := httptest.NewRecorder()
		solarShadingHandler(site)(w, httptest.NewRequest(http.MethodGet, "/api/energyplan/solarshading", nil))
		require.Equal(t, http.StatusOK, w.Code)
		var res map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
		return res
	}

	w := put(`{"robust":true,"settlementMode":"interval","solarShading":true}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"solarShading":true`)
	assert.True(t, site.GetEnergyIntelligenceSettings().SolarShading)
	assert.True(t, site.GetEnergyIntelligenceSettings().Robust, "other settings preserved")

	st := shadingStatus()
	assert.Equal(t, true, st["enabled"])
	assert.Equal(t, false, st["active"], "no solar forecast coordinates: never active")
	assert.Equal(t, "coordinates", st["reason"])
	assert.Equal(t, []any{}, st["bins"])

	require.Equal(t, http.StatusOK, put(`{"robust":true,"settlementMode":"interval","solarShading":false}`).Code)
	assert.False(t, site.GetEnergyIntelligenceSettings().SolarShading)
	assert.Equal(t, false, shadingStatus()["enabled"])

	assert.Equal(t, http.StatusBadRequest, put(`{"settlementMode":"interval","solarShading":"yes"}`).Code)
	assert.False(t, site.GetEnergyIntelligenceSettings().SolarShading)
}
