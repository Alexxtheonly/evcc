package metrics

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/require"
)

func TestPersistTariffs(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(tariffValue)))

	slot := time.Date(2026, 4, 15, 16, 15, 0, 0, time.UTC)
	grid, co2 := 0.3, 250.0

	// nil values omitted
	require.NoError(t, PersistTariffs(slot, &grid, nil, &co2, nil))

	var res tariffValue
	require.NoError(t, db.Instance.First(&res).Error)
	require.Equal(t, slot.Unix(), res.Timestamp)
	require.InDelta(t, 0.3, *res.Grid, 0.001)
	require.Nil(t, res.FeedIn)
	require.InDelta(t, 250, *res.Co2, 0.001)
	require.Nil(t, res.Temperature)

	// duplicate slot ignored, first values kept
	other := 0.4
	require.NoError(t, PersistTariffs(slot, &other, nil, nil, nil))

	var count int64
	require.NoError(t, db.Instance.Model(new(tariffValue)).Count(&count).Error)
	require.Equal(t, int64(1), count)

	require.NoError(t, db.Instance.First(&res).Error)
	require.InDelta(t, 0.3, *res.Grid, 0.001)

	// all nil: no row
	require.NoError(t, PersistTariffs(slot.Add(15*time.Minute), nil, nil, nil, nil))
	require.NoError(t, db.Instance.Model(new(tariffValue)).Count(&count).Error)
	require.Equal(t, int64(1), count)

	// all values set: each column mapped independently
	next := slot.Add(30 * time.Minute)
	feedin, temp := 0.08, 21.5
	require.NoError(t, PersistTariffs(next, &grid, &feedin, &co2, &temp))

	require.NoError(t, db.Instance.Where("ts = ?", next.Unix()).First(&res).Error)
	require.InDelta(t, 0.3, *res.Grid, 0.001)
	require.InDelta(t, 0.08, *res.FeedIn, 0.001)
	require.InDelta(t, 250, *res.Co2, 0.001)
	require.InDelta(t, 21.5, *res.Temperature, 0.001)
}

// TestPersistTariffsFillsMissingUsageLater covers the write-path half of the ledger
// coverage bug: a slot written while one tariff was unavailable left that column NULL,
// and clause.OnConflict{DoNothing} meant nothing ever filled it again. A single UI
// config edit that briefly removed the feed-in tariff therefore left 86 permanently
// feed-in-less slots, all of them dropped from the savings ledger.
func TestPersistTariffsFillsMissingUsageLater(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(tariffValue)))

	slot := time.Date(2026, 4, 15, 16, 15, 0, 0, time.UTC)

	// first write: the feed-in tariff was unavailable
	grid := 0.30
	require.NoError(t, PersistTariffs(slot, &grid, nil, nil, nil))

	// retry once it is back - and with a different grid price, which must NOT win
	otherGrid, feedin := 0.40, 0.0786
	require.NoError(t, PersistTariffs(slot, &otherGrid, &feedin, nil, nil))

	var count int64
	require.NoError(t, db.Instance.Model(new(tariffValue)).Count(&count).Error)
	require.Equal(t, int64(1), count, "the retry must update the existing row, not add a second one")

	var res tariffValue
	require.NoError(t, db.Instance.First(&res).Error)
	require.InDelta(t, 0.30, *res.Grid, 1e-9, "a value already on record is never overwritten")
	require.NotNil(t, res.FeedIn, "a NULL left by an unavailable tariff must be fillable later")
	require.InDelta(t, 0.0786, *res.FeedIn, 1e-9)
}

func TestGridPriceMedian(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(tariffValue)))

	from := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	stats, err := GridPriceMedian(from, to)
	require.NoError(t, err)
	require.Zero(t, stats.Samples, "no history is absence, not a zero price")

	feedin := 0.08
	for i, price := range []float64{0.40, 0.20, 0.50} {
		require.NoError(t, PersistTariffs(from.Add(time.Duration(i)*15*time.Minute), &price, &feedin, nil, nil))
	}
	// a slot without grid price must not count as zero
	require.NoError(t, PersistTariffs(from.Add(45*time.Minute), nil, &feedin, nil, nil))
	// outside [from,to)
	late := 0.10
	require.NoError(t, PersistTariffs(to, &late, nil, nil, nil))

	stats, err = GridPriceMedian(from, to)
	require.NoError(t, err)
	require.Equal(t, 3, stats.Samples)
	require.InDelta(t, 0.40, stats.Median, 1e-9)
	require.Equal(t, 1, stats.Days)

	even := 0.30
	require.NoError(t, PersistTariffs(from.Add(45*time.Minute), &even, nil, nil, nil))
	stats, err = GridPriceMedian(from, to)
	require.NoError(t, err)
	require.Equal(t, 4, stats.Samples)
	require.InDelta(t, 0.35, stats.Median, 1e-9)
}

// TestGridPriceMedianBackfill: backfilled prices count only where nothing was recorded,
// days with prices are counted with gaps excluded, and without the table nothing changes.
func TestGridPriceMedianBackfill(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(tariffValue)))

	day := time.Date(2026, 6, 10, 12, 0, 0, 0, time.Local)
	recorded := 0.40
	require.NoError(t, PersistTariffs(day, &recorded, nil, nil, nil))
	from, to := day.AddDate(0, 0, -30), day.Add(time.Hour)

	stats, err := GridPriceMedian(from, to)
	require.NoError(t, err)
	require.Equal(t, 1, stats.Samples, "no backfill table: recorded prices only")

	require.NoError(t, db.Instance.Exec("CREATE TABLE "+TariffsBackfillTable+" (ts integer, grid real, feedin real, co2 real, temperature real)").Error)
	for ts, price := range map[time.Time]float64{
		day:                   0.90, // overlaps the recorded slot: the recorded 0.40 wins
		day.AddDate(0, 0, -2): 0.30, // two days earlier, a day of gap in between
		day.AddDate(0, 0, -3): 0.20,
	} {
		require.NoError(t, db.Instance.Exec("INSERT INTO "+TariffsBackfillTable+" (ts, grid) VALUES (?, ?)", ts.Unix(), price).Error)
	}
	stats, err = GridPriceMedian(from, to)
	require.NoError(t, err)
	require.Equal(t, 3, stats.Samples)
	require.InDelta(t, 0.30, stats.Median, 1e-9)
	require.Equal(t, 3, stats.Days, "gaps excluded")
}
