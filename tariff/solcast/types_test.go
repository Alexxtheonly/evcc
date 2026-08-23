package solcast

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestForecastPercentileAbsentVsZero pins the reason PvEstimate10/90 are pointers: some
// Solcast plans/sites omit them entirely, and a plain float64 would unmarshal an absent
// key to the same zero value as a genuine (e.g. nighttime) zero estimate. Only a pointer
// keeps "not sent" distinguishable from "sent as zero".
func TestForecastPercentileAbsentVsZero(t *testing.T) {
	var absent Forecast
	require.NoError(t, json.Unmarshal([]byte(`{"pv_estimate":1.5}`), &absent))
	assert.Nil(t, absent.PvEstimate10, "key absent from the response")
	assert.Nil(t, absent.PvEstimate90, "key absent from the response")

	var zero Forecast
	require.NoError(t, json.Unmarshal([]byte(`{"pv_estimate":0,"pv_estimate10":0,"pv_estimate90":0}`), &zero))
	require.NotNil(t, zero.PvEstimate10, "key present with value 0")
	require.NotNil(t, zero.PvEstimate90, "key present with value 0")
	assert.Equal(t, 0.0, *zero.PvEstimate10)
	assert.Equal(t, 0.0, *zero.PvEstimate90)

	var present Forecast
	require.NoError(t, json.Unmarshal([]byte(`{"pv_estimate":1.5,"pv_estimate10":0.8,"pv_estimate90":2.3}`), &present))
	require.NotNil(t, present.PvEstimate10)
	require.NotNil(t, present.PvEstimate90)
	assert.Equal(t, 0.8, *present.PvEstimate10)
	assert.Equal(t, 2.3, *present.PvEstimate90)
}
