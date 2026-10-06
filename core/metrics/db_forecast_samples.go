package metrics

import (
	"context"
	"errors"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"gorm.io/gorm/clause"
)

// ForecastLeadTimes are the offsets ahead of a forecasted slot's start that
// ArchiveForecastSample samples: an hour out (imminent charge/discharge
// decisions), six hours (most of a day-ahead plan) and a full day (the far end
// of the horizon most slots spend most of their life in). Forecast error is not
// uniform across these - a spread gives later analysis something to compare.
var ForecastLeadTimes = []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour}

// forecastSample is one archived solar forecast reading: what the forecast held
// for the slot starting at Slot, sampled roughly LeadMinutes before that slot
// started. Kept separate from the meters table, which only ever captures the
// near-zero-lead nowcast for the slot containing "now" (see the Forecast
// collector) - that is enough to drive solarAdjusted's scale today, but the
// error distribution an hour out is not the error distribution a day out, and
// nothing recorded which was which.
type forecastSample struct {
	Slot        int64   `gorm:"column:slot;uniqueIndex:forecast_samples_slot_lead"`         // unix ts, start of the forecasted slot
	LeadMinutes int     `gorm:"column:lead_minutes;uniqueIndex:forecast_samples_slot_lead"` // requested lead time, minutes
	Energy      float64 `gorm:"column:energy"`                                              // forecast energy for the slot, kWh
}

func (forecastSample) TableName() string {
	return "forecast_samples"
}

// schema migration for forecastSample lives in SetupSchema (db.go), the test
// entry point, not in its own init/db.Register - a test that only calls
// SetupSchema must still get this table.

// ArchiveForecastSample snapshots, for each of ForecastLeadTimes, the forecast
// energy of the slot that is currently that far ahead of now. energyAt is called
// with the target slot's [from,to) bounds and must return the forecast energy in
// Wh (mirroring solarEnergy's contract) plus ok=false when the target slot lies
// outside the forecast horizon entirely - a provider simply has no opinion about
// it yet, which is not the same thing as it having forecast zero production.
// Archiving that "no data" case as an Energy of 0 would make it indistinguishable
// from a real overnight zero, so those lead times are skipped for this tick: the
// row is written later, once its target slot actually falls inside the horizon,
// or never if it never does. The result, when ok, is archived in kWh like the
// rest of the schema.
//
// Callers are expected to throttle to once per 15min slot (as persistTariffs
// does for tariff values) - at that cadence, a fixed lead time's target slot
// advances by exactly one slot per call, so each (slot, lead) pair is written
// at most once over the process's lifetime and the table grows by a small,
// bounded number of rows per slot rather than per update cycle. OnConflict is a
// safety net for an overlapping call after a restart, not a refinement path.
//
// A write failure for one lead time does not abort the remaining lead times:
// each (slot, lead) pair only ever gets one chance at being written (DoNothing
// means a later call for the same pair is a no-op, not a retry), so returning
// early on the first error would permanently drop coverage for every later,
// unrelated lead time in the same call. Errors are joined and returned together
// after all lead times have been attempted.
func ArchiveForecastSample(now time.Time, energyAt func(from, to time.Time) (float64, bool)) error {
	var errs error

	for _, lead := range ForecastLeadTimes {
		target := now.Add(lead).Truncate(tariff.SlotDuration)

		energy, ok := energyAt(target, target.Add(tariff.SlotDuration))
		if !ok {
			continue
		}
		if !finite(energy) || energy < 0 {
			errs = errors.Join(errs, errors.New("invalid solar forecast energy"))
			continue
		}

		sample := forecastSample{
			Slot:        target.Unix(),
			LeadMinutes: int(lead.Minutes()),
			Energy:      energy / 1e3, // Wh -> kWh
		}

		if err := db.Instance.Clauses(clause.OnConflict{DoNothing: true}).Create(&sample).Error; err != nil {
			errs = errors.Join(errs, err)
		}
	}

	// samples at ShadingLeadMinutes are kept long enough for the shading map to
	// learn from the same calendar weeks of earlier years (ADR-016)
	return errors.Join(errs, db.Instance.
		Where("slot < ? AND (lead_minutes <> ? OR slot < ?)",
			now.AddDate(0, 0, -90).Unix(), ShadingLeadMinutes, now.AddDate(-ShadingRetentionYears, 0, -30).Unix()).
		Delete(new(forecastSample)).Error)
}

// ShadingLeadMinutes is the forecast lead the solar shading map learns from.
// The 60 min samples gave a slightly better day-ahead backtest than the 1440 min
// ones (47 vs 48 % slot error, core/shading/backtest_test.go): a nearer
// forecast carries less weather error, leaving more of the fixed geometry in
// the ratio.
const ShadingLeadMinutes = 60

// ShadingRetentionYears is how many years of ShadingLeadMinutes samples are
// kept (plus a month, covering the seasonal window around the anniversary).
const ShadingRetentionYears = 3

// LeadTimeSample pairs one archived forecast reading with the actual PV energy
// measured for the same slot, for a per-lead-time forecast bias calculation.
type LeadTimeSample struct {
	Slot        int64
	LeadMinutes int
	Forecast    float64 // archived forecast energy for the slot, kWh
	Actual      float64 // measured PV energy for the same slot, kWh
}

// QueryLeadTimeSamples joins forecast_samples against the PV meters' own slot
// energy for slots from "from" onward. The join is an inner join on purpose: a
// forecast_samples row for a slot that has not been measured yet (or never
// will be, e.g. no PV configured) is silently skipped rather than treated as
// a zero actual, which would bias every lead-time bucket toward "forecast
// always over-predicts". Multiple PV meters are summed per slot, matching how
// querySolarScale compares total PV against total forecast.
func QueryLeadTimeSamples(from time.Time) ([]LeadTimeSample, error) {
	var res []LeadTimeSample

	err := db.Instance.Table("forecast_samples fs").
		Select(`fs.slot AS slot, fs.lead_minutes AS lead_minutes, fs.energy AS forecast, SUM(m.energy) AS actual`).
		Joins(`JOIN meters m ON m.ts = fs.slot`).
		Joins(`JOIN entities e ON e.id = m.meter AND e."group" = ?`, PV).
		Where("fs.slot >= ? AND m.energy IS NOT NULL AND m.energy >= 0 AND COALESCE(m.recovered,0)=0 AND COALESCE(m.incomplete,0)=0", from.Unix()).
		Group("fs.slot, fs.lead_minutes, fs.energy").
		Having(`COUNT(DISTINCT m.meter) = (SELECT COUNT(*) FROM entities WHERE "group" = ?)`, PV).
		Scan(&res).Error

	return res, err
}

// ShadingSample is one completed slot with both its archived forecast and a
// clean measurement of the whole PV group, in kWh.
type ShadingSample struct {
	Slot     time.Time
	Forecast float64
	Measured float64
}

// QueryShadingSamples returns the slots in [from,to) that have a forecast
// archived at leadMinutes and a measurement from every PV entity reporting by
// then. Slots with a missing, recovered, incomplete or invalid PV reading, or
// a missing forecast, are absent from the result - never zero. hasPV is false
// when no PV entity exists at all.
func QueryShadingSamples(ctx context.Context, leadMinutes int, from, to time.Time) (res []ShadingSample, hasPV bool, err error) {
	pv, err := queryGroupSeries(ctx, PV, from, to)
	if err != nil || !pv.present {
		return nil, pv.present, err
	}

	var rows []forecastSample
	if err := db.Instance.WithContext(ctx).
		Where("lead_minutes = ? AND slot >= ? AND slot < ?", leadMinutes, from.Unix(), to.Unix()).
		Order("slot").Find(&rows).Error; err != nil {
		return nil, true, err
	}

	for _, r := range rows {
		wh, excluded := pv.energy(r.Slot, false)
		if wh == nil || excluded || !finite(r.Energy) || r.Energy < 0 {
			continue
		}
		res = append(res, ShadingSample{Slot: time.Unix(r.Slot, 0), Forecast: r.Energy, Measured: *wh / 1e3})
	}
	return res, true, nil
}
