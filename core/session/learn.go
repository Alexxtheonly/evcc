package session

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/evcc-io/evcc/api"
)

// Learning parameters. Departures are observed vehicle disconnects validated by
// a subsequent SoC drop or odometer increase, so charger automations that cycle
// the connection without the vehicle leaving do not count.
const (
	learnWindow             = 182 * 24 * time.Hour // observation window
	learnMinDepartures      = 10                   // total validated departures required
	learnMinWeekdaySamples  = 4                    // per-weekday samples below this use pooled times
	learnReservePct         = 20                   // soc to still have when returning
	learnSocFloor           = 30                   // never plan below
	learnSocCap             = 80                   // never plan above
	learnP90Probability     = 0.5                  // below this departure probability, plan the median day
	learnMinSocDrop         = 3.0                  // soc points that prove the vehicle drove
	learnMinOdometerKm      = 1.0                  // odometer km that prove the vehicle drove
	learnArtifactClusterLen = 5                    // identical wall-clock seconds marking an automation
)

// departure is a validated vehicle departure
type departure struct {
	at      time.Time
	socUsed float64 // soc points consumed until the next session, negative when unknown
}

// quantile returns the linear-interpolation quantile of sorted values
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := q * float64(len(sorted)-1)
	lo := int(pos)
	hi := min(lo+1, len(sorted)-1)
	frac := pos - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

// departures extracts validated departures from chronological sessions.
// A disconnect only counts when the next session shows the vehicle actually
// drove, and disconnect times repeating on the exact same wall-clock second
// are treated as automation artifacts.
func departures(sessions Sessions, now time.Time) []departure {
	clusters := make(map[string]int)
	for _, s := range sessions {
		if s.Disconnected != nil {
			clusters[s.Disconnected.Format("15:04:05")]++
		}
	}

	var res []departure
	for i := range len(sessions) - 1 {
		s, next := sessions[i], sessions[i+1]

		if s.Disconnected == nil || now.Sub(*s.Disconnected) > learnWindow {
			continue
		}
		if clusters[s.Disconnected.Format("15:04:05")] >= learnArtifactClusterLen {
			continue
		}

		socDrop := s.SocEnd != nil && next.SocStart != nil && *s.SocEnd-*next.SocStart >= learnMinSocDrop
		odoMoved := s.Odometer != nil && next.Odometer != nil && *next.Odometer-*s.Odometer >= learnMinOdometerKm
		if !socDrop && !odoMoved {
			continue
		}

		d := departure{at: *s.Disconnected, socUsed: -1}
		if s.SocEnd != nil && next.SocStart != nil {
			d.socUsed = max(*s.SocEnd-*next.SocStart, 0)
		}
		res = append(res, d)
	}

	return res
}

// weekdayCount returns how often each weekday occurs in [from, to]
func weekdayCount(from, to time.Time) [7]int {
	var res [7]int
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		res[int(d.Weekday())]++
	}
	return res
}

// LearnRepeatingPlans derives per-weekday repeating charge plans from the
// vehicle's session history: ready-by is an early quantile of the observed
// departure time, the target covers the observed consumption plus a reserve.
// Returns nil when the history does not support a confident plan.
func LearnRepeatingPlans(sessions Sessions, now time.Time) []api.RepeatingPlan {
	deps := departures(sessions, now)
	if len(deps) < learnMinDepartures {
		return nil
	}

	byDay := make(map[int][]departure)
	var pooledTimes []float64
	first, last := deps[0].at, deps[0].at
	for _, d := range deps {
		day := int(d.at.Weekday())
		byDay[day] = append(byDay[day], d)
		pooledTimes = append(pooledTimes, minutesOfDay(d.at))
		if d.at.Before(first) {
			first = d.at
		}
		if d.at.After(last) {
			last = d.at
		}
	}
	slices.Sort(pooledTimes)

	var pooledSoc []float64
	for _, d := range deps {
		if d.socUsed >= 0 {
			pooledSoc = append(pooledSoc, d.socUsed)
		}
	}
	slices.Sort(pooledSoc)

	counts := weekdayCount(first, last)
	tz := now.Location().String()
	if tz == "" {
		tz = "Local"
	}

	type slot struct{ time, soc int }
	perDay := make(map[int]slot)

	for day, dd := range byDay {
		times := make([]float64, 0, len(dd))
		soc := make([]float64, 0, len(dd))
		for _, d := range dd {
			times = append(times, minutesOfDay(d.at))
			if d.socUsed >= 0 {
				soc = append(soc, d.socUsed)
			}
		}
		slices.Sort(times)
		slices.Sort(soc)

		if len(times) < learnMinWeekdaySamples {
			times = pooledTimes
		}
		if len(soc) == 0 {
			soc = pooledSoc
		}
		if len(soc) == 0 {
			continue
		}

		q := 0.9
		if counts[day] > 0 && float64(len(dd))/float64(counts[day]) < learnP90Probability {
			q = 0.5
		}

		target := learnReservePct + quantile(soc, q)
		target = max(target, learnSocFloor)
		target = min(target, learnSocCap)

		ready := int(quantile(times, 0.1))
		ready -= ready % 15

		perDay[day] = slot{time: ready, soc: 5 * int(math.Round(target/5))}
	}

	// merge weekdays sharing time and target into one plan
	groups := make(map[slot][]int)
	for day, s := range perDay {
		groups[s] = append(groups[s], day)
	}

	keys := make([]slot, 0, len(groups))
	for s := range groups {
		keys = append(keys, s)
	}
	slices.SortFunc(keys, func(a, b slot) int {
		if a.time != b.time {
			return a.time - b.time
		}
		return a.soc - b.soc
	})

	var plans []api.RepeatingPlan
	for _, s := range keys {
		days := groups[s]
		slices.Sort(days)
		plans = append(plans, api.RepeatingPlan{
			Weekdays: days,
			Time:     fmt.Sprintf("%02d:%02d", s.time/60, s.time%60),
			Tz:       tz,
			Soc:      s.soc,
			Active:   true,
		})
	}

	return plans
}

func minutesOfDay(t time.Time) float64 {
	return float64(t.Hour()*60+t.Minute()) + float64(t.Second())/60
}
