package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
)

// MaxPlanHistoryHours bounds the measured window the energy plan chart may request.
const MaxPlanHistoryHours = 48

// ErrPlanHistoryRange rejects a window outside 1..MaxPlanHistoryHours.
var ErrPlanHistoryRange = errors.New("hours must be between 1 and 48")

// PlanHistory is the measured past in the energy plan's own slot/device shape, so the
// chart draws it with the same step helpers. Every value is measured; a value that was
// not recorded is null, never zero.
type PlanHistory struct {
	From     time.Time            `json:"from"`
	To       time.Time            `json:"to"`
	Forecast []PlanHistorySlot    `json:"forecast"`
	Devices  []PlanHistoryDevice  `json:"devices"`
	Unmapped []PlanHistoryUnknown `json:"unmapped,omitempty"`
}

// PlanHistorySlot is one complete 15min slot. Energies are Wh over the slot.
type PlanHistorySlot struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Measured bool      `json:"measured"`
	// Flagged marks a slot with a recovered (downtime catch-up) or incomplete reading
	// among its rows: rendered, but not as trustworthy as a clean slot.
	Flagged            bool     `json:"flagged,omitempty"`
	HomeWh             *float64 `json:"homeWh"`
	SolarWh            *float64 `json:"solarWh"`
	GridImportWh       *float64 `json:"gridImportWh"`
	GridExportWh       *float64 `json:"gridExportWh"`
	BatteryChargeWh    *float64 `json:"batteryChargeWh"`
	BatteryDischargeWh *float64 `json:"batteryDischargeWh"`
	LoadpointWh        *float64 `json:"loadpointWh"`
	// GridChargeWh is the battery charge the local surplus cannot explain:
	// max(0, charge - max(0, solar - home - loadpoints)), capped at grid import.
	GridChargeWh *float64            `json:"gridChargeWh"`
	GridPrice    *float64            `json:"gridPrice"`
	FeedInPrice  *float64            `json:"feedInPrice"`
	GridCost     *float64            `json:"gridCost"`      // import x grid price
	FeedInIncome *float64            `json:"feedInRevenue"` // export x feed-in price
	Planned      *PlanHistoryPlanned `json:"planned,omitempty"`
}

// PlanHistoryPlanned is the plan in force when the slot started: the slot's entry in
// the latest optimizer snapshot taken at or before the slot start.
type PlanHistoryPlanned struct {
	SnapshotID      uint64                     `json:"snapshotId"`
	Snapshot        time.Time                  `json:"snapshot"`
	GridChargeMinWh *float64                   `json:"gridChargeMinWh,omitempty"`
	GridChargeMaxWh *float64                   `json:"gridChargeMaxWh,omitempty"`
	Devices         []PlanHistoryPlannedDevice `json:"devices"`
}

// PlanHistoryPlannedDevice is one device's planned slot; Soc is the planned level at
// the slot END, as in the live plan.
type PlanHistoryPlannedDevice struct {
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	Kind        string   `json:"kind"`
	ChargeWh    float64  `json:"chargeWh"`
	DischargeWh float64  `json:"dischargeWh"`
	Soc         *float64 `json:"soc,omitempty"`
}

// PlanHistoryDevice is one measured device keyed like the plan (battery:<name>,
// loadpoint:<index>). Loadpoints carry charging energy only: their soc_temp is the
// last vehicle soc the loadpoint saw (or a temperature), which says nothing about
// whether that vehicle was still connected.
type PlanHistoryDevice struct {
	Key         string             `json:"key"`
	Name        string             `json:"name"`
	Title       string             `json:"title"`
	Kind        string             `json:"kind"`
	CapacityKWh float64            `json:"capacityKWh"`
	Plan        []PlanHistoryPoint `json:"plan"`
}

// PlanHistoryPoint is one measured device slot. Soc is the level at the slot START.
type PlanHistoryPoint struct {
	Start       time.Time `json:"start"`
	ChargeWh    *float64  `json:"chargeWh"`
	DischargeWh *float64  `json:"dischargeWh"`
	Soc         *float64  `json:"soc"`
	Flagged     bool      `json:"flagged,omitempty"`
}

// PlanHistoryUnknown names a loadpoint whose energy is measured but which cannot be
// tied to a plan device key (e.g. two loadpoints share a title).
type PlanHistoryUnknown struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

// PlanHistoryWindow returns the complete slots of the last hours before now.
func PlanHistoryWindow(now time.Time, hours int) (time.Time, time.Time, error) {
	if hours < 1 || hours > MaxPlanHistoryHours {
		return time.Time{}, time.Time{}, ErrPlanHistoryRange
	}
	to := now.Truncate(tariff.SlotDuration)
	return to.Add(-time.Duration(hours) * time.Hour), to, nil
}

// groupSeries is one group's per-slot sums; a slot is absent when any entity that had
// reported by then is missing or any value is invalid, so a partial sum never shows.
type groupSeries struct {
	present bool
	rows    map[int64]groupSlotRow
}

func (g groupSeries) energy(ts int64, ret bool) (*float64, bool) {
	r, ok := g.rows[ts]
	if !ok {
		return nil, false
	}
	value, count, invalid := r.Energy, r.EnergyCount, r.InvalidEnergy
	if ret {
		value, count, invalid = r.ReturnEnergy, r.ReturnEnergyCount, r.InvalidReturnEnergy
	}
	if count != r.Count || invalid || !finite(value) {
		return nil, false
	}
	wh := value * 1000
	return &wh, r.Excluded
}

func queryGroupSeries(ctx context.Context, group string, from, to time.Time) (groupSeries, error) {
	// requireAll folds a partial group into Excluded; dropping those slots here leaves
	// Excluded meaning only "recovered or incomplete"
	rows, members, present, err := queryGroupSlotsCoverage(ctx, group, from, to, true)
	if err != nil || !present {
		return groupSeries{present: present}, err
	}
	for ts, r := range rows {
		if r.Count != members.at(ts) {
			delete(rows, ts)
		}
	}
	return groupSeries{present: true, rows: rows}, nil
}

type planHistoryEntityRow struct {
	Meter        int
	Ts           int64
	Energy       *float64
	ReturnEnergy *float64
	SocTemp      *float64
	Flagged      bool
}

func queryEntityRows(ctx context.Context, ids []int, from, to time.Time) (map[int]map[int64]planHistoryEntityRow, error) {
	var rows []planHistoryEntityRow
	if len(ids) > 0 {
		// NULL recovered/incomplete predate the migration and mean "clean", see queryGroupSlotsCoverage
		if err := db.Instance.WithContext(ctx).Table("meters").
			Select(`meter, ts, energy, return_energy, soc_temp,
				CASE WHEN COALESCE(recovered, 0) = 0 AND COALESCE(incomplete, 0) = 0 THEN 0 ELSE 1 END AS flagged`).
			Where("meter IN ? AND ts >= ? AND ts < ?", ids, from.Unix(), to.Unix()).
			Scan(&rows).Error; err != nil {
			return nil, err
		}
	}
	res := make(map[int]map[int64]planHistoryEntityRow)
	for _, r := range rows {
		if res[r.Meter] == nil {
			res[r.Meter] = make(map[int64]planHistoryEntityRow)
		}
		res[r.Meter][r.Ts] = r
	}
	return res, nil
}

func whOf(v *float64) *float64 {
	if v == nil || !finite(*v) || *v < 0 {
		return nil
	}
	return new(*v * 1000)
}

// ComputePlanHistory reads the measured slots in [from,to). loadpointKeys maps a
// loadpoint entity title to its plan key; titles missing from it are reported as
// unmapped instead of guessed.
func ComputePlanHistory(ctx context.Context, from, to time.Time, loadpointKeys map[string]string) (*PlanHistory, error) {
	series := make(map[string]groupSeries)
	for _, group := range []string{Home, Grid, PV, Battery, Loadpoint} {
		s, err := queryGroupSeries(ctx, group, from, to)
		if err != nil {
			return nil, err
		}
		series[group] = s
	}
	prices, err := queryTariffSlots(ctx, from, to)
	if err != nil {
		return nil, err
	}

	res := &PlanHistory{From: from, To: to, Forecast: []PlanHistorySlot{}, Devices: []PlanHistoryDevice{}}
	for ts := from; ts.Before(to); ts = ts.Add(tariff.SlotDuration) {
		res.Forecast = append(res.Forecast, historySlot(ts, series, prices))
	}

	devices, unmapped, err := historyDevices(ctx, from, to, loadpointKeys)
	if err != nil {
		return nil, err
	}
	res.Devices, res.Unmapped = devices, unmapped

	if err := attachPlannedReference(ctx, res.Forecast); err != nil {
		return nil, err
	}
	return res, nil
}

func historySlot(start time.Time, series map[string]groupSeries, prices map[int64]tariffSlot) PlanHistorySlot {
	ts := start.Unix()
	slot := PlanHistorySlot{Start: start, End: start.Add(tariff.SlotDuration), Measured: true}
	value := func(group string, ret bool) *float64 {
		v, flagged := series[group].energy(ts, ret)
		slot.Flagged = slot.Flagged || v != nil && flagged
		return v
	}
	slot.HomeWh = value(Home, false)
	slot.SolarWh = value(PV, false)
	slot.GridImportWh = value(Grid, false)
	slot.GridExportWh = value(Grid, true)
	slot.BatteryChargeWh = value(Battery, false)
	slot.BatteryDischargeWh = value(Battery, true)
	slot.LoadpointWh = value(Loadpoint, false)

	// a site without PV or loadpoints has none of that energy - a fact, not a gap
	solar, loadpoints := slot.SolarWh, slot.LoadpointWh
	if !series[PV].present {
		solar = new(0.0)
	}
	if !series[Loadpoint].present {
		loadpoints = new(0.0)
	}
	if c, h, s, l, g := slot.BatteryChargeWh, slot.HomeWh, solar, loadpoints, slot.GridImportWh; c != nil && h != nil && s != nil && l != nil && g != nil {
		surplus := max(0, *s-*h-*l)
		slot.GridChargeWh = new(min(max(0, *c-surplus), *g))
	}

	p, ok := prices[ts]
	if ok && finite(p.Grid) {
		slot.GridPrice = new(p.Grid)
		if slot.GridImportWh != nil {
			slot.GridCost = new(*slot.GridImportWh / 1000 * p.Grid)
		}
	}
	// recorded only; the static-tariff fallback is the ledger's, not a display fact
	if ok && p.FeedIn != nil && finite(*p.FeedIn) {
		slot.FeedInPrice = new(*p.FeedIn)
		if slot.GridExportWh != nil {
			slot.FeedInIncome = new(*slot.GridExportWh / 1000 * *p.FeedIn)
		}
	}
	return slot
}

func historyDevices(ctx context.Context, from, to time.Time, loadpointKeys map[string]string) ([]PlanHistoryDevice, []PlanHistoryUnknown, error) {
	var entities []entity
	if err := db.Instance.WithContext(ctx).Where(`"group" IN ?`, []string{Battery, Loadpoint}).Order("id").Find(&entities).Error; err != nil {
		return nil, nil, err
	}
	ids := make([]int, 0, len(entities))
	for _, e := range entities {
		ids = append(ids, e.Id)
	}
	rows, err := queryEntityRows(ctx, ids, from, to)
	if err != nil {
		return nil, nil, err
	}

	devices := []PlanHistoryDevice{}
	var unmapped []PlanHistoryUnknown
	for _, e := range entities {
		title := e.Title
		if title == "" {
			title = e.Name
		}
		device := PlanHistoryDevice{Name: e.Name, Title: title, Kind: e.Group}
		switch e.Group {
		case Battery:
			device.Key = "battery:" + e.Name
			if e.CapacityKWh != nil && finite(*e.CapacityKWh) && *e.CapacityKWh > 0 {
				device.CapacityKWh = *e.CapacityKWh
			}
		case Loadpoint:
			key, ok := loadpointKeys[e.Title]
			if !ok {
				if len(rows[e.Id]) > 0 {
					unmapped = append(unmapped, PlanHistoryUnknown{Name: e.Name, Title: title})
				}
				continue
			}
			device.Key = key
		}
		if len(rows[e.Id]) == 0 {
			continue
		}
		for ts := from; ts.Before(to); ts = ts.Add(tariff.SlotDuration) {
			point := PlanHistoryPoint{Start: ts}
			if r, ok := rows[e.Id][ts.Unix()]; ok {
				point.ChargeWh = whOf(r.Energy)
				point.Flagged = r.Flagged
				if e.Group == Battery {
					point.DischargeWh = whOf(r.ReturnEnergy)
					if r.SocTemp != nil && finite(*r.SocTemp) && *r.SocTemp >= 0 && *r.SocTemp <= 100 {
						point.Soc = new(*r.SocTemp)
					}
				} else if point.ChargeWh != nil {
					point.DischargeWh = new(0.0)
				}
			}
			device.Plan = append(device.Plan, point)
		}
		devices = append(devices, device)
	}
	return devices, unmapped, nil
}

// snapshotPlan is the slice of a v1 snapshot's quality payload (the published energy
// insights) the history needs.
type snapshotPlan struct {
	Status   string `json:"status"`
	Forecast []struct {
		Start           time.Time `json:"start"`
		End             time.Time `json:"end"`
		GridChargeMinWh *float64  `json:"gridChargeMinWh"`
		GridChargeMaxWh *float64  `json:"gridChargeMaxWh"`
	} `json:"forecast"`
	Devices []struct {
		Key         string  `json:"key"`
		Title       string  `json:"title"`
		Kind        string  `json:"kind"`
		CapacityKWh float64 `json:"capacityKWh"`
		Plan        []struct {
			Start       time.Time `json:"start"`
			ChargeWh    float64   `json:"chargeWh"`
			DischargeWh float64   `json:"dischargeWh"`
			Soc         float64   `json:"soc"`
		} `json:"plan"`
	} `json:"devices"`
}

type plannedSnapshot struct {
	id   uint64
	ts   time.Time
	plan *snapshotPlan
}

// attachPlannedReference adds, per slot, the entry of the latest snapshot at or before
// the slot start - only when that snapshot planned exactly this slot.
func attachPlannedReference(ctx context.Context, slots []PlanHistorySlot) error {
	if len(slots) == 0 {
		return nil
	}
	first, last := slots[0].Start.Unix(), slots[len(slots)-1].Start.Unix()

	var rows []OptimizerSnapshot
	tx := db.Instance.WithContext(ctx).Select("id, version, ts, quality")
	if err := tx.Where("ts <= ? AND ts >= COALESCE((SELECT MAX(ts) FROM optimizer_snapshots WHERE ts <= ?), ?)", last, first, first).
		Order("ts, id").Find(&rows).Error; err != nil {
		return err
	}

	snapshots := make([]plannedSnapshot, 0, len(rows))
	for _, r := range rows {
		s := plannedSnapshot{id: r.ID, ts: time.Unix(r.TS, 0)}
		var plan snapshotPlan
		if r.Version == 1 && json.Unmarshal(r.Quality, &plan) == nil && plan.Status != "unavailable" && len(plan.Devices) > 0 {
			s.plan = &plan
		}
		snapshots = append(snapshots, s)
	}

	for i := range slots {
		start := slots[i].Start
		// latest snapshot at or before the slot start; an unusable one is not skipped
		// over in favour of an older plan that was no longer in force
		n := slices.IndexFunc(snapshots, func(s plannedSnapshot) bool { return s.ts.After(start) })
		if n < 0 {
			n = len(snapshots)
		}
		if n == 0 || snapshots[n-1].plan == nil {
			continue
		}
		slots[i].Planned = plannedEntry(snapshots[n-1], slots[i])
	}
	return nil
}

func plannedEntry(s plannedSnapshot, slot PlanHistorySlot) *PlanHistoryPlanned {
	res := &PlanHistoryPlanned{SnapshotID: s.id, Snapshot: s.ts, Devices: []PlanHistoryPlannedDevice{}}
	found := false
	for _, f := range s.plan.Forecast {
		if f.Start.Equal(slot.Start) && f.End.Equal(slot.End) {
			res.GridChargeMinWh, res.GridChargeMaxWh = f.GridChargeMinWh, f.GridChargeMaxWh
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	for _, d := range s.plan.Devices {
		for _, p := range d.Plan {
			if !p.Start.Equal(slot.Start) {
				continue
			}
			device := PlanHistoryPlannedDevice{Key: d.Key, Title: d.Title, Kind: d.Kind, ChargeWh: p.ChargeWh, DischargeWh: p.DischargeWh}
			if d.CapacityKWh > 0 && finite(p.Soc) {
				device.Soc = new(p.Soc)
			}
			res.Devices = append(res.Devices, device)
			break
		}
	}
	return res
}
