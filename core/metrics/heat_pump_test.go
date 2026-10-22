package metrics

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func heatPumpDB(t *testing.T) (home, hp entity) {
	t.Helper()
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	var err error
	home, err = createEntity(Home, Home, "")
	require.NoError(t, err)
	hp, err = createEntity(Meter, "db:19", "Heat pump")
	require.NoError(t, err)
	return home, hp
}

func TestHeatPumpEntityAndTables(t *testing.T) {
	_, hp := heatPumpDB(t)

	id, ok, err := HeatPumpEntity("db:19")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, hp.Id, id)

	_, ok, err = HeatPumpEntity("db:20")
	require.NoError(t, err)
	assert.False(t, ok, "unknown ref")
	other, err := createEntity(Consumer, "db:21", "")
	require.NoError(t, err)
	_, ok, _ = HeatPumpEntity(other.Name)
	assert.False(t, ok, "only ext meters")

	m := db.Instance.Migrator()
	assert.True(t, m.HasTable("meters_backfill"))
	assert.True(t, m.HasTable("temperature_backfill"))
	assert.True(t, m.HasIndex("meters_backfill", "meters_backfill_meter_ts"))
}

func TestHeatPumpReadToleratesMissingBackfillTables(t *testing.T) {
	_, hp := heatPumpDB(t)
	require.NoError(t, db.Instance.Migrator().DropTable("meters_backfill", "temperature_backfill"))

	rows, err := QueryHeatPumpBackfill(hp.Id, time.Unix(0, 0))
	require.NoError(t, err)
	assert.Empty(t, rows)

	temps, err := QueryTemperatureHistory(time.Unix(0, 0))
	require.NoError(t, err)
	assert.Empty(t, temps)

	// and AutoMigrate brings them back on the next start
	require.NoError(t, SetupSchema())
	assert.True(t, db.Instance.Migrator().HasTable("meters_backfill"))
}

func TestHeatPumpRowsKeepFlags(t *testing.T) {
	_, hp := heatPumpDB(t)
	at := time.Date(2026, 1, 15, 6, 0, 0, 0, time.UTC)
	require.NoError(t, db.Instance.Create(&meter{Meter: hp.Id, Timestamp: at.Unix(), Energy: 0.3}).Error)
	require.NoError(t, db.Instance.Create(&meter{Meter: hp.Id, Timestamp: at.Add(15 * time.Minute).Unix(), Energy: 0, Incomplete: true}).Error)
	require.NoError(t, db.Instance.Create(&meter{Meter: hp.Id, Timestamp: at.Add(30 * time.Minute).Unix(), Energy: 4, Recovered: true}).Error)
	require.NoError(t, db.Instance.Create(&meter{Meter: hp.Id, Timestamp: at.Add(45 * time.Minute).Unix(), Energy: 0}).Error)
	require.NoError(t, db.Instance.Exec(`INSERT INTO meters (meter, ts, energy) VALUES (?, ?, NULL)`, hp.Id, at.Add(time.Hour).Unix()).Error)

	rows, err := QueryHeatPumpRows(hp.Id, at)
	require.NoError(t, err)
	require.Len(t, rows, 4, "a row without energy is absent")
	assert.True(t, rows[0].Clean())
	assert.False(t, rows[1].Clean(), "a zero reading flagged incomplete is unknown, not zero")
	assert.False(t, rows[2].Clean())
	assert.True(t, rows[3].Clean(), "an unflagged zero is a measurement")
	assert.True(t, rows[1].Incomplete && rows[2].Recovered)

	incomplete, newest, ok, err := HeatPumpFreshness(hp.Id, at)
	require.NoError(t, err)
	assert.Equal(t, 1, incomplete)
	require.True(t, ok)
	assert.Equal(t, at.Add(45*time.Minute).Unix(), newest.Unix(), "newest slot with a reading")

	_, _, ok, err = HeatPumpFreshness(hp.Id+99, at)
	require.NoError(t, err)
	assert.False(t, ok, "no rows at all")
}

func TestQueryHeatPumpBackfillAndTemperatureHistory(t *testing.T) {
	_, hp := heatPumpDB(t)
	temp, err := createEntity(Temperature, Temperature, "")
	require.NoError(t, err)
	at := time.Date(2026, 1, 15, 6, 0, 0, 0, time.UTC)

	require.NoError(t, db.Instance.Create(&[]metersBackfill{
		{Meter: hp.Id, Timestamp: at.Unix(), Energy: 0.25, Source: "ha_hourly"},
		{Meter: hp.Id, Timestamp: at.Add(15 * time.Minute).Unix(), Energy: 0.25, Source: "ha_hourly"},
		{Meter: hp.Id + 1, Timestamp: at.Unix(), Energy: 9, Source: "ha_hourly"},
	}).Error)
	rows, err := QueryHeatPumpBackfill(hp.Id, at)
	require.NoError(t, err)
	assert.Equal(t, []SlotValue{{at.Unix(), 0.25}, {at.Add(15 * time.Minute).Unix(), 0.25}}, rows, "this meter only, kWh")
	rows, err = QueryHeatPumpBackfill(hp.Id, at.Add(time.Minute))
	require.NoError(t, err)
	assert.Len(t, rows, 1)

	recorded := 1.5
	require.NoError(t, db.Instance.Create(&[]temperatureBackfill{
		{Timestamp: at.Unix(), Temperature: -3, Source: "open-meteo"},
		{Timestamp: at.Add(15 * time.Minute).Unix(), Temperature: -4, Source: "open-meteo"},
	}).Error)
	require.NoError(t, db.Instance.Create(&meter{Meter: temp.Id, Timestamp: at.Unix(), SocTemp: &recorded}).Error)
	temps, err := QueryTemperatureHistory(at)
	require.NoError(t, err)
	assert.Equal(t, map[int64]float64{at.Unix(): 1.5, at.Add(15 * time.Minute).Unix(): -4}, temps, "a recorded reading wins over the backfill")
}

func TestHomeForecastNonHeatPump(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)
	home, hp := heatPumpDB(t)
	at := time.Date(2026, 2, 10, 12, 0, 0, 0, loc)

	// household 0.5 kWh per slot of which the heat pump 0.2; the profile must see 300 Wh
	insert := func(entity entity, ts time.Time, v float64) {
		require.NoError(t, db.Instance.Create(&meter{Meter: entity.Id, Timestamp: ts.Unix(), Energy: v}).Error)
	}
	var negative, unpaired time.Time
	for ts := at.AddDate(0, 0, -30); ts.Before(at); ts = ts.Add(tariff.SlotDuration) {
		switch {
		case ts.Equal(at.AddDate(0, 0, -3).Add(2 * time.Hour)):
			negative = ts
			insert(home, ts, 0.1)
			insert(hp, ts, 0.2)
		case ts.Equal(at.AddDate(0, 0, -4).Add(2 * time.Hour)):
			unpaired = ts
			insert(home, ts, 0.5)
		default:
			insert(home, ts, 0.5)
			insert(hp, ts, 0.2)
		}
	}
	require.False(t, negative.IsZero() || unpaired.IsZero())

	to := at.Add(24 * time.Hour)
	res, stats, err := HomeForecastNonHeatPump(at, to, hp.Id)
	require.NoError(t, err)
	require.Len(t, res.Rates, 96)
	for _, r := range res.Rates {
		assert.InDelta(t, 300, r.Base, 1e-9, "Wh: household minus heat pump")
		assert.LessOrEqual(t, r.Low, r.Base)
		assert.GreaterOrEqual(t, r.High, r.Base)
	}
	assert.Equal(t, 1, stats.Dropped, "the negative residual is dropped and counted")
	assert.Equal(t, 1, stats.Unpaired, "a household slot without a heat pump reading is neither used nor zero")
	assert.Equal(t, 30*96-1, stats.Pairs)
	assert.Equal(t, 30*96-2, res.Quality.Samples)

	plain, err := HomeForecast(at, to)
	require.NoError(t, err)
	assert.InDelta(t, 500, plain.Rates[0].Base, 1e-9, "the household forecast is unchanged")

	t.Run("a flagged heat pump slot is not zero", func(t *testing.T) {
		require.NoError(t, db.Instance.Model(&meter{}).Where("meter = ? AND ts = ?", hp.Id, at.Add(-time.Hour).Unix()).
			Updates(map[string]any{"energy": 0, "incomplete": true}).Error)
		_, stats, err := HomeForecastNonHeatPump(at, to, hp.Id)
		require.NoError(t, err)
		assert.Equal(t, 2, stats.Unpaired)
	})
}
