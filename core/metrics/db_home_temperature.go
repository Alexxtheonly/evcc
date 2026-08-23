package metrics

import (
	"time"

	"github.com/evcc-io/evcc/server/db"
)

// TemperatureSample pairs one slot's Home meter energy (Wh) with the temperature (°C)
// recorded for the same slot, for fitting a heating-degree correction to the home load
// profile (see core.fitHeatingDegree).
type TemperatureSample struct {
	Energy      float64
	Temperature float64
}

// QueryHomeTemperatureSamples joins the Home meter's slot energy against the Temperature
// meter's slot reading for slots from "from" onward. The join is an inner join on purpose: a
// slot with no matching temperature reading (tariff not configured at the time, or a gap) is
// silently skipped rather than treated as "no signal," which would bias the fit toward
// whichever slots happen to have both readings anyway - it changes nothing either way, this
// just makes the intent explicit instead of relying on the join's default behavior.
//
// Excludes the same rows energyProfile does, for the same reasons: a recovered row dumps a
// whole downtime period's energy into a single 15min slot (see Collector.advanceSlot), and an
// incomplete row's energy comes from a power reading that failed mid-slot - both would inject
// an outlier the OLS fit in fitHeatingDegree has no resistance to. COALESCE guards the same
// legacy-NULL-energy rows energyProfile guards against.
//
// No GROUP BY/SUM unlike QueryLeadTimeSamples: both Home and Temperature are always
// singleton virtual meters (see the single NewCollector(metrics.Home, metrics.Home, ...) and
// NewCollector(metrics.Temperature, metrics.Temperature, ...) call sites, each the only
// entity ever created for its group/name pair), and meters has a unique (meter, ts) index, so
// this join can never fan out to more than one row per h.ts.
func QueryHomeTemperatureSamples(from time.Time) ([]TemperatureSample, error) {
	var res []TemperatureSample

	err := db.Instance.Table("meters h").
		Select(`COALESCE(h.energy, 0) * 1000 AS energy, t.soc_temp AS temperature`). // kWh -> Wh
		Joins(`JOIN entities eh ON eh.id = h.meter AND eh."group" = ?`, Home).
		Joins(`JOIN meters t ON t.ts = h.ts`).
		Joins(`JOIN entities et ON et.id = t.meter AND et."group" = ?`, Temperature).
		Where("h.ts >= ? AND t.soc_temp IS NOT NULL", from.Unix()).
		Where("COALESCE(h.recovered, 0) = 0 AND COALESCE(h.incomplete, 0) = 0").
		Scan(&res).Error

	return res, err
}
