package core

import (
	"reflect"
	"time"

	"github.com/evcc-io/evcc/core/session"
	"github.com/evcc-io/evcc/server/db"
)

// adaptivePlanLearnInterval paces re-learning adaptive plans from session
// history. Sessions accrue slowly; anything faster is wasted work.
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

// updateAdaptivePlans learns adaptive plans from session history for every
// vehicle with learning enabled. Plans are only re-written when they changed,
// so plan locks are not churned.
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

		plans := session.LearnRepeatingPlans(sessions, time.Now())

		current, _ := v.GetAdaptivePlans()
		if len(plans) == 0 && len(current) == 0 || reflect.DeepEqual(plans, current) {
			continue
		}

		if err := v.SetAdaptivePlans(plans); err != nil {
			site.log.ERROR.Printf("adaptive plans %s: %v", v.Name(), err)
			continue
		}

		site.log.DEBUG.Printf("adaptive plans %s: learned %d plans", v.Name(), len(plans))
	}
}
