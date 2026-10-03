package metrics

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/evcc-io/evcc/core/session"
	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
)

// EV timing is a tariff-only, same-energy session comparison. It does not model
// PV, battery routing, demand charges or shared-circuit feasibility, and must not
// be added to the household world chain. Connection-date cohorts make daily sums
// reconcile without pretending the two schedules consume equal energy each day.
func computeEVTimingPrices(ctx context.Context, report *EnergyPriceReport, prices map[int64]tariffSlot) (EVTimingPrice, error) {
	result := EVTimingPrice{Status: "unavailable", Reason: "no_completed_sessions"}
	var sessions []session.Session
	// Include crossings so missing full-session evidence remains visible.
	if err := db.Instance.WithContext(ctx).Where("created < ? AND (disconnected IS NULL OR disconnected >= ?)", report.EvaluatedTo, report.From).Find(&sessions).Error; err != nil {
		return result, err
	}
	var entities []entity
	if err := db.Instance.WithContext(ctx).Where("\"group\" = ?", Loadpoint).Find(&entities).Error; err != nil {
		return result, err
	}
	var snapshots []OptimizerSnapshot
	if len(sessions) > 0 {
		if err := db.Instance.WithContext(ctx).Where("ts >= ? AND ts < ?", report.From.Unix(), report.EvaluatedTo.Unix()).Order("ts").Find(&snapshots).Error; err != nil {
			return result, err
		}
	}
	for i, s := range sessions {
		cohort := s.Created
		if s.Connected != nil {
			cohort = *s.Connected
		}
		if cohort.Before(report.From) || !cohort.Before(report.EvaluatedTo) {
			continue
		}
		if s.ChargedEnergy <= 0 {
			continue
		}
		date := s.Created.In(report.From.Location()).Format("2006-01-02")
		if s.Connected != nil {
			date = s.Connected.In(report.From.Location()).Format("2006-01-02")
		}
		var day *EVTimingPrice
		for j := range report.Days {
			if report.Days[j].Date == date {
				day = &report.Days[j].EVTiming
				break
			}
		}
		// Connections beginning this month but ending outside it are excluded;
		// previous-month cohorts are not partially rebooked into this month.
		actual, immediate, ok, err := priceEVSession(ctx, i, s, sessions, entities, snapshots, prices, report.From, report.EvaluatedTo)
		if err != nil {
			return result, err
		}
		for _, r := range []*EVTimingPrice{&result, day} {
			if r == nil {
				continue
			}
			if !ok {
				r.ExcludedSessions++
				r.Reason = "historical_evidence_unavailable"
				continue
			}
			r.IncludedSessions++
			addPricePointer(&r.EnergyKWh, s.ChargedEnergy)
			addPricePointer(&r.ActualTariffCost, actual)
			addPricePointer(&r.ImmediateTariffCost, immediate)
			addPricePointer(&r.Savings, immediate-actual)
		}
	}
	finishEVTiming(&result)
	for i := range report.Days {
		finishEVTiming(&report.Days[i].EVTiming)
	}
	return result, nil
}
func addPricePointer(p **float64, v float64) {
	if *p == nil {
		*p = new(float64)
	}
	**p += v
}
func finishEVTiming(r *EVTimingPrice) {
	if r.IncludedSessions > 0 {
		r.Status = "estimated"
		r.Reason = "tariff_only_connection_cohort"
		if r.ExcludedSessions > 0 {
			r.Status = "partial"
		}
	}
}

func priceEVSession(ctx context.Context, index int, s session.Session, sessions []session.Session, entities []entity, snapshots []OptimizerSnapshot, prices map[int64]tariffSlot, from, to time.Time) (float64, float64, bool, error) {
	if s.Connected == nil || s.Disconnected == nil || s.Connected.Before(from) || s.Disconnected.After(to) || !s.Disconnected.After(*s.Connected) || s.Created.Before(*s.Connected) || s.Finished.After(*s.Disconnected) || s.Finished.Before(s.Created) || !finite(s.ChargedEnergy) {
		return 0, 0, false, nil
	}
	// An overlap could share a circuit or a meter; neither capacity allocation nor
	// sub-slot attribution is historical evidence available in this archive.
	floor := s.Connected.Truncate(tariff.SlotDuration)
	ceil := s.Disconnected.Truncate(tariff.SlotDuration)
	if ceil.Before(*s.Disconnected) {
		ceil = ceil.Add(tariff.SlotDuration)
	}
	for j, other := range sessions {
		if j == index {
			continue
		}
		begin, end := other.Connected, other.Disconnected
		if begin == nil {
			begin = &other.Created
		}
		if begin.Before(ceil) && (end == nil || end.After(floor)) {
			return 0, 0, false, nil
		}
	}
	var ent *entity
	for i := range entities {
		if entities[i].Title == s.Loadpoint {
			if ent != nil {
				return 0, 0, false, nil
			}
			ent = &entities[i]
		}
	}
	if ent != nil {
		for _, candidate := range entities {
			if candidate.Id != ent.Id && candidate.Title == ent.Title {
				return 0, 0, false, nil
			}
		}
	}
	if ent == nil {
		return 0, 0, false, nil
	}
	capW, ok := historicalEVPower(s, snapshots, ent.Title)
	if !ok {
		return 0, 0, false, nil
	}
	var rows []meter
	if err := db.Instance.WithContext(ctx).Where("meter = ? AND ts >= ? AND ts < ? AND energy IS NOT NULL", ent.Id, floor.Unix(), ceil.Unix()).Find(&rows).Error; err != nil {
		return 0, 0, false, err
	}
	byTS := make(map[int64]meter, len(rows))
	for _, r := range rows {
		byTS[r.Timestamp] = r
	}
	actual, energy, immediate, remaining := 0.0, 0.0, 0.0, s.ChargedEnergy
	for ts := floor; ts.Before(ceil); ts = ts.Add(tariff.SlotDuration) {
		price, ok := prices[ts.Unix()]
		if !ok || !finite(price.Grid) {
			return 0, 0, false, nil
		}
		row, ok := byTS[ts.Unix()]
		if !ok || row.Recovered || row.Incomplete || !finite(row.Energy) || row.Energy < 0 {
			return 0, 0, false, nil
		}
		actual += row.Energy * price.Grid
		energy += row.Energy
		begin, end := ts, ts.Add(tariff.SlotDuration)
		if begin.Before(*s.Connected) {
			begin = *s.Connected
		}
		if end.After(*s.Disconnected) {
			end = *s.Disconnected
		}
		amount := min(remaining, capW/1000*end.Sub(begin).Hours())
		immediate += amount * price.Grid
		remaining -= amount
	}
	// No energy normalization: materially mismatched session/register history
	// cannot support a same-energy comparison. 1Wh allows floating-point counters.
	if math.Abs(energy-s.ChargedEnergy) > .001 || remaining > .001 {
		return 0, 0, false, nil
	}
	return actual, immediate, true, nil
}

func historicalEVPower(s session.Session, snapshots []OptimizerSnapshot, title string) (float64, bool) {
	var power float64
	var last time.Time
	for _, snapshot := range snapshots {
		ts := time.Unix(snapshot.TS, 0)
		if ts.Before(s.Connected.Add(-tariff.SlotDuration)) || !ts.Before(*s.Disconnected) {
			continue
		}
		if snapshot.Version != 1 {
			return 0, false
		}
		var req struct {
			Batteries []struct {
				CMax float64 `json:"c_max"`
			} `json:"batteries"`
		}
		var quality struct {
			Devices []struct{ Kind, Name, Title string } `json:"devices"`
		}
		if json.Unmarshal(snapshot.Request, &req) != nil || json.Unmarshal(snapshot.Quality, &quality) != nil || len(req.Batteries) != len(quality.Devices) {
			return 0, false
		}
		found := false
		for i, d := range quality.Devices {
			matches := d.Kind == "loadpoint" && d.Title == title
			if !ts.After(*s.Connected) && d.Kind == "expectedVehicle" && s.Vehicle != "" && d.Title == s.Vehicle {
				matches = true
			}
			if !matches {
				continue
			}
			if found {
				return 0, false
			}
			found = true
			value := req.Batteries[i].CMax
			if !finite(value) || value <= 0 || power != 0 && power != value {
				return 0, false
			}
			power = value
		}
		if !found {
			if ts.Before(*s.Connected) {
				continue
			}
			return 0, false
		}
		if last.IsZero() {
			// No snapshot taken after the hypothetical immediate start may supply its cap.
			if ts.After(*s.Connected) {
				return 0, false
			}
			last = *s.Connected
		}
		if ts.Sub(last) > tariff.SlotDuration {
			return 0, false
		}
		last = ts
	}
	// Full-session archival coverage avoids silently extrapolating a recent cap
	// across a missing/expired historical interval. CMax is watts, frozen from
	// EffectiveMaxPower (configured current, phases, vehicle and circuit ceiling).
	return power, power > 0 && !last.IsZero() && s.Disconnected.Sub(last) <= tariff.SlotDuration
}
