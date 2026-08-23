package api

import (
	"encoding/json"
	"slices"
	"time"
)

// Rate is a grid tariff rate
type Rate struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Value float64   `json:"value"`
	// Forecast marks a rate as extending past a merged tariff's primary source -
	// e.g. a slot filled in by the secondary tariff beyond the primary's own known
	// horizon (see tariff.Merged). It is about horizon, not confidence: a tariff
	// that is inherently a prediction end to end (e.g. solar) is never "beyond its
	// own horizon" and reports false for every rate it produces, so this field is
	// not a general "is this value uncertain" flag - only tariff.Merged sets it.
	// Zero value (false) means "not past the primary's horizon" (or "no such
	// concept applies"), the common case.
	Forecast bool `json:"forecast,omitempty"`
	// Low and High optionally bound Value with a provider's confidence-interval
	// forecast, e.g. Solcast's pv_estimate10/pv_estimate90 around pv_estimate. nil
	// means the provider does not supply a band for this rate, the common case -
	// callers must treat a nil Low or High as "unknown", not as zero.
	Low  *float64 `json:"low,omitempty"`
	High *float64 `json:"high,omitempty"`
}

// IsZero returns is the rate is the zero value
func (r Rate) IsZero() bool {
	return r.Start.IsZero() && r.End.IsZero() && r.Value == 0 && !r.Forecast && r.Low == nil && r.High == nil
}

// Rates is a slice of (future) tariff rates
type Rates []Rate

// Sort rates by start time
func (rr Rates) Sort() {
	slices.SortStableFunc(rr, func(i, j Rate) int {
		return i.Start.Compare(j.Start)
	})
}

// At returns the rate for given timestamp or error.
// Rates MUST be sorted by start time.
func (rr Rates) At(ts time.Time) (Rate, error) {
	if i, ok := slices.BinarySearchFunc(rr, ts, func(r Rate, ts time.Time) int {
		switch {
		case ts.Before(r.Start):
			return +1
		case !ts.Before(r.End):
			return -1
		default:
			return 0
		}
	}); ok {
		return rr[i], nil
	}

	return Rate{}, ErrNotAvailable
}

var _ BytesMarshaler = (*Rates)(nil)

// MarshalBytes implements server.BytesMarshaler
func (r Rates) MarshalBytes() ([]byte, error) {
	return json.Marshal(r)
}
