package metrics

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/evcc-io/evcc/tariff"
)

type ImportPriceMetric struct {
	ImportKWh     float64  `json:"importKWh"`
	ImportCost    float64  `json:"importCost"`
	WeightedPrice *float64 `json:"weightedPrice,omitempty"`
	Coverage      Coverage `json:"coverage"`
}

type ConsumptionPriceMetric struct {
	ConsumptionKWh float64  `json:"consumptionKWh"`
	ImportKWh      float64  `json:"importKWh"`
	ExportKWh      float64  `json:"exportKWh"`
	ImportCost     float64  `json:"importCost"`
	ExportRevenue  float64  `json:"exportRevenue"`
	NetCost        float64  `json:"netCost"`
	EffectivePrice *float64 `json:"effectivePrice,omitempty"`
	Coverage       Coverage `json:"coverage"`
}

type WorldPriceMetric struct {
	Label string `json:"label"`
	ConsumptionPriceMetric
	WeightedPrice *float64 `json:"weightedPrice,omitempty"`
}

type EnergyPriceBucket struct {
	Date             string                 `json:"date"`
	From             time.Time              `json:"from"`
	To               time.Time              `json:"to"`
	Grid             ImportPriceMetric      `json:"grid"`
	Consumption      ConsumptionPriceMetric `json:"consumption"`
	Worlds           []WorldPriceMetric     `json:"worlds"`
	EVTiming         EVTimingPrice          `json:"evTiming"`
	InventoryBenefit *float64               `json:"inventoryBenefit,omitempty"`
}

type EnergyPriceComparison struct {
	Status            string                  `json:"status"`
	Reason            string                  `json:"reason,omitempty"`
	InventoryAdjusted *InventoryAdjustedChain `json:"inventoryAdjusted,omitempty"`
	Notes             []string                `json:"notes"`
}

type EVTimingPrice struct {
	Status              string   `json:"status"`
	Reason              string   `json:"reason,omitempty"`
	IncludedSessions    int      `json:"includedSessions"`
	ExcludedSessions    int      `json:"excludedSessions"`
	EnergyKWh           *float64 `json:"energyKWh,omitempty"`
	ActualTariffCost    *float64 `json:"actualTariffCost,omitempty"`
	ImmediateTariffCost *float64 `json:"immediateTariffCost,omitempty"`
	Savings             *float64 `json:"savings,omitempty"`
}

type EnergyPriceReport struct {
	Month       string                `json:"month"`
	Timezone    string                `json:"timezone"`
	Today       string                `json:"today"`
	From        time.Time             `json:"from"`
	To          time.Time             `json:"to"`
	EvaluatedTo time.Time             `json:"evaluatedTo"`
	Earliest    *time.Time            `json:"earliest,omitempty"`
	Totals      EnergyPriceBucket     `json:"totals"`
	Days        []EnergyPriceBucket   `json:"days"`
	Comparison  EnergyPriceComparison `json:"comparison"`
	EVTiming    EVTimingPrice         `json:"evTiming"`
}

// ErrEnergyPriceMonth rejects malformed and future calendar requests.
var ErrEnergyPriceMonth = errors.New("month must be YYYY-MM and not in the future")

// ComputeEnergyPrices uses one calendar-month trajectory for every daily bucket.
// Ratios use only the cost and energy of the same valid slots; absent prices and
// meters are exclusions, never assumed zero. now is explicit for boundary tests.
func ComputeEnergyPrices(ctx context.Context, month string, now time.Time, feedInStatic *float64) (*EnergyPriceReport, error) {
	loc := now.Location()
	if month == "" {
		month = now.Format("2006-01")
	}
	from, err := time.ParseInLocation("2006-01", month, loc)
	if err != nil || from.After(now) {
		return nil, ErrEnergyPriceMonth
	}
	to := from.AddDate(0, 1, 0)
	end := now.Truncate(tariff.SlotDuration)
	if to.Before(end) {
		end = to
	}
	report := &EnergyPriceReport{Month: month, Timezone: reportTimezone(loc, now), Today: now.Format("2006-01-02"), From: from, To: to, EvaluatedTo: end, Days: []EnergyPriceBucket{}, Comparison: EnergyPriceComparison{Status: "unavailable", Reason: "no_priced_slots", Notes: []string{noteInvoiceComparability, noteEVTimingUnattributed, noteMeterResidual}}, EVTiming: EVTimingPrice{Status: "unavailable", Reason: "historical_evidence_unavailable"}}
	report.Totals = newEnergyPriceBucket(month, from, end)
	for day := from; day.Before(end); day = day.AddDate(0, 0, 1) {
		dayEnd := day.AddDate(0, 0, 1)
		if dayEnd.After(end) {
			dayEnd = end
		}
		report.Days = append(report.Days, newEnergyPriceBucket(day.Format("2006-01-02"), day, dayEnd))
	}
	earliest, err := EarliestTariffSlot(ctx)
	if err != nil {
		return nil, err
	}
	if !earliest.IsZero() {
		report.Earliest = &earliest
	}
	if !end.After(from) {
		return report, nil
	}
	type groupData struct {
		rows    map[int64]groupSlotRow
		members groupMembership
		present bool
	}
	groups := make(map[string]groupData)
	for _, name := range []string{Grid, Home, Loadpoint, PV, Battery} {
		rows, members, present, err := queryGroupSlotsCoverage(ctx, name, from, end, true)
		if err != nil {
			return nil, err
		}
		groups[name] = groupData{rows, members, present}
	}
	prices, err := queryTariffSlots(ctx, from, end)
	if err != nil {
		return nil, err
	}
	fallback, err := feedInFallback(ctx, feedInStatic)
	if err != nil {
		return nil, err
	}
	set := &ledgerSlotSet{TotalSlots: report.Totals.Grid.Coverage.TotalSlots, HasPV: groups[PV].present, HasBattery: groups[Battery].present, HasLoadpoint: groups[Loadpoint].present}
	valid := func(name string, ts int64, required bool) (groupSlotRow, bool) {
		g := groups[name]
		if !g.present {
			return groupSlotRow{}, !required
		}
		if g.members.at(ts) == 0 {
			// Before any loadpoint reported, its energy was still inside the residual
			// home meter, so zero keeps the load balance. Absent PV or battery evidence
			// cannot be read as absent hardware.
			return groupSlotRow{}, name == Loadpoint
		}
		r, ok := g.rows[ts]
		return r, ok && !r.Excluded && !r.InvalidEnergy && r.EnergyCount == r.Count && finite(r.Energy) && r.Energy >= 0
	}
	dayIndex := 0
	for ts := from; ts.Before(end); ts = ts.Add(tariff.SlotDuration) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for !ts.Before(report.Days[dayIndex].To) {
			dayIndex++
		}
		day := &report.Days[dayIndex]
		p, ok := prices[ts.Unix()]
		if !ok || !finite(p.Grid) {
			continue
		}
		g, ok := valid(Grid, ts.Unix(), true)
		if !ok || !finite(g.Energy*p.Grid) || !finite(report.Totals.Grid.ImportCost+g.Energy*p.Grid) || !finite(report.Totals.Grid.ImportKWh+g.Energy) {
			continue
		}
		for _, bucket := range []*EnergyPriceBucket{day, &report.Totals} {
			bucket.Grid.ImportKWh += g.Energy
			bucket.Grid.ImportCost += g.Energy * p.Grid
			bucket.Grid.Coverage.ValidSlots++
		}
		if g.InvalidReturnEnergy || g.ReturnEnergyCount != g.Count || !finite(g.ReturnEnergy) || g.ReturnEnergy < 0 {
			continue
		}
		h, ok := valid(Home, ts.Unix(), true)
		if !ok {
			continue
		}
		lp, ok := valid(Loadpoint, ts.Unix(), false)
		if !ok {
			continue
		}
		feed := p.FeedIn
		if feed == nil {
			feed = fallback
		}
		if g.ReturnEnergy > 0 && (feed == nil || !finite(*feed)) {
			continue
		}
		s := slotData{Start: ts, GridImportKWh: g.Energy, GridExportKWh: g.ReturnEnergy, HomeKWh: h.Energy, LoadpointKWh: lp.Energy, PriceGrid: p.Grid}
		if feed != nil && finite(*feed) {
			s.PriceFeedIn = *feed
		}
		for _, bucket := range []*EnergyPriceBucket{day, &report.Totals} {
			addConsumption(&bucket.Consumption, s, worldFlow{ImportKWh: g.Energy, ExportKWh: g.ReturnEnergy})
		}
		pv, ok := valid(PV, ts.Unix(), false)
		if !ok {
			continue
		}
		s.PVKWh = pv.Energy
		// Counterfactual worlds may export even when the actual site did not.
		if s.PVKWh > s.modelledLoadKWh() && (feed == nil || !finite(*feed)) {
			continue
		}
		if set.HasBattery {
			b, ok := valid(Battery, ts.Unix(), true)
			if !ok || b.InvalidSoc || b.InvalidReturnEnergy || b.SocCount != b.Count || b.ReturnEnergyCount != b.Count || !finite(b.ReturnEnergy) || b.ReturnEnergy < 0 || !b.SocFrac.Valid || !finite(b.SocFrac.Float64) || b.SocFrac.Float64 < 0 || b.SocFrac.Float64 > 100 {
				continue
			}
			soc := b.SocFrac.Float64 / 100
			s.BatterySocFrac = &soc
			s.BatteryChargeKWh = b.Energy
			s.BatteryDischargeKWh = b.ReturnEnergy
		}
		set.Slots = append(set.Slots, s)
	}
	for i := range report.Days {
		finishEnergyPriceBucket(&report.Days[i])
	}
	finishEnergyPriceBucket(&report.Totals)
	if err := populatePriceWorlds(ctx, report, set); err != nil {
		return nil, err
	}
	report.EVTiming, err = computeEVTimingPrices(ctx, report, prices)
	if err != nil {
		return nil, err
	}
	report.Totals.EVTiming = report.EVTiming
	return report, nil
}

func newEnergyPriceBucket(date string, from, to time.Time) EnergyPriceBucket {
	total := int(to.Sub(from) / tariff.SlotDuration)
	return EnergyPriceBucket{Date: date, From: from, To: to, Grid: ImportPriceMetric{Coverage: Coverage{TotalSlots: total}}, Consumption: ConsumptionPriceMetric{Coverage: Coverage{TotalSlots: total}}, Worlds: []WorldPriceMetric{}, EVTiming: EVTimingPrice{Status: "unavailable", Reason: "no_completed_sessions"}}
}

func addConsumption(m *ConsumptionPriceMetric, s slotData, flow worldFlow) {
	m.ConsumptionKWh += s.modelledLoadKWh()
	m.ImportKWh += flow.ImportKWh
	m.ExportKWh += flow.ExportKWh
	m.ImportCost += flow.ImportKWh * s.PriceGrid
	m.ExportRevenue += flow.ExportKWh * s.PriceFeedIn
	m.NetCost = m.ImportCost - m.ExportRevenue
	m.Coverage.ValidSlots++
}
func finishCoverage(c *Coverage) {
	if c.TotalSlots > 0 {
		c.Fraction = float64(c.ValidSlots) / float64(c.TotalSlots)
	}
}
func priceRatio(cost, energy float64) *float64 {
	if energy <= 0 {
		return nil
	}
	v := cost / energy
	return &v
}
func finishEnergyPriceBucket(b *EnergyPriceBucket) {
	finishCoverage(&b.Grid.Coverage)
	finishCoverage(&b.Consumption.Coverage)
	b.Grid.WeightedPrice = priceRatio(b.Grid.ImportCost, b.Grid.ImportKWh)
	b.Consumption.EffectivePrice = priceRatio(b.Consumption.NetCost, b.Consumption.ConsumptionKWh)
	for i := range b.Worlds {
		w := &b.Worlds[i]
		finishCoverage(&w.Coverage)
		w.WeightedPrice = priceRatio(w.ImportCost, w.ImportKWh)
		w.EffectivePrice = priceRatio(w.NetCost, w.ConsumptionKWh)
	}
}

func populatePriceWorlds(ctx context.Context, report *EnergyPriceReport, set *ledgerSlotSet) error {
	if len(set.Slots) == 0 {
		return nil
	}
	flows := [][]worldFlow{computeW0(set.Slots), computeW1(set.Slots), computeW1(set.Slots), actualFlows(set.Slots)}
	var replay *inventoryReplay
	if set.HasBattery {
		phys, err := deriveBatteryPhysics(ctx)
		if err != nil {
			if errors.Is(err, ErrBatteryPhysicsUnavailable) || errors.Is(err, ErrBatteryRateCeilingUnavailable) {
				report.Comparison.Reason = "battery_physics_unavailable"
				return nil
			}
			return err
		}
		if !phys.HasChargeEvidence || !phys.HasDischargeEvidence {
			report.Comparison.Reason = "battery_physics_unavailable"
			return nil
		}
		flows[2], _, err = computeW2(set.Slots, phys)
		if err != nil {
			return err
		}
		replay, err = loadInventoryReplay(ctx, set, phys)
		if err != nil {
			return err
		}
		report.Comparison.InventoryAdjusted = replay.through(len(set.Slots))
	}
	for _, bucket := range append([]*EnergyPriceBucket{&report.Totals}, priceBucketPointers(report.Days)...) {
		for i := range flows {
			bucket.Worlds = append(bucket.Worlds, WorldPriceMetric{Label: fmt.Sprintf("W%d", i), ConsumptionPriceMetric: ConsumptionPriceMetric{Coverage: Coverage{TotalSlots: bucket.Grid.Coverage.TotalSlots}}})
		}
	}
	di := 0
	prefix := 0
	previousBenefit := 0.0
	for i, s := range set.Slots {
		for !s.Start.Before(report.Days[di].To) {
			if replay != nil {
				applyInventoryBenefit(&report.Days[di], replay, prefix, &previousBenefit)
			}
			di++
		}
		for w := range flows {
			for _, bucket := range []*EnergyPriceBucket{&report.Totals, &report.Days[di]} {
				addConsumption(&bucket.Worlds[w].ConsumptionPriceMetric, s, flows[w][i])
			}
		}
		prefix = i + 1
	}
	for di < len(report.Days) {
		if replay != nil {
			applyInventoryBenefit(&report.Days[di], replay, prefix, &previousBenefit)
		}
		di++
	}
	if adj := report.Comparison.InventoryAdjusted; adj != nil && adj.Control != nil {
		v := adj.Control.Full
		report.Totals.InventoryBenefit = &v
	}
	report.Comparison.Status = "available"
	report.Comparison.Reason = ""
	if len(set.Slots) < set.TotalSlots {
		report.Comparison.Status = "partial"
		report.Comparison.Reason = "incomplete_coverage"
	}
	for i := range report.Days {
		finishEnergyPriceBucket(&report.Days[i])
	}
	finishEnergyPriceBucket(&report.Totals)
	return nil
}
func priceBucketPointers(b []EnergyPriceBucket) []*EnergyPriceBucket {
	res := make([]*EnergyPriceBucket, len(b))
	for i := range b {
		res[i] = &b[i]
	}
	return res
}
func applyInventoryBenefit(bucket *EnergyPriceBucket, replay *inventoryReplay, n int, previous *float64) {
	if len(bucket.Worlds) == 0 || bucket.Worlds[0].Coverage.ValidSlots == 0 {
		return
	}
	adj := replay.through(n)
	if adj.Control == nil {
		return
	}
	v := adj.Control.Full - *previous
	bucket.InventoryBenefit = &v
	*previous = adj.Control.Full
}

// localZoneName resolves the IANA name behind Go's "Local" placeholder, which Go
// uses when TZ is unset and /etc/localtime is read. Empty when unknown.
var localZoneName = func() string {
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(target, "zoneinfo/"); ok {
			return name
		}
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

// reportTimezone names the site calendar zone in a form browsers accept: browsers
// reject "Local" (Intl RangeError). An IANA name is used when it resolves to the
// same offset as loc at now; otherwise the offset at now, as Etc/GMT for whole
// hours (note the POSIX-inverted sign) or "+hh:mm".
func reportTimezone(loc *time.Location, now time.Time) string {
	name := loc.String()
	if name == "Local" {
		name = localZoneName()
	}
	_, offset := now.In(loc).Zone()
	if name != "" && name != "Local" {
		if named, err := time.LoadLocation(name); err == nil {
			if _, o := now.In(named).Zone(); o == offset {
				return name
			}
		}
	}
	switch {
	case offset == 0:
		return "UTC"
	case offset%3600 == 0:
		return fmt.Sprintf("Etc/GMT%+d", -offset/3600)
	}
	sign := '+'
	if offset < 0 {
		sign, offset = '-', -offset
	}
	return fmt.Sprintf("%c%02d:%02d", sign, offset/3600, offset%3600/60)
}
