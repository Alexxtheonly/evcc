package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var learnNow = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) // a Sunday

// commuterSessions builds a weekday-commuter history: plug in the evening,
// unplug next morning around 06:10, drive ~10 soc points, occasionally 20.
func commuterSessions(weeks int) Sessions {
	var res Sessions
	odo := 10000.0

	day := learnNow.AddDate(0, 0, -weeks*7)
	for week := range weeks {
		for wd := range 7 {
			d := day.AddDate(0, 0, wd)
			if wd == 0 || wd == 6 { // Sunday, Saturday: stays home
				continue
			}

			// disconnect in the morning with human week-to-week jitter
			dep := time.Date(d.Year(), d.Month(), d.Day(), 6, 5+wd+week%4, (13*wd+7*week)%60, 0, time.UTC)
			socEnd := 60.0
			used := 10.0
			if wd == 2 { // Tuesdays are long days
				used = 20.0
			}
			socStart := socEnd - used
			odoNext := odo + used*5

			res = append(res, Session{
				Created:      dep.Add(-10 * time.Hour),
				Finished:     dep.Add(-8 * time.Hour),
				Disconnected: &dep,
				Vehicle:      "car",
				SocEnd:       &socEnd,
				Odometer:     &odo,
			})
			// next session starts when the car returns in the evening
			res = append(res, Session{
				Created:  dep.Add(11 * time.Hour),
				Vehicle:  "car",
				SocStart: &socStart,
				Odometer: &odoNext,
			})
			odo = odoNext
		}
		day = day.AddDate(0, 0, 7)
	}

	return res
}

func TestLearnRepeatingPlansCommuter(t *testing.T) {
	plans := LearnRepeatingPlans(commuterSessions(8), learnNow)
	require.NotEmpty(t, plans)

	var days []int
	for _, p := range plans {
		days = append(days, p.Weekdays...)
		assert.True(t, p.Active)
		assert.NotEmpty(t, p.Tz)

		// ready-by must be at or before the earliest observed departures (06:05+)
		assert.LessOrEqual(t, p.Time, "06:15", "ready-by covers early departures")
		assert.GreaterOrEqual(t, p.Time, "05:00", "pooled times stay in the morning")

		for _, d := range p.Weekdays {
			if d == 2 {
				// Tuesday needs 20 soc + 20 reserve -> 40
				assert.Equal(t, 40, p.Soc, "long day gets the larger target")
			} else {
				// 10 soc + 20 reserve = 30 (floor)
				assert.Equal(t, 30, p.Soc)
			}
		}
	}

	// weekend days without departures get no plan
	assert.ElementsMatch(t, []int{1, 2, 3, 4, 5}, days)
}

func TestLearnRejectsAutomationArtifacts(t *testing.T) {
	sessions := commuterSessions(8)

	// an automation cycles the charger daily at exactly 04:30:00 with SoC drop
	// lookalikes; these must not poison the departure times
	for i := 0; i < 40; i++ {
		d := learnNow.AddDate(0, 0, -i)
		dep := time.Date(d.Year(), d.Month(), d.Day(), 4, 30, 0, 0, time.UTC)
		socEnd, socStart := 60.0, 50.0
		sessions = append(sessions, Session{
			Created:      dep.Add(-2 * time.Hour),
			Disconnected: &dep,
			Vehicle:      "car",
			SocEnd:       &socEnd,
		}, Session{
			Created:  dep.Add(time.Hour),
			Vehicle:  "car",
			SocStart: &socStart,
		})
	}

	plans := LearnRepeatingPlans(sessions, learnNow)
	for _, p := range plans {
		assert.NotEqual(t, "04:30", p.Time, "artifact cluster must not become a plan")
		assert.GreaterOrEqual(t, p.Time, "05:00")
	}
}

func TestLearnRequiresEvidenceOfDriving(t *testing.T) {
	var sessions Sessions

	// plug-outs with no SoC drop and no odometer movement: car never left
	for i := 0; i < 30; i++ {
		d := learnNow.AddDate(0, 0, -i)
		dep := time.Date(d.Year(), d.Month(), d.Day(), 8, i%50, i%40, 0, time.UTC)
		soc := 60.0
		odo := 10000.0
		sessions = append(sessions, Session{
			Created:      dep.Add(-2 * time.Hour),
			Disconnected: &dep,
			Vehicle:      "car",
			SocEnd:       &soc,
			Odometer:     &odo,
		}, Session{
			Created:  dep.Add(time.Hour),
			Vehicle:  "car",
			SocStart: &soc,
			Odometer: &odo,
		})
	}

	assert.Empty(t, LearnRepeatingPlans(sessions, learnNow), "no driving evidence, no plans")
}

func TestLearnRequiresMinimumHistory(t *testing.T) {
	assert.Empty(t, LearnRepeatingPlans(nil, learnNow))
	assert.Empty(t, LearnRepeatingPlans(commuterSessions(1), learnNow), "one week is not enough")
}

func TestLearnExpectedArrivalCommuter(t *testing.T) {
	arrival := LearnExpectedArrival(commuterSessions(8), learnNow)
	require.NotNil(t, arrival)

	// commuterSessions returns the car 11h after each ~06:05-06:32 departure, i.e.
	// ~17:05-17:32; the early (0.1) quantile of that must still land in the evening,
	// nowhere near midnight or the morning departure window.
	assert.GreaterOrEqual(t, arrival.TimeOfDay, 16*60, "evening arrival, not the morning departure")
	assert.LessOrEqual(t, arrival.TimeOfDay, 18*60, "evening arrival")

	// Tuesdays use 20 soc, every other weekday 10: the high (0.9) quantile must pick
	// up the long day rather than settle on the common case.
	assert.GreaterOrEqual(t, arrival.SocUsed, 18.0, "high quantile reflects the long day, not the typical one")
}

func TestLearnExpectedArrivalRejectsAutomationArtifacts(t *testing.T) {
	sessions := commuterSessions(8)

	// an automation reconnects the charger daily at exactly 05:30:00 - if this leaked
	// into the arrival sample, its early wall-clock time would drag the 0.1 quantile
	// out of the evening and into the middle of the night.
	for i := 0; i < 40; i++ {
		d := learnNow.AddDate(0, 0, -i)
		dep := time.Date(d.Year(), d.Month(), d.Day(), 4, 30, 0, 0, time.UTC)
		arr := time.Date(d.Year(), d.Month(), d.Day(), 5, 30, 0, 0, time.UTC)
		socEnd, socStart := 60.0, 50.0
		sessions = append(sessions, Session{
			Created:      dep.Add(-2 * time.Hour),
			Disconnected: &dep,
			Vehicle:      "car",
			SocEnd:       &socEnd,
		}, Session{
			Created:  arr,
			Vehicle:  "car",
			SocStart: &socStart,
		})
	}

	arrival := LearnExpectedArrival(sessions, learnNow)
	require.NotNil(t, arrival)
	assert.GreaterOrEqual(t, arrival.TimeOfDay, 16*60, "artifact cluster must not pull the estimate into the night")
}

func TestLearnExpectedArrivalRequiresDrivingEvidence(t *testing.T) {
	var sessions Sessions

	// plug-outs with no SoC drop and no odometer movement: car never left
	for i := 0; i < 30; i++ {
		d := learnNow.AddDate(0, 0, -i)
		dep := time.Date(d.Year(), d.Month(), d.Day(), 8, i%50, i%40, 0, time.UTC)
		soc := 60.0
		odo := 10000.0
		sessions = append(sessions, Session{
			Created:      dep.Add(-2 * time.Hour),
			Disconnected: &dep,
			Vehicle:      "car",
			SocEnd:       &soc,
			Odometer:     &odo,
		}, Session{
			Created:  dep.Add(time.Hour),
			Vehicle:  "car",
			SocStart: &soc,
			Odometer: &odo,
		})
	}

	assert.Nil(t, LearnExpectedArrival(sessions, learnNow), "no driving evidence, no prediction")
}

func TestLearnExpectedArrivalRequiresMinimumHistory(t *testing.T) {
	assert.Nil(t, LearnExpectedArrival(nil, learnNow))
	assert.Nil(t, LearnExpectedArrival(commuterSessions(1), learnNow), "one week is not enough")
}

// TestLearnExpectedArrivalHandlesMidnightWrap pins the fix for a vehicle that usually
// arrives around 23:30 but occasionally rolls past midnight to ~00:15: split at raw
// minutes-of-day, 00:15 sorts as the smallest value in the set and the early (0.1)
// quantile reports it directly - a day earlier than the cluster it actually belongs to.
func TestLearnExpectedArrivalHandlesMidnightWrap(t *testing.T) {
	var sessions Sessions
	day := learnNow.AddDate(0, 0, -30)

	for i := range 20 {
		d := day.AddDate(0, 0, i)
		dep := time.Date(d.Year(), d.Month(), d.Day(), 18, 0, i, 0, time.UTC)

		var arr time.Time
		if i%5 == 0 {
			// occasionally rolls past midnight
			arr = time.Date(d.Year(), d.Month(), d.Day()+1, 0, 15, i, 0, time.UTC)
		} else {
			arr = time.Date(d.Year(), d.Month(), d.Day(), 23, 30, i, 0, time.UTC)
		}

		socEnd, socStart := 60.0, 50.0
		sessions = append(sessions, Session{
			Created:      dep.Add(-8 * time.Hour),
			Disconnected: &dep,
			Vehicle:      "car",
			SocEnd:       &socEnd,
		}, Session{
			Created:  arr,
			Vehicle:  "car",
			SocStart: &socStart,
		})
	}

	arrival := LearnExpectedArrival(sessions, learnNow)
	require.NotNil(t, arrival)

	// the early edge of a cluster centered on 23:30 with occasional 00:15 rollovers must
	// stay in the 23:xx range - the wrap bug reported 00:15, a full day early
	assert.GreaterOrEqual(t, arrival.TimeOfDay, 23*60, "must stay in the 23:xx cluster, not wrap to just after midnight")
	assert.Less(t, arrival.TimeOfDay, 24*60)
}

func TestUnwrapMinutesOfDay(t *testing.T) {
	// cluster spanning midnight: 00:15 unwraps to 24:15 (1455) so it sorts after 23:30
	// instead of before it
	wrapped := []float64{15, 1410, 1410, 1425}
	got := unwrapMinutesOfDay(wrapped)
	assert.Equal(t, []float64{1410, 1410, 1425, 1455}, got)

	// no midnight crossing: the largest gap is the wrap itself, values pass through
	notWrapped := []float64{600, 610, 620}
	assert.Equal(t, notWrapped, unwrapMinutesOfDay(notWrapped))

	// fewer than 2 values: nothing to unwrap
	assert.Equal(t, []float64{700}, unwrapMinutesOfDay([]float64{700}))
	assert.Equal(t, []float64{}, unwrapMinutesOfDay([]float64{}))
}

// TestLearnExpectedArrivalRequiresEnoughSocSamples pins the energy estimate's own sample
// floor: departures() only requires SocEnd/SocStart to be present at all to contribute a
// socUsed value, so a history with plenty of arrival times but almost none of them
// carrying usable soc data must not fall back to trusting a single observation.
func TestLearnExpectedArrivalRequiresEnoughSocSamples(t *testing.T) {
	sessions := commuterSessions(8)

	// strip SocStart from every arrival pair but one: driving is still proven via
	// odometer movement (already present from commuterSessions), so departures() keeps
	// producing plenty of validated departures with plenty of arrival times - just almost
	// none of them carrying a usable socUsed.
	kept := false
	for i := range sessions {
		if sessions[i].SocStart == nil {
			continue
		}
		if !kept {
			kept = true
			continue
		}
		sessions[i].SocStart = nil
	}

	assert.Nil(t, LearnExpectedArrival(sessions, learnNow), "one soc sample is not enough to trust the estimate")
}

func TestLearnExpectedArrivalIgnoresSessionsOutsideWindow(t *testing.T) {
	old := commuterSessions(8)
	for i := range old {
		old[i].Created = old[i].Created.AddDate(-1, 0, 0)
		if old[i].Disconnected != nil {
			d := old[i].Disconnected.AddDate(-1, 0, 0)
			old[i].Disconnected = &d
		}
	}
	assert.Nil(t, LearnExpectedArrival(old, learnNow), "year-old history is ignored")
}

func TestLearnIgnoresSessionsOutsideWindow(t *testing.T) {
	old := commuterSessions(8)
	for i := range old {
		old[i].Created = old[i].Created.AddDate(-1, 0, 0)
		if old[i].Disconnected != nil {
			d := old[i].Disconnected.AddDate(-1, 0, 0)
			old[i].Disconnected = &d
		}
	}
	assert.Empty(t, LearnRepeatingPlans(old, learnNow), "year-old history is ignored")
}

func TestLearnSocCap(t *testing.T) {
	sessions := commuterSessions(8)
	for i := range sessions {
		if sessions[i].SocEnd != nil {
			socEnd := 95.0
			socStart := 5.0 // 90 points used
			sessions[i].SocEnd = &socEnd
			if i+1 < len(sessions) && sessions[i+1].SocStart != nil {
				sessions[i+1].SocStart = &socStart
			}
		}
	}

	for _, p := range LearnRepeatingPlans(sessions, learnNow) {
		assert.LessOrEqual(t, p.Soc, 80, "target capped")
	}
}
