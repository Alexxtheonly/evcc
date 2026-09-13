package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEnergyConfigRejectsMalformedRequests(t *testing.T) {
	for _, body := range []string{`{"unknown":true}`, `{"settlementMode":"simulation"} {}`, `null`, `[]`, strings.Repeat("x", 17000)} {
		r := httptest.NewRequest(http.MethodPut, "/api/config/energyintelligence", strings.NewReader(body))
		w := httptest.NewRecorder()
		energyIntelligenceHandler(nil)(w, r)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	}
}
