package session

import (
	"slices"
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

// departuresAtMinutes builds one validated departure per day, at the given minute of day,
// newest first. Times are exact minutes with zero seconds: departures() screens clusters of
// five or more identical wall-clock seconds as automation artifacts, and every entry here
// has a distinct minute, so nothing is screened.
func departuresAtMinutes(mins []int) Sessions {
	var res Sessions
	for i, m := range mins {
		d := learnNow.AddDate(0, 0, -i-1)
		dep := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC).Add(time.Duration(m) * time.Minute)
		socEnd, socStart := 80.0, 60.0
		res = append(res,
			Session{Created: dep.Add(-6 * time.Hour), Disconnected: &dep, Vehicle: "car", SocEnd: &socEnd},
			Session{Created: dep.Add(8 * time.Hour), Vehicle: "car", SocStart: &socStart},
		)
	}
	return res
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

// TestLearnRepeatingPlansMidnightWrap pins the fix for a vehicle whose departures straddle
// midnight. quantile interpolates linearly, so a weekday bucket holding values from both
// sides of the 0/1440 seam is bimodal and the interpolated 0.1 quantile lands in the empty
// middle of the day - a ready-by time the vehicle has never once departed at. Without the
// unwrap this fixture produces plans at 00:00, 09:15 and 11:45 for a driver who always
// leaves within half an hour of midnight.
//
// 40 consecutive days is deliberate: it puts five or six samples in every weekday bucket,
// clearing learnMinWeekdaySamples so the per-weekday path is used rather than the pooled
// fallback. That per-weekday path is where the fabrication happens.
func TestLearnRepeatingPlansMidnightWrap(t *testing.T) {
	var mins []int
	for m := 1410; m <= 1439; m++ { // 23:30 .. 23:59
		mins = append(mins, m)
	}
	for m := range 10 { // 00:00 .. 00:09
		mins = append(mins, m)
	}

	plans := LearnRepeatingPlans(departuresAtMinutes(mins), learnNow)
	require.Len(t, plans, 1, "one habit must produce one plan, not one per side of midnight")

	// unwrapped, the 40 values are the contiguous run 1410..1449. quantile(0.1) takes
	// pos = 0.1*39 = 3.9, i.e. 0.1*res[3] + 0.9*res[4] = 0.1*1413 + 0.9*1414 = 1413.9,
	// truncated to 1413, %1440 unchanged, floored to the quarter hour = 1410 = 23:30
	assert.Equal(t, "23:30", plans[0].Time)

	for _, p := range plans {
		require.NotEmpty(t, p.Weekdays)

		hhmm, err := time.Parse("15:04", p.Time)
		require.NoError(t, err, "learned time must be a valid time of day")

		// the fabrication guard: every observed departure is within 30min of midnight,
		// so a ready-by anywhere in the daytime is interpolation across the seam, not
		// an observation
		mod := hhmm.Hour()*60 + hhmm.Minute()
		assert.True(t, mod >= 1380 || mod <= 60,
			"ready-by %s is in the empty middle of the day - no departure was ever observed there", p.Time)
	}
}

// TestLearnRepeatingPlansMidnightBoundary covers the %1440 fold. Ten departures just after
// midnight plus one just before means the unwrap re-bases the ten, putting the 0.1 quantile
// exactly on 1440. Without the fold, ready formats as "24:00", which time.Parse("15:04")
// rejects with "hour out of range" - so validateRepeatingPlans (core/vehicle/adaptive.go)
// would refuse to store the plan and the learner would silently never persist anything.
func TestLearnRepeatingPlansMidnightBoundary(t *testing.T) {
	plans := LearnRepeatingPlans(departuresAtMinutes([]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 1439}), learnNow)
	require.Len(t, plans, 1)

	// unwrapped: {1439} plus 1440..1449. quantile(0.1) takes pos = 0.1*10 = 1.0 exactly,
	// i.e. res[1] = 1440 - which %1440 folds back to 0
	assert.Equal(t, "00:00", plans[0].Time)

	_, err := time.Parse("15:04", plans[0].Time)
	assert.NoError(t, err)
}

// TestUnwrapIsNoopForMorningCommuter is the claim that makes the unwrap safe to apply
// unconditionally: for any history that does not straddle midnight the largest gap on the
// 24h circle IS the wrap, so gapIdx stays -1 and the input is returned unchanged. Everyone
// who is not a night-shift driver sees byte-for-byte identical times, and
// TestLearnRepeatingPlansCommuter's expectations above are unaffected for that reason.
func TestUnwrapIsNoopForMorningCommuter(t *testing.T) {
	var times []float64
	for _, d := range departures(commuterSessions(8), learnNow) {
		times = append(times, minutesOfDay(d.at))
	}
	slices.Sort(times)
	require.NotEmpty(t, times)

	assert.Equal(t, times, unwrapMinutesOfDay(times), "a morning cluster must pass through untouched")
}
