package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/session"
	"github.com/evcc-io/evcc/core/vehicle"
	"github.com/evcc-io/evcc/util"
	"go.uber.org/mock/gomock"
)

// arrivalSessions builds a minimal history with enough validated departure/arrival pairs
// (disconnect + a later reconnect showing a soc drop) for LearnExpectedArrival to produce a
// confident prediction: reconnects cluster around 18:00 with ~15 soc points used.
func arrivalSessions(n int, now time.Time) session.Sessions {
	var res session.Sessions
	for i := range n {
		d := now.AddDate(0, 0, -i)
		dep := time.Date(d.Year(), d.Month(), d.Day(), 8, i%10, i%50, 0, time.UTC)
		arr := time.Date(d.Year(), d.Month(), d.Day(), 18, i%10, i%50, 0, time.UTC)
		socEnd, socStart := 80.0, 65.0

		res = append(res,
			session.Session{Created: dep.Add(-time.Hour), Disconnected: &dep, Vehicle: "car", SocEnd: &socEnd},
			session.Session{Created: arr, Vehicle: "car", SocStart: &socStart},
		)
	}
	return res
}

// TestUpdateExpectedArrivalClearsOnNoHistory ensures a vehicle whose history no longer
// supports a confident prediction has its stored one cleared rather than left stale -
// otherwise a vehicle that stops driving regularly (or is sold/replaced) would keep
// influencing the optimizer with a prediction the data no longer backs.
func TestUpdateExpectedArrivalClearsOnNoHistory(t *testing.T) {
	site := &Site{log: util.NewLogger("foo")}
	ctrl := gomock.NewController(t)
	v := vehicle.NewMockAPI(ctrl)

	v.EXPECT().Name().Return("car").AnyTimes()
	v.EXPECT().GetExpectedArrival().Return(session.ExpectedArrival{TimeOfDay: 1080, SocUsed: 30}, time.Now())
	v.EXPECT().SetExpectedArrival(session.ExpectedArrival{})

	site.updateExpectedArrival(v, nil) // no sessions at all
}

// TestUpdateExpectedArrivalStoresConfidentPrediction ensures a change is written when the
// learner produces a prediction that differs from what's stored.
func TestUpdateExpectedArrivalStoresConfidentPrediction(t *testing.T) {
	site := &Site{log: util.NewLogger("foo")}
	ctrl := gomock.NewController(t)
	v := vehicle.NewMockAPI(ctrl)

	sessions := arrivalSessions(12, time.Now())

	v.EXPECT().Name().Return("car").AnyTimes()
	v.EXPECT().GetExpectedArrival().Return(session.ExpectedArrival{}, time.Time{})
	v.EXPECT().SetExpectedArrival(gomock.Not(session.ExpectedArrival{}))

	site.updateExpectedArrival(v, sessions)
}

// TestUpdateExpectedArrivalSkipsUnchanged ensures a re-learned prediction identical to
// what's already stored does not trigger a write, keeping this consistent with how
// updateAdaptivePlan avoids churn.
func TestUpdateExpectedArrivalSkipsUnchanged(t *testing.T) {
	site := &Site{log: util.NewLogger("foo")}
	ctrl := gomock.NewController(t)
	v := vehicle.NewMockAPI(ctrl)

	sessions := arrivalSessions(12, time.Now())
	learned := session.LearnExpectedArrival(sessions, time.Now())
	if learned == nil {
		t.Fatal("fixture must produce a confident prediction")
	}

	v.EXPECT().Name().Return("car").AnyTimes()
	v.EXPECT().GetExpectedArrival().Return(*learned, time.Now())
	// no SetExpectedArrival expectation: any call fails the test

	site.updateExpectedArrival(v, sessions)
}

// TestUpdateExpectedArrivalRefreshesStableButAgingPrediction ensures an unchanged
// prediction still gets re-written once its stored timestamp is old enough to risk
// crossing vehicle.AdaptivePlansValidity before the next learn cycle. SetExpectedArrival
// is the only place that stamps Updated, so skipping the write purely because the value
// hasn't changed would let a routine, still-correct prediction expire out from under
// itself - the exact case this feature exists for.
func TestUpdateExpectedArrivalRefreshesStableButAgingPrediction(t *testing.T) {
	site := &Site{log: util.NewLogger("foo")}
	ctrl := gomock.NewController(t)
	v := vehicle.NewMockAPI(ctrl)

	sessions := arrivalSessions(12, time.Now())
	learned := session.LearnExpectedArrival(sessions, time.Now())
	if learned == nil {
		t.Fatal("fixture must produce a confident prediction")
	}

	v.EXPECT().Name().Return("car").AnyTimes()
	// stored well past half of AdaptivePlansValidity, but still the same value the
	// learner would produce again today
	v.EXPECT().GetExpectedArrival().Return(*learned, time.Now().Add(-vehicle.AdaptivePlansValidity/2-time.Hour))
	v.EXPECT().SetExpectedArrival(*learned)

	site.updateExpectedArrival(v, sessions)
}
