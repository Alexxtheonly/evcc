package metrics

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/require"
)

// TestArchiveForecastSample verifies the archive tags each sample with the lead
// time it was requested at, and that a repeat write for the same (slot, lead)
// does not clobber or duplicate the first observation - the whole point of
// archiving by lead time is that the value recorded close to that lead stays
// put instead of being overwritten by a later, shorter-lead reading of the
// same slot.
func TestArchiveForecastSample(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(forecastSample)))

	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	// energyAt returns a value keyed by the requested slot's start, so we can
	// tell which target slot each lead time actually resolved to
	energyAt := func(from, _ time.Time) (float64, bool) {
		return float64(from.Unix()), true // Wh; content is irrelevant, only traceability matters
	}

	require.NoError(t, ArchiveForecastSample(now, energyAt))

	var samples []forecastSample
	require.NoError(t, db.Instance.Order("lead_minutes").Find(&samples).Error)
	require.Len(t, samples, len(ForecastLeadTimes), "one row per lead time")

	for i, lead := range ForecastLeadTimes {
		want := now.Add(lead).Truncate(15 * time.Minute)
		s := samples[i]
		require.Equal(t, int(lead.Minutes()), s.LeadMinutes)
		require.Equal(t, want.Unix(), s.Slot)
		require.InDelta(t, float64(want.Unix())/1e3, s.Energy, 1e-9)
	}

	// a second call with the same now (an overlapping tick, e.g. after a
	// restart) re-targets the exact same (slot, lead) rows - the row count
	// must not grow and the original value must survive untouched
	require.NoError(t, ArchiveForecastSample(now, func(from, to time.Time) (float64, bool) {
		return 1e6, true // distinguishable valid value; must not appear if dedup holds
	}))

	var count int64
	require.NoError(t, db.Instance.Model(new(forecastSample)).Count(&count).Error)
	require.Equal(t, int64(len(ForecastLeadTimes)), count, "an overlapping call for the same now must not duplicate rows")

	require.NoError(t, db.Instance.Order("lead_minutes").Find(&samples).Error)
	for i, lead := range ForecastLeadTimes {
		want := now.Add(lead).Truncate(15 * time.Minute)
		require.InDelta(t, float64(want.Unix())/1e3, samples[i].Energy, 1e-9, "first observation must not be overwritten")
	}
}

// TestQueryLeadTimeSamples verifies the forecast_samples/meters join: a slot with a
// matching PV reading comes back paired with its lead time, a slot with none (not
// measured yet) is silently dropped rather than surfacing as a zero actual, and
// multiple PV meters for the same slot are summed.
func TestQueryLeadTimeSamples(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	pv1, err := createEntity(PV, "pv1", "")
	require.NoError(t, err)
	pv2, err := createEntity(PV, "pv2", "")
	require.NoError(t, err)

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC).Truncate(15 * time.Minute)
	measuredSlot := base
	unmeasuredSlot := base.Add(15 * time.Minute)

	require.NoError(t, db.Instance.Create(&forecastSample{Slot: measuredSlot.Unix(), LeadMinutes: 60, Energy: 2.0}).Error)
	require.NoError(t, db.Instance.Create(&forecastSample{Slot: measuredSlot.Unix(), LeadMinutes: 1440, Energy: 1.5}).Error)
	require.NoError(t, db.Instance.Create(&forecastSample{Slot: unmeasuredSlot.Unix(), LeadMinutes: 60, Energy: 3.0}).Error)

	require.NoError(t, db.Instance.Create(&meter{Meter: pv1.Id, Timestamp: measuredSlot.Unix(), Energy: 1.2}).Error)
	require.NoError(t, db.Instance.Create(&meter{Meter: pv2.Id, Timestamp: measuredSlot.Unix(), Energy: 0.6}).Error)
	// unmeasuredSlot deliberately has no meter row

	rows, err := QueryLeadTimeSamples(base)
	require.NoError(t, err)
	require.Len(t, rows, 2, "only the measured slot's two lead-time rows, the unmeasured slot is dropped")

	byLead := make(map[int]LeadTimeSample, len(rows))
	for _, r := range rows {
		byLead[r.LeadMinutes] = r
	}

	require.InDelta(t, 2.0, byLead[60].Forecast, 1e-9)
	require.InDelta(t, 1.8, byLead[60].Actual, 1e-9, "both PV meters summed")
	require.InDelta(t, 1.5, byLead[1440].Forecast, 1e-9)
	require.InDelta(t, 1.8, byLead[1440].Actual, 1e-9)
}

// TestArchiveForecastSampleOutsideHorizon verifies that a lead time whose target
// slot falls outside the forecast horizon (ok=false) is skipped entirely rather
// than archived as an Energy of 0 - a missing forecast and a forecast of zero
// production are not the same thing, and only omitting the row keeps them
// distinguishable to later bias analysis.
func TestArchiveForecastSampleOutsideHorizon(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(forecastSample)))

	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	// only the 1h lead time has forecast coverage; 6h and 24h are beyond the
	// provider's horizon and must not produce a row
	require.NoError(t, ArchiveForecastSample(now, func(from, to time.Time) (float64, bool) {
		if from.Sub(now) > time.Hour {
			return 0, false
		}
		return 1234, true
	}))

	var samples []forecastSample
	require.NoError(t, db.Instance.Order("lead_minutes").Find(&samples).Error)
	require.Len(t, samples, 1, "only the in-horizon lead time is archived")
	require.Equal(t, int(time.Hour.Minutes()), samples[0].LeadMinutes)
}
