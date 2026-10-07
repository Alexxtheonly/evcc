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

// TestHeatingDegreeSettingToggle: the setting persists through the energy
// settings endpoint, reads as auto when absent (settings saved before it
// existed), rejects unknown values and is reflected by the status endpoint.
func TestHeatingDegreeSettingToggle(t *testing.T) {
	site := core.NewSite()
	t.Cleanup(func() { _ = settings.SetJson("energyIntelligence", map[string]any{"settlementMode": "simulation"}) })

	require.NoError(t, settings.SetJson("energyIntelligence", map[string]any{"robust": true, "settlementMode": "interval"}))
	assert.Equal(t, "auto", site.GetEnergyIntelligenceSettings().HeatingDegree)

	put := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/config/energyintelligence", strings.NewReader(body))
		w := httptest.NewRecorder()
		energyIntelligenceHandler(site)(w, r)
		return w
	}
	status := func() map[string]any {
		w := httptest.NewRecorder()
		heatingDegreeHandler(site)(w, httptest.NewRequest(http.MethodGet, "/api/energyplan/heatingdegree", nil))
		require.Equal(t, http.StatusOK, w.Code)
		var res map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
		return res
	}

	assert.Equal(t, "auto", status()["mode"])

	for _, mode := range []string{"on", "off", "auto"} {
		w := put(`{"robust":true,"settlementMode":"interval","heatingDegree":"` + mode + `"}`)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), `"heatingDegree":"`+mode+`"`)
		assert.Equal(t, mode, site.GetEnergyIntelligenceSettings().HeatingDegree)
		assert.True(t, site.GetEnergyIntelligenceSettings().Robust, "other settings preserved")
		st := status()
		assert.Equal(t, mode, st["mode"])
		assert.Equal(t, false, st["temperatureForecast"])
		assert.Equal(t, false, st["open"])
		assert.Equal(t, "no_tariff", st["reason"])
		assert.Equal(t, "none", st["applied"])
	}

	// a client that does not know the setting keeps the default
	require.Equal(t, http.StatusOK, put(`{"settlementMode":"interval"}`).Code)
	assert.Equal(t, "auto", site.GetEnergyIntelligenceSettings().HeatingDegree)

	for _, bad := range []string{`"yes"`, `true`, `"ON"`} {
		assert.Equal(t, http.StatusBadRequest, put(`{"settlementMode":"interval","heatingDegree":`+bad+`}`).Code, bad)
	}
	assert.Equal(t, "auto", site.GetEnergyIntelligenceSettings().HeatingDegree)
}

// TestCheapBandReferenceSettings: the band reference settings persist through the energy
// settings endpoint, stay absent (defaults) when not sent and reject nonsense.
func TestCheapBandReferenceSettings(t *testing.T) {
	site := core.NewSite()
	t.Cleanup(func() { _ = settings.SetJson("energyIntelligence", map[string]any{"settlementMode": "simulation"}) })

	put := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/config/energyintelligence", strings.NewReader(body))
		w := httptest.NewRecorder()
		energyIntelligenceHandler(site)(w, r)
		return w
	}

	require.Equal(t, http.StatusOK, put(`{"settlementMode":"interval"}`).Code)
	cfg := site.GetEnergyIntelligenceSettings()
	assert.Nil(t, cfg.CheapBandDays)
	assert.Nil(t, cfg.CheapBandMargin)
	assert.Nil(t, cfg.CheapBandPrice)
	assert.False(t, cfg.CheapBandFromBattery, "home batteries hold during band charging by default")

	w := put(`{"settlementMode":"interval","cheapBandDays":90,"cheapBandMargin":0,"cheapBandPrice":0.28,"cheapBandFromBattery":true}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	cfg = site.GetEnergyIntelligenceSettings()
	require.NotNil(t, cfg.CheapBandMargin)
	assert.Equal(t, 90, *cfg.CheapBandDays)
	assert.Equal(t, 0.0, *cfg.CheapBandMargin, "zero margin is kept, not replaced by the default")
	assert.Equal(t, 0.28, *cfg.CheapBandPrice)
	assert.True(t, cfg.CheapBandFromBattery)

	for _, bad := range []string{`"cheapBandDays":0`, `"cheapBandDays":60`, `"cheapBandDays":400`, `"cheapBandMargin":-1`, `"cheapBandMargin":95`, `"cheapBandPrice":50`} {
		assert.Equal(t, http.StatusBadRequest, put(`{"settlementMode":"interval",`+bad+`}`).Code, bad)
	}
	assert.Equal(t, 90, *site.GetEnergyIntelligenceSettings().CheapBandDays)
}
