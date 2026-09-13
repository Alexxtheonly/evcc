package core

import (
	"fmt"
	"slices"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/session"
	"github.com/evcc-io/evcc/core/vehicle"
	"github.com/evcc-io/evcc/server/db"
	optimizer "github.com/evcc-io/optimizer/client"
)

func expectedPlanGoal(v vehicle.API, after, horizon time.Time) (time.Time, int) {
	if at, soc := v.GetPlanSoc(); at.After(time.Now()) && soc > 0 {
		return at, soc
	}
	var selected time.Time
	var target int
	for _, plan := range v.GetEffectiveRepeatingPlans() {
		if !plan.Active || plan.Soc <= 0 {
			continue
		}
		loc, err := time.LoadLocation(plan.Tz)
		if err != nil {
			continue
		}
		clock, err := time.Parse("15:04", plan.Time)
		if err != nil {
			continue
		}
		for day := 0; day < 5; day++ {
			date := after.In(loc).AddDate(0, 0, day)
			if !slices.Contains(plan.Weekdays, int(date.Weekday())) {
				continue
			}
			at := time.Date(date.Year(), date.Month(), date.Day(), clock.Hour(), clock.Minute(), 0, 0, loc)
			if at.After(after) && at.Before(horizon) && (selected.IsZero() || at.Before(selected)) {
				selected, target = at, plan.Soc
			}
		}
	}
	return selected, target
}

func expectedVehicleBattery(capacity, soc, minPower, maxPower float64, goal int, arrival, departure time.Time, starts []time.Time, dt []int) (energyBattery, bool) {
	if capacity <= 0 || maxPower <= 0 || soc < 0 || soc > 100 || goal <= 0 || goal > 100 || !departure.After(arrival) {
		return energyBattery{}, false
	}
	bat := energyBattery{BatteryConfig: optimizer.BatteryConfig{
		SCapacity: float32(capacity * 1000), SInitial: float32(capacity * soc * 10),
		SMax: float32(capacity * max(soc, float64(goal)) * 10), CMin: float32(minPower), CMax: float32(maxPower),
		ChargeFromGrid: true,
	}, Availability: make([]bool, len(starts))}
	last := -1
	for i, start := range starts {
		end := start.Add(time.Duration(dt[i]) * time.Second)
		bat.Availability[i] = !start.Before(arrival) && !end.After(departure)
		if bat.Availability[i] {
			last = i
		}
	}
	if last < 0 {
		return bat, false
	}
	bat.SGoal = make([]float32, len(starts))
	bat.SGoal[last] = float32(capacity * float64(goal) * 10)
	return bat, true
}

func (site *Site) addExpectedVehicles(req *energyRequest, details *requestDetails) error {
	if len(details.Timestamps) == 0 {
		return nil
	}
	stamp := time.Now()
	last := len(details.Timestamps) - 1
	horizon := details.Timestamps[last].Add(time.Duration(req.TimeSeries.Dt[last]) * time.Second)
	for _, v := range site.Vehicles().Settings() {
		if !v.GetAdaptivePlanLearning() || v.Instance() == nil || v.GetMode() == api.ModeOff {
			continue
		}
		instance := v.Instance()
		connected := false
		for _, lp := range site.ActiveLoadpoints() {
			if lp.GetVehicle() == instance && (lp.GetStatus() == api.StatusB || lp.GetStatus() == api.StatusC) {
				connected = true
			}
		}
		if connected {
			continue
		}
		history, err := session.VehicleSessions(db.Instance, instance.GetTitle())
		if err != nil {
			return fmt.Errorf("arrival history %s: %w", v.Name(), err)
		}
		arrival := session.ExpectedArrival(history, stamp, horizon)
		if arrival == nil {
			continue
		}
		departure, goal := expectedPlanGoal(v, arrival.Time, horizon)
		if departure.IsZero() || !departure.After(arrival.Time) || departure.After(horizon) {
			continue
		}
		if limit := v.GetLimitSoc(); limit > 0 {
			goal = min(goal, limit)
		}
		var lastSession *session.Session
		for i := range history {
			if history[i].Created.Before(stamp) && (lastSession == nil || history[i].Created.After(lastSession.Created)) {
				lastSession = &history[i]
			}
		}
		if lastSession == nil {
			continue
		}
		var minPower, maxPower float64
		for _, lp := range site.Loadpoints() {
			if lp.GetTitle() == lastSession.Loadpoint {
				minPower, maxPower = lp.EffectiveMinPower(), lp.EffectiveMaxPower()
			}
		}
		bat, ok := expectedVehicleBattery(instance.Capacity(), arrival.Soc, minPower, maxPower, goal, arrival.Time, departure, details.Timestamps, req.TimeSeries.Dt)
		if !ok {
			continue
		}
		if len(req.Batteries) >= 16 {
			return fmt.Errorf("expected vehicles exceed optimizer battery limit")
		}
		req.Batteries = append(req.Batteries, bat)
		details.BatteryDetails = append(details.BatteryDetails, batteryDetail{Type: batteryTypeVehicle, Name: v.Name(), Title: instance.GetTitle(), Capacity: instance.Capacity(), arrival: &arrival.Time, departure: &departure})
	}
	return nil
}
