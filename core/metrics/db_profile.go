package metrics

import (
	"errors"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
)

var ErrIncomplete = errors.New("meter profile incomplete")

// energyProfileMinSamples is the minimum number of qualifying 15min rows across the whole
// lookback window required before energyProfile trusts the derived profile - 1.5*96, i.e. at
// least 1.5 days' worth. len(res)==96 below already guarantees at least one row per
// time-of-day bucket, so this only ever matters above 96 (a row count under 96 already fails
// the bucket-completeness check on its own); this guard exists for what that check alone
// misses - a mostly-failed month (flaky Modbus, meter offline) that still landed one thin row
// in each of the 96 buckets would pass len(res)==96 while being built from far less real data
// than the ~30-day average the caller assumes.
const energyProfileMinSamples = 144

// energyProfile returns a 15min average meter profile in Wh. The profile
// is sorted by timestamp starting at 00:00. It is guaranteed to contain 96 15min values.
func energyProfile(entity entity, from time.Time) (*[96]float64, error) {
	db, err := db.Instance.DB()
	if err != nil {
		return nil, err
	}

	// COALESCE guards against legacy rows with NULL energy
	rows, err := db.Query(`SELECT min(ts) AS ts, COALESCE(avg(energy), 0) AS energy, count(*) AS n
		FROM meters
		WHERE meter = ? AND ts >= ? AND COALESCE(recovered, 0) = 0 AND COALESCE(incomplete, 0) = 0
		GROUP BY strftime("%H:%M", ts, 'unixepoch', 'localtime')
		ORDER BY strftime("%H:%M", ts, 'unixepoch', 'localtime') ASC`,
		entity.Id, from.Unix(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var prev time.Time
	var samples int
	res := make([]float64, 0, 96)

	for rows.Next() {
		var ts SqlTime
		var val float64
		var n int

		if err := rows.Scan(&ts, &val, &n); err != nil {
			return nil, err
		}
		samples += n

		// interpolate single missing value, maybe due to regular restarts?
		if time.Time(ts).Sub(prev) == 2*tariff.SlotDuration {
			res = append(res, (val+res[len(res)-1])/2)
		}
		prev = time.Time(ts)

		res = append(res, val)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(res) != 96 || samples < energyProfileMinSamples {
		return nil, ErrIncomplete
	}

	return (*[96]float64)(res), nil
}
