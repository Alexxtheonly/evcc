package core

import (
	"reflect"
	"time"

	"github.com/evcc-io/evcc/core/session"
	"github.com/evcc-io/evcc/core/vehicle"
	"github.com/evcc-io/evcc/server/db"
)

// adaptivePlanLearnInterval paces re-learning adaptive plans from session history.
// Sessions accrue slowly; anything faster is wasted work.
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

// updateAdaptivePlans learns adaptive plans from session history for every vehicle with
// learning enabled. Results are only re-written when they changed, so plan locks are not
// churned.
func (site *Site) updateAdaptivePlans() {
	if db.Instance == nil {
		return
	}

	for _, v := range site.Vehicles().Settings() {
		if !v.GetAdaptivePlanLearning() {
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

		site.updateAdaptivePlan(v, sessions)
	}
}

// updateAdaptivePlan re-learns and stores a single vehicle's adaptive repeating plans
func (site *Site) updateAdaptivePlan(v vehicle.API, sessions session.Sessions) {
	plans := session.LearnRepeatingPlans(sessions, time.Now())

	current, updated := v.GetAdaptivePlans()
	// SetAdaptivePlans is the only place that stamps Updated, and
	// GetEffectiveRepeatingPlans distrusts a stored plan once it is older than
	// vehicle.AdaptivePlansValidity - so a stable, still-correct plan that keeps
	// re-learning the same value would never get re-stamped and would silently go
	// stale after that window, exactly the repeating case this feature targets.
	// Forcing a re-write once the stored copy is comfortably past half that window
	// keeps the original goal (skip writes/publishes for no reason) while
	// guaranteeing it never actually expires out from under a still-valid plan.
	unchanged := len(plans) == 0 && len(current) == 0 || reflect.DeepEqual(plans, current)
	if unchanged && time.Since(updated) < vehicle.AdaptivePlansValidity/2 {
		return
	}

	if err := v.SetAdaptivePlans(plans); err != nil {
		site.log.ERROR.Printf("adaptive plans %s: %v", v.Name(), err)
		return
	}

	site.log.DEBUG.Printf("adaptive plans %s: learned %d plans", v.Name(), len(plans))
}
