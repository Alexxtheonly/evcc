package metrics

import (
	"errors"
	"fmt"
	"time"

	"github.com/evcc-io/evcc/server/db"
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
	rows, err := db.Query(`SELECT CAST(strftime('%H', ts, 'unixepoch', 'localtime') AS INTEGER) * 4 + CAST(strftime('%M', ts, 'unixepoch', 'localtime') AS INTEGER) / 15 AS bucket, avg(energy) AS energy, count(*) AS n
		FROM meters
		WHERE meter = ? AND ts >= ? AND energy IS NOT NULL AND energy >= 0 AND COALESCE(recovered, 0) = 0 AND COALESCE(incomplete, 0) = 0
		GROUP BY strftime("%H:%M", ts, 'unixepoch', 'localtime')
		ORDER BY strftime("%H:%M", ts, 'unixepoch', 'localtime') ASC`,
		entity.Id, from.Unix(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var samples int
	var res [96]float64
	var present [96]bool

	for rows.Next() {
		var bucket int
		var val float64
		var n int

		if err := rows.Scan(&bucket, &val, &n); err != nil {
			return nil, err
		}
		if bucket >= 0 && bucket < 96 && finite(val) {
			samples += n
			res[bucket] = val
			present[bucket] = true
		}
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	missing := 0
	for _, ok := range present {
		if !ok {
			missing++
		}
	}
	if missing > 4 || samples < energyProfileMinSamples {
		return nil, fmt.Errorf("%w: %d clean samples, %d/96 buckets", ErrIncomplete, samples, 96-missing)
	}
	for i, ok := range present {
		if ok {
			continue
		}
		left, right := (i+95)%96, (i+1)%96
		if !present[left] || !present[right] {
			return nil, fmt.Errorf("%w: adjacent missing buckets near %02d:%02d", ErrIncomplete, i/4, i%4*15)
		}
		res[i] = (res[left] + res[right]) / 2
	}
	return &res, nil
}
