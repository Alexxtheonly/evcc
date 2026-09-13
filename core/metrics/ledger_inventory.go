package metrics

import (
	"context"
	"errors"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"gorm.io/gorm"
)

// InventoryAdjustedChain neutralizes stored energy borrowed across period boundaries.
type InventoryAdjustedChain struct {
	EstimatedEndpoints int            `json:"estimatedEndpoints"`
	MeasurementCaveat  string         `json:"measurementCaveat"`
	Status             string         `json:"status"`
	Reason             string         `json:"reason,omitempty"`
	Worlds             []WorldCost    `json:"worlds,omitempty"`
	Contributions      []Contribution `json:"contributions,omitempty"`
	Control            *ControlSplit  `json:"control,omitempty"`
	TerminalAdjustment Settled        `json:"terminalAdjustment"`
	WearAdjustment     *float64       `json:"wearAdjustment,omitempty"`
	Segments           int            `json:"segments"`
	AssumptionsSource  string         `json:"assumptionsSource"`
	Valuation          string         `json:"valuation"`
}

func computeInventoryAdjusted(ctx context.Context, set *ledgerSlotSet, fallback batteryPhysics) (*InventoryAdjustedChain, error) {
	res := &InventoryAdjustedChain{Status: "unavailable", AssumptionsSource: "legacy_assumed_efficiency_and_observed_limits", Valuation: "stored DC energy valued at last nonnegative grid price times discharge efficiency; period-average uses nonnegative mean price"}
	if len(set.Slots) == 0 {
		res.Reason = "no_priced_slots"
		return res, nil
	}
	first, last := set.Slots[0].Start, set.Slots[len(set.Slots)-1].Start
	var controls []controlSlot
	if err := db.Instance.WithContext(ctx).Where("ts >= ? AND ts <= ?", first.Unix(), last.Unix()).Find(&controls).Error; err != nil {
		return nil, err
	}
	snapshots := make(map[uint64]*OptimizerSnapshot)
	bySlot := make(map[int64]*uint64)
	for _, r := range controls {
		if r.SnapshotUnavailable {
			res.Reason = "historical_snapshot_expired_or_deleted"
			return res, nil
		}
		bySlot[r.Timestamp] = r.OptimizerSnapshotID
		if r.OptimizerSnapshotID == nil {
			continue
		}
		id := *r.OptimizerSnapshotID
		if _, ok := snapshots[id]; ok {
			continue
		}
		s, err := GetOptimizerSnapshot(id)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		snapshots[id] = s
	}
	physics := make([]batteryPhysics, len(set.Slots))
	historical := 0
	for i, s := range set.Slots {
		p := fallback
		if id := bySlot[s.Start.Unix()]; id != nil {
			var ok bool
			p, _, ok = snapshotPhysics(snapshots[*id], fallback)
			if !ok {
				res.Reason = "unsupported_historical_economics"
				return res, nil
			}
			historical++
		}
		physics[i] = p
	}
	if historical == len(set.Slots) {
		res.AssumptionsSource = "optimizer_snapshots"
	} else if historical > 0 {
		res.AssumptionsSource = "mixed_snapshots_and_explicit_legacy_assumptions"
	}
	endpoints := make(map[int64]float64)
	var measured []meter
	if err := db.Instance.Table("meters m").Select("m.ts,m.soc_temp").Joins(`JOIN entities e ON e.id=m.meter AND e."group" = ?`, Battery).Where("m.ts > ? AND m.ts <= ? AND m.soc_temp IS NOT NULL AND COALESCE(m.recovered,0)=0 AND COALESCE(m.incomplete,0)=0", first.Unix(), last.Add(tariff.SlotDuration).Unix()).Find(&measured).Error; err != nil {
		return nil, err
	}
	for _, r := range measured {
		if r.SocTemp != nil && finite(*r.SocTemp) && *r.SocTemp >= 0 && *r.SocTemp <= 100 {
			endpoints[r.Timestamp] = *r.SocTemp / 100
		}
	}
	return inventoryAdjustedFromSlots(set.Slots, physics, res, endpoints), nil
}

func inventoryAdjustedFromSlots(slots []slotData, physics []batteryPhysics, res *InventoryAdjustedChain, endpoints map[int64]float64) *InventoryAdjustedChain {
	res.MeasurementCaveat = "household and PV balance is approximate when meter AC/DC measurement planes differ; measured SoC endpoints take precedence over energy-based estimates"
	w2 := make([]worldFlow, len(slots))
	var sumGrid float64
	for _, s := range slots {
		sumGrid += s.PriceGrid
	}
	meanGrid := sumGrid / float64(len(slots))
	var baselineCorrection, actualCorrection Settled
	var baselineWear, actualWear float64
	wearKnown := true
	var soc, start, actualEnd float64
	var prev time.Time
	closeSegment := func(i int) {
		p := physics[i]
		rate := max(0, slots[i].PriceGrid) * p.EtaD
		meanRate := max(0, meanGrid) * p.EtaD
		baselineCorrection.PerSlot += (start - soc) * rate
		actualCorrection.PerSlot += (start - actualEnd) * rate
		baselineCorrection.PeriodAverage += (start - soc) * meanRate
		actualCorrection.PeriodAverage += (start - actualEnd) * meanRate
	}
	for i, s := range slots {
		p := physics[i]
		if s.BatterySocFrac == nil {
			res.Status = "unavailable"
			res.Reason = "missing_soc"
			return res
		}
		newSegment := i == 0 || !s.Start.Equal(prev.Add(tariff.SlotDuration)) || !equalPhysics(p, physics[max(0, i-1)])
		if newSegment {
			if i > 0 {
				closeSegment(i - 1)
			}
			start = *s.BatterySocFrac * p.CapacityKWh
			soc = start
			res.Segments++
		}
		next, flow := simulateNormalStep(s.modelledLoadKWh(), s.PVKWh, soc, p)
		w2[i] = flow
		if p.WearPerKWh == nil {
			wearKnown = false
		} else {
			baselineWear += max(0, soc-next) * *p.WearPerKWh
			discharge := s.BatteryDischargeKWh / p.EtaD
			if p.MeasurementPlane == "dc" {
				discharge = s.BatteryDischargeKWh
			}
			actualWear += discharge * *p.WearPerKWh
		}
		soc = next
		if endpoint, ok := endpoints[s.Start.Add(tariff.SlotDuration).Unix()]; ok {
			actualEnd = endpoint * p.CapacityKWh
		} else {
			res.EstimatedEndpoints++
			if p.MeasurementPlane == "dc" {
				actualEnd = *s.BatterySocFrac*p.CapacityKWh + s.BatteryChargeKWh - s.BatteryDischargeKWh
			} else {
				actualEnd = *s.BatterySocFrac*p.CapacityKWh + s.BatteryChargeKWh*p.EtaC - s.BatteryDischargeKWh/p.EtaD
			}
		}
		prev = s.Start
	}
	closeSegment(len(slots) - 1)
	res.Worlds = []WorldCost{{Label: "W0", Settled: settleFlows(slots, computeW0(slots))}, {Label: "W1", Settled: settleFlows(slots, computeW1(slots))}, {Label: "W2", Settled: settleFlows(slots, w2)}, {Label: "W3", Settled: settleFlows(slots, actualFlows(slots))}}
	addCost := func(i int, c Settled, wear float64) {
		res.Worlds[i].Settled.PerSlot += c.PerSlot + wear
		res.Worlds[i].Settled.PeriodAverage += c.PeriodAverage + wear
	}
	if !wearKnown {
		baselineWear = 0
		actualWear = 0
	} else {
		w := baselineWear - actualWear
		res.WearAdjustment = &w
	}
	addCost(2, baselineCorrection, baselineWear)
	addCost(3, actualCorrection, actualWear)
	res.TerminalAdjustment = diffSettled(baselineCorrection, actualCorrection)
	res.Contributions = []Contribution{{Label: "PV", Settled: diffSettled(res.Worlds[0].Settled, res.Worlds[1].Settled)}, {Label: "Battery", Settled: diffSettled(res.Worlds[1].Settled, res.Worlds[2].Settled)}, {Label: "Control", Settled: diffSettled(res.Worlds[2].Settled, res.Worlds[3].Settled)}}
	c := res.Contributions[2].Settled
	res.Control = &ControlSplit{Full: c.PerSlot, Routing: c.PeriodAverage, Timing: c.PerSlot - c.PeriodAverage}
	res.Status = "estimated"
	if res.Segments > 1 {
		res.Status = "partial"
		res.Reason = "separate_inventory_neutral_segments_at_gaps_or_assumption_changes"
	}
	return res
}

func equalPhysics(a, b batteryPhysics) bool {
	wearEqual := a.WearPerKWh == nil && b.WearPerKWh == nil || a.WearPerKWh != nil && b.WearPerKWh != nil && *a.WearPerKWh == *b.WearPerKWh
	return a.MeasurementPlane == b.MeasurementPlane && a.CapacityKWh == b.CapacityKWh && a.EtaC == b.EtaC && a.EtaD == b.EtaD && a.FloorFrac == b.FloorFrac && a.MaxChargeKWh == b.MaxChargeKWh && a.MaxDischargeKWh == b.MaxDischargeKWh && wearEqual
}
