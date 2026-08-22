package metrics

import (
	"time"

	"github.com/evcc-io/evcc/server/db"
)

// BatteryPowerSamples returns non-zero, non-recovered, non-incomplete per-slot charge and
// discharge energy (kWh, matching the meters table and EnergyProfile) since from, for
// percentile-based power limit derivation (see core.Site.batteryPowerLimits). charge is read
// from the return_energy column, discharge from energy - see the battery accumulation
// convention in core/site.go's updateBatteryMeters (charging = import = return_energy,
// discharging = export = energy). Recovered downtime-catchup slots and slots with an
// incomplete reading are excluded, the same way they already are from LastSlotEnergy and the
// household demand profile.
func (c *Collector) BatteryPowerSamples(from time.Time) (charge, discharge []float64, err error) {
	sqlDB, err := db.Instance.DB()
	if err != nil {
		return nil, nil, err
	}

	rows, err := sqlDB.Query(`SELECT energy, return_energy FROM meters
		WHERE meter = ? AND ts >= ? AND COALESCE(recovered, 0) = 0 AND COALESCE(incomplete, 0) = 0`,
		c.entity.Id, from.Unix())
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var energy, returnEnergy float64
		if err := rows.Scan(&energy, &returnEnergy); err != nil {
			return nil, nil, err
		}
		if energy > 0 {
			discharge = append(discharge, energy)
		}
		if returnEnergy > 0 {
			charge = append(charge, returnEnergy)
		}
	}

	return charge, discharge, rows.Err()
}
