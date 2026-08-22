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
