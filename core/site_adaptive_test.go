package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/session"
	"github.com/evcc-io/evcc/core/vehicle"
	"github.com/evcc-io/evcc/util"
	"go.uber.org/mock/gomock"
)

// commuterSessions builds a minimal weekday commuter history: disconnect every weekday
// morning, reconnect in the evening, enough weeks for LearnRepeatingPlans to produce a
// confident weekday plan (mirrors core/session's own commuterSessions fixture, which
// isn't exported).
func commuterSessions(weeks int, now time.Time) session.Sessions {
	var res session.Sessions

	day := now.AddDate(0, 0, -weeks*7)
	for week := range weeks {
		for wd := range 7 {
			d := day.AddDate(0, 0, wd)
			if wd == 0 || wd == 6 { // Sunday, Saturday: stays home
				continue
			}

			dep := time.Date(d.Year(), d.Month(), d.Day(), 6, 5+wd+week%4, (13*wd+7*week)%60, 0, time.UTC)
			socEnd, socStart := 60.0, 50.0

			res = append(res,
				session.Session{Created: dep.Add(-10 * time.Hour), Disconnected: &dep, Vehicle: "car", SocEnd: &socEnd},
				session.Session{Created: dep.Add(11 * time.Hour), Vehicle: "car", SocStart: &socStart},
			)
		}
		day = day.AddDate(0, 0, 7)
	}

	return res
}

// TestUpdateAdaptivePlanSkipsUnchanged ensures a re-learned set of plans identical to
// what's already stored, and freshly stamped, does not trigger a write.
func TestUpdateAdaptivePlanSkipsUnchanged(t *testing.T) {
	site := &Site{log: util.NewLogger("foo")}
	ctrl := gomock.NewController(t)
	v := vehicle.NewMockAPI(ctrl)

	sessions := commuterSessions(8, time.Now())
	learned := session.LearnRepeatingPlans(sessions, time.Now())
	if len(learned) == 0 {
		t.Fatal("fixture must produce at least one plan")
	}

	v.EXPECT().Name().Return("car").AnyTimes()
	v.EXPECT().GetAdaptivePlans().Return(learned, time.Now())
	// no SetAdaptivePlans expectation: any call fails the test

	site.updateAdaptivePlan(v, sessions)
}

// TestUpdateAdaptivePlanRefreshesStableButAgingPlan: SetAdaptivePlans is the only
// place that stamps Updated, so skipping the write purely because the plans haven't
// changed would let a stable, still-correct weekday commute expire out of
// GetEffectiveRepeatingPlans (core/vehicle/adaptive.go) once it crosses
// vehicle.AdaptivePlansValidity - the exact repeating case the feature exists for.
func TestUpdateAdaptivePlanRefreshesStableButAgingPlan(t *testing.T) {
	site := &Site{log: util.NewLogger("foo")}
	ctrl := gomock.NewController(t)
	v := vehicle.NewMockAPI(ctrl)

	sessions := commuterSessions(8, time.Now())
	learned := session.LearnRepeatingPlans(sessions, time.Now())
	if len(learned) == 0 {
		t.Fatal("fixture must produce at least one plan")
	}

	v.EXPECT().Name().Return("car").AnyTimes()
	// stored well past half of AdaptivePlansValidity, but still the same plans the
	// learner would produce again today
	v.EXPECT().GetAdaptivePlans().Return(learned, time.Now().Add(-vehicle.AdaptivePlansValidity/2-time.Hour))
	v.EXPECT().SetAdaptivePlans(learned)

	site.updateAdaptivePlan(v, sessions)
}
