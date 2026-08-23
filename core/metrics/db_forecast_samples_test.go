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
	energyAt := func(from, _ time.Time) float64 {
		return float64(from.Unix()) // Wh; content is irrelevant, only traceability matters
	}

	require.NoError(t, ArchiveForecastSample(now, energyAt))

	var samples []forecastSample
	require.NoError(t, db.Instance.Order("lead_minutes").Find(&samples).Error)
	require.Len(t, samples, len(forecastLeadTimes), "one row per lead time")

	for i, lead := range forecastLeadTimes {
		want := now.Add(lead).Truncate(15 * time.Minute)
		s := samples[i]
		require.Equal(t, int(lead.Minutes()), s.LeadMinutes)
		require.Equal(t, want.Unix(), s.Slot)
		require.InDelta(t, float64(want.Unix())/1e3, s.Energy, 1e-9)
	}

	// a second call with the same now (an overlapping tick, e.g. after a
	// restart) re-targets the exact same (slot, lead) rows - the row count
	// must not grow and the original value must survive untouched
	require.NoError(t, ArchiveForecastSample(now, func(from, to time.Time) float64 {
		return -1e6 // distinguishable sentinel; must not appear if dedup holds
	}))

	var count int64
	require.NoError(t, db.Instance.Model(new(forecastSample)).Count(&count).Error)
	require.Equal(t, int64(len(forecastLeadTimes)), count, "an overlapping call for the same now must not duplicate rows")

	require.NoError(t, db.Instance.Order("lead_minutes").Find(&samples).Error)
	for i, lead := range forecastLeadTimes {
		want := now.Add(lead).Truncate(15 * time.Minute)
		require.InDelta(t, float64(want.Unix())/1e3, samples[i].Energy, 1e-9, "first observation must not be overwritten")
	}
}
