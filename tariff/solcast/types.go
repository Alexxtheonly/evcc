package solcast

import (
	"time"

	"github.com/dylanmei/iso8601"
)

type Forecasts struct {
	Forecasts []Forecast
}

type Forecast struct {
	PvEstimate float64 `json:"pv_estimate"`
	// PvEstimate10 and PvEstimate90 are Solcast's 10th/90th percentile band around
	// PvEstimate. They are pointers because some Solcast plans/sites don't return them
	// at all - a key absent from the response would leave a plain float64 at its zero
	// value, indistinguishable from a genuine (e.g. nighttime) zero estimate, whereas a
	// nil pointer means "the API didn't send this."
	PvEstimate10 *float64  `json:"pv_estimate10"`
	PvEstimate90 *float64  `json:"pv_estimate90"`
	PeriodEnd    time.Time `json:"period_end"`
	Period       Duration
}

type Duration time.Duration

func (d *Duration) Duration() time.Duration {
	return time.Duration(*d)
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	val, err := iso8601.ParseDuration(string(data))
	if err != nil {
		return err
	}
	*d = Duration(val)
	return nil
}
