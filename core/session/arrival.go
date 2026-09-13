package session

import (
	"slices"
	"time"
)

type ArrivalEstimate struct {
	Time    time.Time
	Soc     float64
	Samples int
}

// ExpectedArrival uses validated past trips and never interprets a missing SoC as empty.
func ExpectedArrival(history Sessions, now, horizon time.Time) *ArrivalEstimate {
	known := make(Sessions, 0, len(history))
	for _, s := range history {
		if s.Created.Before(now) {
			known = append(known, s)
		}
	}
	slices.SortFunc(known, func(a, b Session) int { return a.Created.Compare(b.Created) })
	deps := departures(known, now)
	if len(deps) < learnMinDepartures || len(known) == 0 || now.Sub(known[len(known)-1].Created) > 30*24*time.Hour {
		return nil
	}
	for day := 0; day < 4; day++ {
		date := now.AddDate(0, 0, day)
		var minutes, soc []float64
		for _, dep := range deps {
			arrival := dep.arrivedAt.In(now.Location())
			if arrival.Weekday() != date.Weekday() {
				continue
			}
			for _, s := range known {
				if s.Created.Equal(dep.arrivedAt) && s.SocStart != nil && *s.SocStart >= 0 && *s.SocStart <= 100 {
					minutes = append(minutes, minutesOfDay(arrival))
					soc = append(soc, *s.SocStart)
					break
				}
			}
		}
		if len(minutes) < learnMinWeekdaySamples {
			continue
		}
		slices.Sort(minutes)
		slices.Sort(soc)
		minute := int(quantile(unwrapMinutesOfDay(minutes), 0.8)) % 1440
		arrival := time.Date(date.Year(), date.Month(), date.Day(), minute/60, minute%60, 0, 0, date.Location())
		if arrival.After(now) && arrival.Before(horizon) {
			return &ArrivalEstimate{Time: arrival, Soc: quantile(soc, 0.5), Samples: len(soc)}
		}
	}
	return nil
}
