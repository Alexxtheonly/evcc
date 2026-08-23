package core

import (
	"math"
	"slices"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/tariff"
	"github.com/jinzhu/now"
)

// heatingDegreeBaseTemp is the reference outdoor temperature (°C) below which extra heating
// energy is assumed. 15°C is a conventional heating-degree-day base for buildings with
// typical internal/solar gains (lower than the classic 18°C base, which assumes none).
const heatingDegreeBaseTemp = 15.0

func heatingDegree(tempC float64) float64 {
	return max(0, heatingDegreeBaseTemp-tempC)
}

const (
	// heatingDegreeMinSamples requires most of the 30-day lookback queryHeatingDegreeFit uses
	// (2880 slots at 96/day) to be covered by paired (energy, temperature) samples, not just a
	// handful of days. meanHDD (the fit's zero point) is computed only over this joined sample
	// set, while the base profile applyHeatingDegree corrects is a separate 30-day average - if
	// a temperature tariff gets configured during a cold snap, a low sample count would let
	// meanHDD lock in that cold snap's average as "typical," under-correcting every forecast
	// slot from then on. 2000 (~20.8 days) requires the joined window to already resemble the
	// full 30-day one before the fit is trusted; a fresh setup instead runs on the unmodified
	// profile until enough of the month has actually accrued.
	heatingDegreeMinSamples = 2000

	// heatingDegreeMinCorrelation is the minimum Pearson correlation between home load and
	// heating-degree before the fit is trusted. This is the "demonstrated fit" gate: a site
	// with no temperature-sensitive load (no heat pump, no electric heating) will not clear
	// it and the profile stays exactly as it is today, regardless of how much temperature
	// history has accrued - presence of data is not the same as the data being informative.
	heatingDegreeMinCorrelation = 0.3
)

// heatingDegreeFit is a demonstrated linear relationship between home load and heating-degree.
type heatingDegreeFit struct {
	slope   float64 // Wh per heating-degree
	meanHDD float64 // mean heating-degree over the fit window
}

// adjust returns the correction (Wh) for a slot at the given forecast temperature, relative
// to the fit window's average conditions - not an absolute load, since the base profile
// already reflects the average of everything that happened during the fit window, heating
// included. A forecast slot at exactly the average heating-degree gets no correction.
func (f heatingDegreeFit) adjust(tempC float64) float64 {
	return f.slope * (heatingDegree(tempC) - f.meanHDD)
}

// fitHeatingDegree fits a home-load-vs-heating-degree slope by ordinary least squares and
// reports whether the correlation clears heatingDegreeMinCorrelation with at least
// heatingDegreeMinSamples pairs. False means "do not apply a correction" - noise, a flat
// temperature history, or a site whose load genuinely does not respond to temperature all
// look the same here and are all handled the same way: leave the profile alone.
func fitHeatingDegree(samples []metrics.TemperatureSample) (heatingDegreeFit, bool) {
	if len(samples) < heatingDegreeMinSamples {
		return heatingDegreeFit{}, false
	}

	var sumX, sumY float64
	for _, s := range samples {
		sumX += heatingDegree(s.Temperature)
		sumY += s.Energy
	}
	n := float64(len(samples))
	meanX, meanY := sumX/n, sumY/n

	var sxy, sxx, syy float64
	for _, s := range samples {
		dx := heatingDegree(s.Temperature) - meanX
		dy := s.Energy - meanY
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}

	// no variance in heating-degree (a mild window) or in load: nothing to fit either way
	if sxx == 0 || syy == 0 {
		return heatingDegreeFit{}, false
	}

	r := sxy / math.Sqrt(sxx*syy)
	if r < heatingDegreeMinCorrelation {
		return heatingDegreeFit{}, false
	}

	return heatingDegreeFit{slope: sxy / sxx, meanHDD: meanX}, true
}

// heatingDegreeResult is what solarScaleCached-style caching wraps: the fit plus whether it
// is trusted, since "no fit" is an expected outcome, not an error (see fitHeatingDegree).
type heatingDegreeResult struct {
	fit heatingDegreeFit
	ok  bool
}

// queryHeatingDegreeFit does the actual metrics query and regression for the cached fit,
// over the same lookback window homeProfile itself uses.
func (site *Site) queryHeatingDegreeFit() (heatingDegreeResult, error) {
	samples, err := metrics.QueryHomeTemperatureSamples(now.BeginningOfDay().AddDate(0, 0, -30))
	if err != nil {
		return heatingDegreeResult{}, err
	}

	fit, ok := fitHeatingDegree(samples)
	return heatingDegreeResult{fit: fit, ok: ok}, nil
}

// applyHeatingDegree adjusts profile (kWh per slot, aligned starting at the current slot - see
// profileSlotsFromNow) with a heating-degree correction, when a temperature forecast is
// configured and history demonstrates a fit (see fitHeatingDegree). Otherwise profile comes
// back completely unchanged: no tariff configured, no cached fit yet, or a fit that failed
// the correlation gate are all handled identically, on purpose - this is the "must not
// degrade the profile" requirement, not three different code paths to keep in sync.
func (site *Site) applyHeatingDegree(profile []float64) []float64 {
	tempTariff := site.GetTariff(api.TariffUsageTemperature)
	if tempTariff == nil {
		return profile
	}

	result, err := site.heatingDegreeCached()
	if err != nil {
		site.log.ERROR.Printf("heating degree fit: %v", err)
		return profile
	}
	if !result.ok {
		return profile
	}

	rates, err := tempTariff.Rates()
	if err != nil || len(rates) == 0 {
		return profile
	}

	start := time.Now().Truncate(tariff.SlotDuration)
	res := slices.Clone(profile)
	for i := range res {
		rate, err := rates.At(start.Add(time.Duration(i) * tariff.SlotDuration))
		if err != nil {
			continue // no forecast for this slot: leave the historical average in place
		}

		correction := result.fit.adjust(rate.Value) / 1e3 // Wh -> kWh

		// cap the correction relative to the slot's own base value: an OLS slope driven by a
		// thin or skewed sample set, applied to a forecast temperature far outside the fit
		// window, must not be able to swing a slot to an arbitrary multiple of its historical
		// average. Bounding it to +/-100% of the base keeps the corrected value within
		// [0, 2x base] - generous enough for a real heating effect, not enough for the fit to
		// run away.
		correction = min(max(correction, -res[i]), res[i])

		res[i] = max(0, res[i]+correction)
	}

	return res
}
