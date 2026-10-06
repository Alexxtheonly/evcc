package metrics

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func shadowSlots(from time.Time, n int, wh float64) []HomeForecastSlot {
	res := make([]HomeForecastSlot, n)
	for i := range res {
		res[i] = HomeForecastSlot{Start: from.Add(time.Duration(i) * tariff.SlotDuration), Low: wh / 2, Base: wh, High: 2 * wh}
	}
	return res
}

func countRows(t *testing.T, table string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Instance.Table(table).Count(&n).Error)
	return n
}

func TestArchiveHomeForecastShadow(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	issued := time.Date(2026, 2, 3, 10, 5, 0, 0, time.UTC)
	slots := shadowSlots(issued.Truncate(tariff.SlotDuration), 2*96, 300)

	t.Run("lead times only, the primary table untouched", func(t *testing.T) {
		require.NoError(t, ArchiveHomeForecastShadow(issued, "on", slots, false))
		var rows []homeForecastShadowSample
		require.NoError(t, db.Instance.Order("lead_minutes").Find(&rows).Error)
		require.Len(t, rows, len(ForecastLeadTimes))
		for i, lead := range ForecastLeadTimes {
			assert.Equal(t, int(lead.Minutes()), rows[i].LeadMinutes)
			assert.Equal(t, issued.Add(lead).Truncate(tariff.SlotDuration).Unix(), rows[i].Slot)
			assert.Equal(t, "on", rows[i].Variant)
			assert.Equal(t, issued.Unix(), rows[i].Issued)
			assert.InDelta(t, 0.3, rows[i].Base, 1e-12, "kWh")
			assert.InDelta(t, 0.15, rows[i].Low, 1e-12)
			assert.InDelta(t, 0.6, rows[i].High, 1e-12)
		}
		assert.Zero(t, countRows(t, "home_forecast_samples"), "shadow rows never reach home_forecast_samples")
	})

	t.Run("unique per slot, lead and variant", func(t *testing.T) {
		later := issued.Add(5 * time.Minute) // same targets
		require.NoError(t, ArchiveHomeForecastShadow(later, "on", shadowSlots(issued.Truncate(tariff.SlotDuration), 2*96, 999), false))
		assert.Equal(t, int64(3), countRows(t, "home_forecast_shadow_samples"), "first issue kept")
		require.NoError(t, ArchiveHomeForecastShadow(later, "auto", slots, false))
		assert.Equal(t, int64(6), countRows(t, "home_forecast_shadow_samples"), "a second variant is separate")
	})

	t.Run("horizon snapshot: every future slot at its actual lead", func(t *testing.T) {
		snap := time.Date(2026, 2, 4, 0, 2, 0, 0, time.UTC)
		start := snap.Truncate(tariff.SlotDuration)
		ok, err := HomeForecastShadowSnapshotExists(snap.Add(-time.Hour), snap.Add(time.Hour))
		require.NoError(t, err)
		assert.False(t, ok, "lead-only archives are not snapshots")

		require.NoError(t, ArchiveHomeForecastShadow(snap, "auto", shadowSlots(start, 96, 200), true))
		var n int64
		require.NoError(t, db.Instance.Model(new(homeForecastShadowSample)).Where("issued = ?", snap.Unix()).Count(&n).Error)
		assert.Equal(t, int64(95+2), n, "95 future slots (slot 0 started before the issue) and the 60/360 min lead rows")
		var lead int
		require.NoError(t, db.Instance.Model(new(homeForecastShadowSample)).Where("issued = ? AND slot = ?", snap.Unix(), start.Add(tariff.SlotDuration).Unix()).Pluck("lead_minutes", &lead).Error)
		assert.Equal(t, 13, lead)

		ok, err = HomeForecastShadowSnapshotExists(snap.Add(-time.Minute), snap.Add(time.Minute))
		require.NoError(t, err)
		assert.True(t, ok)
		ok, err = HomeForecastShadowSnapshotExists(snap.Add(time.Minute), snap.Add(time.Hour))
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("invalid values are refused", func(t *testing.T) {
		bad := shadowSlots(issued.Truncate(tariff.SlotDuration), 2*96, 300)
		for i := range bad {
			bad[i].Low = 400
		}
		assert.Error(t, ArchiveHomeForecastShadow(issued.Add(time.Hour), "on", bad, true))
		assert.Error(t, ArchiveHomeForecastShadow(issued, "", slots, false))
	})

	t.Run("90-day retention and deletion with the household forecasts", func(t *testing.T) {
		require.NoError(t, ArchiveHomeForecast(issued, slots))
		primary := countRows(t, "home_forecast_samples")
		require.Positive(t, primary)
		shadow := countRows(t, "home_forecast_shadow_samples")

		// a late archive prunes everything older than 90 days
		late := issued.AddDate(0, 0, 92)
		require.NoError(t, ArchiveHomeForecastShadow(late, "on", shadowSlots(late.Truncate(tariff.SlotDuration), 2*96, 300), false))
		assert.Equal(t, int64(3), countRows(t, "home_forecast_shadow_samples"))
		assert.Equal(t, primary, countRows(t, "home_forecast_samples"), "the primary table keeps its own retention")
		_ = shadow

		n, err := DeleteHomeForecastSamples(late, late.AddDate(0, 0, 3))
		require.NoError(t, err)
		assert.Equal(t, int64(3), n)
		assert.Zero(t, countRows(t, "home_forecast_shadow_samples"))
		n, err = DeleteHomeForecastSamples(issued, issued.AddDate(0, 0, 3))
		require.NoError(t, err)
		assert.Equal(t, primary, n)
	})
}

func TestHomeForecastShadowWindows(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	loc, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)
	day := time.Date(2026, 2, 3, 0, 0, 0, 0, loc)
	window := func(issued time.Time) (time.Time, time.Time, bool) {
		t := issued.In(loc)
		d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		return d, d.AddDate(0, 0, 1), t.Hour() == 0
	}

	// measured 0.25 kWh per slot on Feb 3 and Feb 4, one recovered slot and one gap on Feb 4
	for ts := day; ts.Before(day.AddDate(0, 0, 2)); ts = ts.Add(tariff.SlotDuration) {
		if ts.Equal(day.AddDate(0, 0, 1).Add(time.Hour)) {
			continue
		}
		recovered := ts.Equal(day.AddDate(0, 0, 1).Add(2 * time.Hour))
		require.NoError(t, db.Instance.Exec(`INSERT INTO meters (meter, ts, energy, recovered) VALUES (1, ?, 0.25, ?)`, ts.Unix(), recovered).Error)
	}

	for d, issued := range []time.Time{day.Add(3 * time.Minute), day.AddDate(0, 0, 1).Add(time.Minute)} {
		start := issued.Truncate(tariff.SlotDuration)
		require.NoError(t, ArchiveHomeForecastShadow(issued, "auto", shadowSlots(start, 96+4, 300), true))
		require.NoError(t, ArchiveHomeForecastShadow(issued, "on", shadowSlots(start, 96+4, 200+float64(d)*100), true))
	}
	// a later snapshot for the same window is ignored, a non-window issue too
	require.NoError(t, ArchiveHomeForecastShadow(day.Add(30*time.Minute), "auto", shadowSlots(day.Add(30*time.Minute), 96, 1), true))
	require.NoError(t, ArchiveHomeForecastShadow(day.Add(30*time.Minute), "on", shadowSlots(day.Add(30*time.Minute), 96, 1), true))
	require.NoError(t, ArchiveHomeForecastShadow(day.Add(13*time.Hour), "on", shadowSlots(day.Add(13*time.Hour), 96, 1), true))
	// lead-only rows are no snapshot
	require.NoError(t, ArchiveHomeForecastShadow(day.AddDate(0, 0, 2).Add(time.Minute), "on", shadowSlots(day.AddDate(0, 0, 2), 200, 1), false))

	windows, err := HomeForecastShadowWindows(window, []string{"auto", "on"}, day.AddDate(0, 0, 1).Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, windows, 1, "Feb 4 has not ended yet")

	windows, err = HomeForecastShadowWindows(window, []string{"auto", "on"}, day.AddDate(0, 0, 5))
	require.NoError(t, err)
	require.Len(t, windows, 2)

	w := windows[0]
	assert.True(t, w.Start.Equal(day))
	assert.Equal(t, day.Add(3*time.Minute).Unix(), w.Issued.Unix(), "earliest snapshot of the window")
	assert.Equal(t, 96, w.Slots)
	assert.Equal(t, 1, w.Missing, "slot 00:00 started before the issue")
	assert.InDelta(t, 95*0.25, w.Actual, 1e-9)
	assert.InDelta(t, 95*0.3, w.Variants["auto"], 1e-9)
	assert.InDelta(t, 95*0.2, w.Variants["on"], 1e-9)

	w = windows[1]
	assert.Equal(t, 3, w.Missing, "first slot, the gap and the recovered slot")
	assert.InDelta(t, 93*0.25, w.Actual, 1e-9)
	assert.InDelta(t, 93*0.3, w.Variants["on"], 1e-9)
}
