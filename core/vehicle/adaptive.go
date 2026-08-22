package vehicle

import (
	"errors"
	"fmt"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/server/db/settings"
)

// AdaptivePlansValidity is how long adaptive plans stay trusted without being
// re-computed. One weekly cycle plus a day of slack: a learner that misses a
// day is still trusted, one that has been gone for a week is not.
const AdaptivePlansValidity = 8 * 24 * time.Hour

// adaptivePlanStore is the persisted set of adaptive repeating plans. The
// timestamp is always stamped server-side on write.
type adaptivePlanStore struct {
	Updated time.Time           `json:"updated"`
	Plans   []api.RepeatingPlan `json:"plans"`
}

// validateRepeatingPlans checks weekdays, timezone and time format
func validateRepeatingPlans(plans []api.RepeatingPlan) error {
	for _, plan := range plans {
		for _, day := range plan.Weekdays {
			if day < 0 || day > 6 {
				return fmt.Errorf("weekday out of range: %v", day)
			}
		}
		if _, err := time.LoadLocation(plan.Tz); err != nil {
			return fmt.Errorf("invalid timezone: %v", err)
		}
		if _, err := time.Parse("15:04", plan.Time); err != nil {
			return fmt.Errorf("invalid time: %v", err)
		}
	}

	return nil
}

// SetAdaptivePlans stores the adaptive repeating plans; nil clears them.
// The update timestamp is stamped here — a client-supplied clock must never
// decide plan freshness.
func (v *adapter) SetAdaptivePlans(plans []api.RepeatingPlan) error {
	if err := validateRepeatingPlans(plans); err != nil {
		return err
	}

	if len(plans) > 7 {
		return fmt.Errorf("too many adaptive plans: %d", len(plans))
	}

	for _, plan := range plans {
		if len(plan.Weekdays) == 0 {
			return errors.New("adaptive plan without weekdays")
		}
		if plan.Soc < 1 || plan.Soc > 100 {
			return fmt.Errorf("soc out of range: %d", plan.Soc)
		}
	}

	if err := settings.SetJson(v.key()+keys.AdaptivePlans, adaptivePlanStore{
		Updated: time.Now(),
		Plans:   plans,
	}); err != nil {
		return err
	}

	v.log.DEBUG.Printf("update adaptive plans for %s to: %v", v.name, plans)

	// note: could be optimized by only clearing plan lock of the relevant loadpoint
	v.clearPlanLocks()

	v.publish()

	return nil
}

// GetAdaptivePlans returns the stored adaptive plans and their update time
func (v *adapter) GetAdaptivePlans() ([]api.RepeatingPlan, time.Time) {
	var store adaptivePlanStore

	if err := settings.Json(v.key()+keys.AdaptivePlans, &store); err != nil {
		return nil, time.Time{}
	}

	return store.Plans, store.Updated
}

// GetEffectiveRepeatingPlans returns the plans charging should follow: user
// repeating plans when any are stored — including all-inactive ones, since a
// user who switched every plan off means "no repeating charging" — otherwise
// fresh adaptive plans, with targets capped at the vehicle's soc limit.
func (v *adapter) GetEffectiveRepeatingPlans() []api.RepeatingPlan {
	if plans := v.GetRepeatingPlans(); len(plans) > 0 {
		return plans
	}

	plans, updated := v.GetAdaptivePlans()
	if len(plans) == 0 || time.Since(updated) > AdaptivePlansValidity {
		return nil
	}

	// an active plan bypasses the loadpoint's LimitSocReached check, so the
	// user's soc limit must cap learned targets here
	if limit := v.GetLimitSoc(); limit > 0 {
		for i := range plans {
			plans[i].Soc = min(plans[i].Soc, limit)
		}
	}

	return plans
}
