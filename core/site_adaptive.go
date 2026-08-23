package core

import (
	"reflect"
	"time"

	"github.com/evcc-io/evcc/core/session"
	"github.com/evcc-io/evcc/core/vehicle"
	"github.com/evcc-io/evcc/server/db"
)

// adaptivePlanLearnInterval paces re-learning adaptive plans (and, on the same cycle,
// expected arrivals) from session history. Sessions accrue slowly; anything faster is
// wasted work.
const adaptivePlanLearnInterval = 6 * time.Hour

// updateAdaptivePlansAsync re-learns adaptive plans when due
func (site *Site) updateAdaptivePlansAsync() {
	site.Lock()
	if time.Since(site.adaptivePlansUpdated) < adaptivePlanLearnInterval {
		site.Unlock()
		return
	}
	site.adaptivePlansUpdated = time.Now()
	site.Unlock()

	go site.updateAdaptivePlans()
}

// updateAdaptivePlans learns adaptive plans and expected arrivals from session history
// for every vehicle with the respective learning enabled - the two are independent
// per-vehicle opt-ins, but share one session fetch per vehicle since both read the same
// history. Results are only re-written when they changed, so plan locks are not churned
// and a vehicle without expected-arrival learning enabled is untouched.
func (site *Site) updateAdaptivePlans() {
	if db.Instance == nil {
		return
	}

	for _, v := range site.Vehicles().Settings() {
		learnPlans := v.GetAdaptivePlanLearning()
		learnArrival := v.GetExpectedArrivalLearning()
		if !learnPlans && !learnArrival {
			continue
		}

		instance := v.Instance()
		if instance == nil {
			continue
		}

		sessions, err := session.VehicleSessions(db.Instance, instance.GetTitle())
		if err != nil {
			site.log.ERROR.Printf("adaptive plans %s: %v", v.Name(), err)
			continue
		}

		if learnPlans {
			site.updateAdaptivePlan(v, sessions)
		}
		if learnArrival {
			site.updateExpectedArrival(v, sessions)
		}
	}
}

// updateAdaptivePlan re-learns and stores a single vehicle's adaptive repeating plans
func (site *Site) updateAdaptivePlan(v vehicle.API, sessions session.Sessions) {
	plans := session.LearnRepeatingPlans(sessions, time.Now())

	current, _ := v.GetAdaptivePlans()
	if len(plans) == 0 && len(current) == 0 || reflect.DeepEqual(plans, current) {
		return
	}

	if err := v.SetAdaptivePlans(plans); err != nil {
		site.log.ERROR.Printf("adaptive plans %s: %v", v.Name(), err)
		return
	}

	site.log.DEBUG.Printf("adaptive plans %s: learned %d plans", v.Name(), len(plans))
}

// updateExpectedArrival re-learns and stores a single vehicle's expected-arrival
// prediction. A nil result (not enough history, or history that no longer supports a
// confident prediction) clears any previously stored one rather than leaving it stale.
func (site *Site) updateExpectedArrival(v vehicle.API, sessions session.Sessions) {
	var next session.ExpectedArrival
	if learned := session.LearnExpectedArrival(sessions, time.Now()); learned != nil {
		next = *learned
	}

	current, updated := v.GetExpectedArrival()
	// SetExpectedArrival is the only place that stamps Updated, and
	// expectedArrivalDemand distrusts a prediction once it is older than
	// vehicle.AdaptivePlansValidity - so a stable, still-correct prediction that keeps
	// re-learning the same value would never get re-stamped and would silently go stale
	// after that window, exactly the routine, repeating case this feature targets. Forcing
	// a re-write once the stored copy is comfortably past half that window keeps the
	// original goal (skip writes/publishes for no reason) while guaranteeing it never
	// actually expires out from under a still-valid prediction.
	if next == current && time.Since(updated) < vehicle.AdaptivePlansValidity/2 {
		return
	}

	if err := v.SetExpectedArrival(next); err != nil {
		site.log.ERROR.Printf("expected arrival %s: %v", v.Name(), err)
		return
	}

	if next == (session.ExpectedArrival{}) {
		site.log.DEBUG.Printf("expected arrival %s: cleared, no confident prediction", v.Name())
	} else {
		site.log.DEBUG.Printf("expected arrival %s: learned %02d:%02d, %.0f soc", v.Name(), next.TimeOfDay/60, next.TimeOfDay%60, next.SocUsed)
	}
}
