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
func QueryHomeTemperatureSamples(from time.Time) ([]TemperatureSample, error) {
	var res []TemperatureSample

	err := db.Instance.Table("meters h").
		Select(`h.energy * 1000 AS energy, t.soc_temp AS temperature`). // kWh -> Wh
		Joins(`JOIN entities eh ON eh.id = h.meter AND eh."group" = ?`, Home).
		Joins(`JOIN meters t ON t.ts = h.ts`).
		Joins(`JOIN entities et ON et.id = t.meter AND et."group" = ?`, Temperature).
		Where("h.ts >= ? AND t.soc_temp IS NOT NULL", from.Unix()).
		Scan(&res).Error

	return res, err
}
