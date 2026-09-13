package metrics

import (
	"fmt"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
)

// BatteryEfficiencyCandidate is an estimate, never an automatic setting change.
type BatteryEfficiencyCandidate struct {
	MeasurementPlane    string    `json:"measurementPlane"`
	Applicable          bool      `json:"applicable"`
	Name                string    `json:"name"`
	ChargeEfficiency    *float64  `json:"chargeEfficiency,omitempty"`
	DischargeEfficiency *float64  `json:"dischargeEfficiency,omitempty"`
	ChargeSamples       int       `json:"chargeSamples"`
	DischargeSamples    int       `json:"dischargeSamples"`
	RejectedSamples     int       `json:"rejectedSamples"`
	From                time.Time `json:"from"`
	To                  time.Time `json:"to"`
	CapacityKWh         float64   `json:"capacityKWh"`
	Source              string    `json:"source"`
}

// BatteryEfficiencyCandidates uses clean consecutive SoC measurements and independent capacity.
func BatteryEfficiencyCandidates(from time.Time) ([]BatteryEfficiencyCandidate, error) {
	var entities []entity
	if err := db.Instance.Where(`"group" = ?`, Battery).Find(&entities).Error; err != nil {
		return nil, err
	}
	res := make([]BatteryEfficiencyCandidate, 0, len(entities))
	for _, e := range entities {
		plane := e.MeasurementPlane
		if plane == "" {
			plane = "unknown"
		}
		c := BatteryEfficiencyCandidate{Name: e.Name, Source: "capacity_unavailable", MeasurementPlane: plane}
		if e.CapacityKWh == nil || !finite(*e.CapacityKWh) || *e.CapacityKWh <= 0 {
			res = append(res, c)
			continue
		}
		c.CapacityKWh = *e.CapacityKWh
		c.Source = "soc_to_meter_energy_unverified_plane"
		if plane == "ac" {
			c.Source = "configured_capacity_ac_meter_soc"
			c.Applicable = true
		}
		if plane == "dc" {
			c.Source = "dc_meter_pack_efficiency_not_ac_conversion"
		}
		var rows []meter
		if err := db.Instance.Where("meter = ? AND ts >= ?", e.Id, from.Unix()).Order("ts").Find(&rows).Error; err != nil {
			return nil, err
		}
		var charges, discharges []float64
		for i := 0; i+1 < len(rows); i++ {
			a, b := rows[i], rows[i+1]
			if b.Timestamp-a.Timestamp != int64(tariff.SlotDuration.Seconds()) || a.Recovered || a.Incomplete || b.Recovered || b.Incomplete || a.SocTemp == nil || b.SocTemp == nil || !finite(*a.SocTemp) || !finite(*b.SocTemp) || *a.SocTemp < 5 || *a.SocTemp > 95 || *b.SocTemp < 5 || *b.SocTemp > 95 || !finite(a.Energy) || !finite(a.ReturnEnergy) || a.Energy < 0 || a.ReturnEnergy < 0 {
				c.RejectedSamples++
				continue
			}
			delta := (*b.SocTemp - *a.SocTemp) / 100 * c.CapacityKWh
			// Two percentage points avoids fitting conversion losses to rounded SoC noise.
			var eta float64
			switch {
			case delta >= .02*c.CapacityKWh && a.ReturnEnergy >= .2 && a.Energy == 0:
				eta = delta / a.ReturnEnergy
			case delta <= -.02*c.CapacityKWh && a.Energy >= .2 && a.ReturnEnergy == 0:
				eta = a.Energy / -delta
			default:
				c.RejectedSamples++
				continue
			}
			if !finite(eta) || eta < .65 || eta > 1 {
				c.RejectedSamples++
				continue
			}
			if delta > 0 {
				charges = append(charges, eta)
			} else {
				discharges = append(discharges, eta)
			}
			if c.From.IsZero() {
				c.From = time.Unix(a.Timestamp, 0)
			}
			c.To = time.Unix(b.Timestamp, 0)
		}
		c.ChargeSamples = len(charges)
		c.DischargeSamples = len(discharges)
		if len(charges) >= 12 {
			v := Percentile(charges, .5)
			c.ChargeEfficiency = &v
		}
		if len(discharges) >= 12 {
			v := Percentile(discharges, .5)
			c.DischargeEfficiency = &v
		}
		res = append(res, c)
	}
	return res, nil
}

// SetBatteryMeasurementPlane records an explicit meter-plane declaration for calibration.
func SetBatteryMeasurementPlane(name, plane string) error {
	if plane != "ac" && plane != "dc" && plane != "unknown" {
		return fmt.Errorf("invalid battery measurement plane %q", plane)
	}
	if db.Instance == nil {
		return fmt.Errorf("metrics database unavailable")
	}
	return db.Instance.Model(new(entity)).Where(`"group" = ? AND name = ?`, Battery, name).Update("measurement_plane", plane).Error
}
