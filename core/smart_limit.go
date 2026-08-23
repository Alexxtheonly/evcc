package core

import (
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/loadpoint"
)

// smartLimitMinSamples is the minimum number of forward rate slots required
// before a percentile-based smart cost or battery grid charge limit is
// resolved. Below this the window says too little about "the cheap part of
// the day" to be meaningful, so the limit is treated as not configured for
// that cycle rather than resolved against noise.
const smartLimitMinSamples = 4

// percentileLimit resolves a 0-100 percentile of rates' price distribution
// into an absolute price. Returns nil if the window has too few samples.
func percentileLimit(pct float64, rates api.Rates) *float64 {
	if len(rates) == 0 {
		return nil
	}

	values := make([]float64, len(rates))
	for i, r := range rates {
		values[i] = r.Value
	}

	v, ok := percentileOf(values, pct/100, smartLimitMinSamples)
	if !ok {
		return nil
	}

	return &v
}

// resolveSmartCostLimit returns lp's effective smart cost limit as an
// absolute price: the configured absolute value unchanged, or - when a
// percentile is configured instead - that percentile of rates, freshly
// resolved against the window the caller passes in so it tracks the current
// price spread rather than a value fixed at config time.
func resolveSmartCostLimit(lp loadpoint.API, rates api.Rates) *float64 {
	if pct := lp.GetSmartCostLimitPercentile(); pct != nil {
		return percentileLimit(*pct, rates)
	}

	return lp.GetSmartCostLimit()
}

// resolveBatteryGridChargeLimit is the site-level equivalent of
// resolveSmartCostLimit for the battery grid charge limit.
func (site *Site) resolveBatteryGridChargeLimit(rates api.Rates) *float64 {
	if pct := site.GetBatteryGridChargeLimitPercentile(); pct != nil {
		return percentileLimit(*pct, rates)
	}

	return site.GetBatteryGridChargeLimit()
}
