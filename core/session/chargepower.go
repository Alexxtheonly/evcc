package session

import "slices"

// chargeTaperMinSessions mirrors socGradientMinSessions: a handful of qualifying
// sessions before trusting a derived charge-power prior, so one atypical session
// (a throttled tariff-limited PV-only charge, a temporary breaker limit) can't
// dominate the estimate.
const chargeTaperMinSessions = 3

// chargeTaperMinSwing mirrors socGradientMinSocSwing: a session must cover a
// meaningful soc range for its average power to be a useful plateau or tail
// sample rather than noise from a very short top-up.
const chargeTaperMinSwing = 10.0

// chargeTaperKneeSoc buckets sessions into "stayed below the taper knee"
// (plateau sample) vs "started at or above it" (tail sample). It intentionally
// reuses the generic default knee soc (see core/soc.Estimator) rather than
// trying to locate the vehicle's actual knee from session aggregates alone -
// start/end soc, energy and duration do not carry enough resolution to tell
// "power decayed partway through this session" from "power was steady but
// lower the whole time," so only the two power endpoints are learned; the
// knee position stays the generic default.
const chargeTaperKneeSoc = 50.0

// sessionAvgPower returns a session's average charge power in W, and whether
// it has the fields and soc swing required to be a useful sample at all.
func sessionAvgPower(s Session) (float64, bool) {
	if s.SocStart == nil || s.SocEnd == nil || s.ChargeDuration == nil || *s.ChargeDuration <= 0 || s.ChargedEnergy <= 0 {
		return 0, false
	}
	if *s.SocEnd-*s.SocStart < chargeTaperMinSwing {
		return 0, false
	}
	return s.ChargedEnergy * 1e3 / s.ChargeDuration.Hours(), true
}

// PriorChargeTaper derives a vehicle's plateau (max) and tail (min) charge
// power (W) from session history, for seeding a fresh estimator before any
// live session has taught it anything.
//
// Sessions that ended at or below chargeTaperKneeSoc never reached the point
// where a vehicle tapers, so their average power approximates the plateau
// rate; sessions that started at or above it are "topping off" already in the
// taper zone, so their average power approximates the tail rate. Sessions
// that span the knee contribute to neither bucket - their average blends both
// regimes and would bias whichever bucket it landed in. Each bucket is the
// median across its qualifying sessions rather than the mean, matching
// PriorSocGradient's reasoning: one unusual session should nudge the
// estimate, not dominate it.
//
// ok is false unless both buckets independently clear chargeTaperMinSessions
// - a vehicle that is always unplugged well before topping off, or always
// plugged in already near full, never accumulates the other bucket and the
// generic curve is safer than half a learned taper.
//
// The caller is responsible for sanity-bounding the result (see
// core/soc.PlausibleChargeTaper) before trusting it.
func PriorChargeTaper(sessions Sessions) (minPower, maxPower float64, ok bool) {
	var plateau, tail []float64

	for _, s := range sessions {
		power, qualifies := sessionAvgPower(s)
		if !qualifies {
			continue
		}

		switch {
		case *s.SocEnd <= chargeTaperKneeSoc:
			plateau = append(plateau, power)
		case *s.SocStart >= chargeTaperKneeSoc:
			tail = append(tail, power)
		}
	}

	if len(plateau) < chargeTaperMinSessions || len(tail) < chargeTaperMinSessions {
		return 0, 0, false
	}

	slices.Sort(plateau)
	slices.Sort(tail)
	return quantile(tail, 0.5), quantile(plateau, 0.5), true
}
