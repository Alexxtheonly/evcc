package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/evcc-io/evcc/core/site"
	"github.com/evcc-io/evcc/core/vehicle"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
)

type bandSite struct {
	site.API
	vehicles bandVehicles
}

func (s bandSite) Vehicles() site.Vehicles { return s.vehicles }

type bandVehicles struct {
	site.Vehicles
	v vehicle.API
}

func (vv bandVehicles) ByName(name string) (vehicle.API, error) {
	if name != "calypso" {
		return nil, errors.New("not found")
	}
	return vv.v, nil
}

func TestCheapBandVehicleHandlers(t *testing.T) {
	ctrl := gomock.NewController(t)
	v := vehicle.NewMockAPI(ctrl)
	s := bandSite{vehicles: bandVehicles{v: v}}

	serve := func(h http.HandlerFunc, name, value string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := mux.SetURLVars(httptest.NewRequest(http.MethodPost, "/", nil), map[string]string{"name": name, "value": value})
		h(w, r)
		return w
	}

	v.EXPECT().SetCheapBandSoc(50)
	v.EXPECT().GetCheapBandSoc().Return(50)
	w := serve(cheapBandSocHandler(s), "calypso", "50")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"value":50}`, w.Body.String())

	v.EXPECT().SetCheapBandMinRun(0)
	v.EXPECT().GetCheapBandMinRun().Return(0)
	w = serve(cheapBandMinRunHandler(s), "calypso", "0")
	assert.Equal(t, http.StatusOK, w.Code, "0 switches the minimum run off")

	// out of range or unknown vehicles never reach the setters (gomock fails on any call)
	assert.Equal(t, http.StatusBadRequest, serve(cheapBandSocHandler(s), "calypso", "101").Code)
	assert.Equal(t, http.StatusBadRequest, serve(cheapBandMinRunHandler(s), "calypso", "241").Code)
	assert.Equal(t, http.StatusBadRequest, serve(cheapBandSocHandler(s), "luna", "50").Code)
}
