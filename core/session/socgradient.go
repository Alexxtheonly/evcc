package session

import "slices"

// socGradientMinSessions is the minimum number of qualifying sessions required before a
// historical soc gradient prior is trusted. Below this, a couple of unusual sessions could
// easily dominate the estimate.
const socGradientMinSessions = 3

// socGradientMinSocSwing is the minimum observed soc change (percentage points) for a
// session to qualify - mirrors the live estimator's own recalculation threshold
// (core/soc.Estimator.Soc), so historical and live learning apply the same bar.
const socGradientMinSocSwing = 10.0

// PriorSocGradient derives a prior for a vehicle's energy-per-soc-step (Wh) from session
// history, for seeding a fresh estimator before any live session has taught it anything. It
// is the median across qualifying sessions rather than the mean, so a single unusual session
// (a partial charge, a meter glitch) cannot dominate the estimate the way an average would.
//
// The caller is responsible for sanity-bounding the result against the vehicle's configured
// capacity (see core/soc.PlausibleEnergyPerSocStep) before trusting it - this function only
// summarizes the history, it does not know what a plausible vehicle looks like.
func PriorSocGradient(sessions Sessions) (float64, bool) {
	var gradients []float64

	for _, s := range sessions {
		if s.SocStart == nil || s.SocEnd == nil || s.ChargedEnergy <= 0 {
			continue
		}

		swing := *s.SocEnd - *s.SocStart
		if swing < socGradientMinSocSwing {
			continue
		}

		gradients = append(gradients, s.ChargedEnergy*1e3/swing) // kWh -> Wh
	}

	if len(gradients) < socGradientMinSessions {
		return 0, false
	}

	slices.Sort(gradients)
	return quantile(gradients, 0.5), true
}
