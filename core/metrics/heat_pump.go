package metrics

import (
	"time"

	"github.com/evcc-io/evcc/server/db"
)

// Heat-pump history (ADR-015 amendment). The heat pump is an ext meter whose 15min slots land in
// meters like any other; the hourly Home Assistant history of the winter before it was added
// lives in meters_backfill and temperature_backfill. Both are written by contrib/hp-backfill,
// never by evcc, and read only by the heat-pump daily fit. The tables are created here so a
// read never fails on a fresh database; a database without them reads as no history.

// metersBackfill is one 15min share of a Home Assistant hour (kWh) of an ext meter.
type metersBackfill struct {
	Meter     int     `gorm:"column:meter;uniqueIndex:meters_backfill_meter_ts"`
	Timestamp int64   `gorm:"column:ts;uniqueIndex:meters_backfill_meter_ts"`
	Energy    float64 `gorm:"column:energy"`
	Source    string  `gorm:"column:source"`
}

func (metersBackfill) TableName() string { return "meters_backfill" }

// temperatureBackfill is one 15min share of an hourly outdoor temperature (°C).
type temperatureBackfill struct {
	Timestamp   int64   `gorm:"column:ts;uniqueIndex:temperature_backfill_ts"`
	Temperature float64 `gorm:"column:temperature"`
	Source      string  `gorm:"column:source"`
}

func (temperatureBackfill) TableName() string { return "temperature_backfill" }

// HeatPumpRow is a persisted 15min slot of the heat-pump meter (kWh). The flags are kept
// because a flagged slot is unknown, not zero.
type HeatPumpRow struct {
	Ts         int64
	Energy     float64
	Recovered  bool
	Incomplete bool
}

// Clean reports whether the slot is a measurement.
func (r HeatPumpRow) Clean() bool {
	return !r.Recovered && !r.Incomplete && finite(r.Energy) && r.Energy >= 0
}

// SlotValue is a value of the 15min slot starting at Ts.
type SlotValue struct {
	Ts    int64
	Value float64
}

// NonHeatPumpStats counts the household slots of the non-heat-pump profile window: Pairs have
// a clean household and heat-pump reading, Dropped of those would be negative, Unpaired have
// no clean heat-pump reading.
type NonHeatPumpStats struct {
	Pairs, Dropped, Unpaired int
}

// HeatPumpEntity resolves the ext meter ref to its entity id, false when no such meter exists.
func HeatPumpEntity(ref string) (int, bool, error) {
	var e entity
	res := db.Instance.Where(`"group" = ? AND name = ?`, Meter, ref).Limit(1).Find(&e)
	return e.Id, res.RowsAffected > 0, res.Error
}

// QueryHeatPumpRows returns the meter's rows from from, oldest first. Rows without energy
// are legacy and absent.
func QueryHeatPumpRows(id int, from time.Time) ([]HeatPumpRow, error) {
	var res []HeatPumpRow
	err := db.Instance.Raw(`SELECT ts, energy, COALESCE(recovered,0) AS recovered, COALESCE(incomplete,0) AS incomplete
		FROM meters WHERE meter = ? AND ts >= ? AND energy IS NOT NULL ORDER BY ts`, id, from.Unix()).Scan(&res).Error
	return res, err
}

// QueryHeatPumpBackfill returns the backfilled slots of the meter from from, oldest first.
func QueryHeatPumpBackfill(id int, from time.Time) ([]SlotValue, error) {
	if !db.Instance.Migrator().HasTable(metersBackfill{}.TableName()) {
		return nil, nil
	}
	var res []SlotValue
	err := db.Instance.Raw(`SELECT ts, energy AS value FROM meters_backfill WHERE meter = ? AND ts >= ? ORDER BY ts`, id, from.Unix()).Scan(&res).Error
	return res, err
}

// QueryTemperatureHistory returns the outdoor temperature per slot from from: backfilled
// slots, replaced by the recorded Temperature meter wherever it has a clean reading (not recovered, not incomplete).
func QueryTemperatureHistory(from time.Time) (map[int64]float64, error) {
	res := make(map[int64]float64)
	if db.Instance.Migrator().HasTable(temperatureBackfill{}.TableName()) {
		var rows []SlotValue
		if err := db.Instance.Raw(`SELECT ts, temperature AS value FROM temperature_backfill WHERE ts >= ? AND temperature IS NOT NULL`, from.Unix()).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			if finite(r.Value) {
				res[r.Ts] = r.Value
			}
		}
	}
	var rows []SlotValue
	if err := db.Instance.Raw(`SELECT m.ts AS ts, m.soc_temp AS value FROM meters m JOIN entities e ON e.id = m.meter AND e."group" = ?
		WHERE m.ts >= ? AND m.soc_temp IS NOT NULL AND COALESCE(m.recovered,0) = 0 AND COALESCE(m.incomplete,0) = 0`, Temperature, from.Unix()).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		if finite(r.Value) {
			res[r.Ts] = r.Value
		}
	}
	return res, nil
}

// HeatPumpFreshness returns the number of incomplete slots since since and the start of the
// newest slot with a reading, false when the meter has none.
func HeatPumpFreshness(id int, since time.Time) (incomplete int, newest time.Time, ok bool, err error) {
	var row struct {
		Incomplete int
		Newest     *int64
	}
	err = db.Instance.Raw(`SELECT
		(SELECT COUNT(*) FROM meters WHERE meter = ? AND ts >= ? AND COALESCE(incomplete,0) = 1) AS incomplete,
		(SELECT MAX(ts) FROM meters WHERE meter = ? AND energy IS NOT NULL AND COALESCE(incomplete,0) = 0) AS newest`,
		id, since.Unix(), id).Scan(&row).Error
	if err != nil || row.Newest == nil {
		return row.Incomplete, time.Time{}, false, err
	}
	return row.Incomplete, time.Unix(*row.Newest, 0), true, nil
}

// HomeForecastNonHeatPump is HomeForecast for the household energy without the heat pump
// entity: per slot the household reading minus the heat pump's. A slot without a clean heat
// pump reading, or whose difference would be negative, is not part of the profile. The range
// is the profile's historical variation, not calibrated against household forecast errors.
func HomeForecastNonHeatPump(from, to time.Time, heatPump int) (*HomeForecastResult, NonHeatPumpStats, error) {
	return homeForecast(from, to, &heatPump)
}

// subtractHeatPump returns the household rows [from, to) minus the heat pump entity's slots.
// Flagged household rows stay as they are, so the profile rejects them as before. stats
// cover the rows from cut on.
func subtractHeatPump(rows []meter, heatPump int, from, to time.Time, cut int64) ([]meter, NonHeatPumpStats, error) {
	var stats NonHeatPumpStats
	hp, err := QueryHeatPumpRows(heatPump, from)
	if err != nil {
		return nil, stats, err
	}
	byTs := make(map[int64]HeatPumpRow, len(hp))
	for _, r := range hp {
		if r.Ts < to.Unix() && r.Clean() {
			byTs[r.Ts] = r
		}
	}

	res := make([]meter, 0, len(rows))
	for _, r := range rows {
		if r.Recovered || r.Incomplete || !finite(r.Energy) || r.Energy < 0 {
			res = append(res, r)
			continue
		}
		h, ok := byTs[r.Timestamp]
		switch {
		case !ok:
			if r.Timestamp >= cut {
				stats.Unpaired++
			}
		case r.Energy-h.Energy < 0:
			if r.Timestamp >= cut {
				stats.Pairs++
				stats.Dropped++
			}
		default:
			if r.Timestamp >= cut {
				stats.Pairs++
			}
			r.Energy -= h.Energy
			res = append(res, r)
		}
	}
	return res, stats, nil
}
