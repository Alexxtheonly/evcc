package metrics

import (
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// forecastLeadTimes are the offsets ahead of a forecasted slot's start that
// ArchiveForecastSample samples: an hour out (imminent charge/discharge
// decisions), six hours (most of a day-ahead plan) and a full day (the far end
// of the horizon most slots spend most of their life in). Forecast error is not
// uniform across these - a spread gives later analysis something to compare.
var forecastLeadTimes = []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour}

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

func init() {
	db.Register(func(_ *gorm.DB) error {
		return db.Instance.AutoMigrate(new(forecastSample))
	})
}

// ArchiveForecastSample snapshots, for each of forecastLeadTimes, the forecast
// energy of the slot that is currently that far ahead of now. energyAt is called
// with the target slot's [from,to) bounds and must return Wh, mirroring
// solarEnergy's contract; the result is archived in kWh like the rest of the
// schema.
//
// Callers are expected to throttle to once per 15min slot (as persistTariffs
// does for tariff values) - at that cadence, a fixed lead time's target slot
// advances by exactly one slot per call, so each (slot, lead) pair is written
// at most once over the process's lifetime and the table grows by a small,
// bounded number of rows per slot rather than per update cycle. OnConflict is a
// safety net for an overlapping call after a restart, not a refinement path.
func ArchiveForecastSample(now time.Time, energyAt func(from, to time.Time) float64) error {
	for _, lead := range forecastLeadTimes {
		target := now.Add(lead).Truncate(tariff.SlotDuration)

		sample := forecastSample{
			Slot:        target.Unix(),
			LeadMinutes: int(lead.Minutes()),
			Energy:      energyAt(target, target.Add(tariff.SlotDuration)) / 1e3, // Wh -> kWh
		}

		if err := db.Instance.Clauses(clause.OnConflict{DoNothing: true}).Create(&sample).Error; err != nil {
			return err
		}
	}

	return nil
}
