package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/session"
	"github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnergyPriceAccountingPrototype(t *testing.T) {
	start := time.Date(2026, 10, 1, 23, 30, 0, 0, time.UTC)
	load, pv := []float64{1, 2, 2, 1}, []float64{3, 0, 0, 0}
	imports, prices := []float64{1, 0, 1, 1}, []float64{.1, .4, .5, .1}
	charge, discharge := []float64{3, 0, 0, 0}, []float64{0, 2, 1, 0}
	soc := []float64{0, .6, .2, 0, 0}
	phys := batteryPhysics{CapacityKWh: 5, EtaC: 1, EtaD: 1, MaxChargeKWh: 3, MaxDischargeKWh: 3}
	var slots []slotData
	var physics []batteryPhysics
	endpoints := map[int64]float64{}
	for i := range load {
		ts := start.Add(time.Duration(i) * 15 * time.Minute)
		slots = append(slots, slotData{Start: ts, HomeKWh: load[i], PVKWh: pv[i], GridImportKWh: imports[i], PriceGrid: prices[i], BatterySocFrac: &soc[i], BatteryChargeKWh: charge[i], BatteryDischargeKWh: discharge[i]})
		physics = append(physics, phys)
		endpoints[ts.Add(15*time.Minute).Unix()] = soc[i+1]
	}
	w2, _, err := computeW2(slots, phys)
	require.NoError(t, err)
	actual := settleFlows(slots, actualFlows(slots)).PerSlot
	assert.InDelta(t, .7, actual, 1e-9)
	assert.InDelta(t, 1.1, settleFlows(slots, w2).PerSlot, 1e-9)
	assert.InDelta(t, .7/3, actual/3, 1e-9)
	assert.NotEqual(t, (.1+.3)/2, actual/3, "monthly purchase price is not an average of daily prices")
	day := inventoryAdjustedFromSlots(slots[:2], physics[:2], &InventoryAdjustedChain{}, endpoints)
	month := inventoryAdjustedFromSlots(slots, physics, &InventoryAdjustedChain{}, endpoints)
	assert.InDelta(t, .3, day.Control.Full, 1e-9)
	assert.InDelta(t, .4, month.Control.Full, 1e-9)
	assert.InDelta(t, .1, month.Control.Full-day.Control.Full, 1e-9)
	t.Log("Synthetic shared trajectory: W2 cash1.10, actualcash0.70; weightedgrid0.233333/kWh, wholeload0.116667/kWh; inventoryneutral dailycontrol0.30+0.10=month0.40")
}

func setupEnergyPrices(t *testing.T) (entity, entity) {
	t.Helper()
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	return mustCreateEntity(t, Grid, Grid), mustCreateEntity(t, Home, Home)
}
func TestEnergyPricesIndependentCoverageAndWeightedTotals(t *testing.T) {
	grid, home := setupEnergyPrices(t)
	start := time.Date(2026, 10, 1, 23, 30, 0, 0, time.UTC)
	for i, p := range []float64{-.1, .4, .5, 0} {
		ts := start.Add(time.Duration(i) * 15 * time.Minute)
		feed := .08
		var f *float64
		if i != 1 {
			f = &feed
		}
		require.NoError(t, PersistTariffs(ts, &p, f, nil, nil))
		require.NoError(t, persist(grid, ts, float64(i+1), map[bool]float64{true: 1}[i == 1], nil, false, false))
		if i != 2 {
			require.NoError(t, persist(home, ts, 2, 0, nil, false, false))
		}
	}
	// A configured PV meter with no history must not affect actual prices.
	mustCreateEntity(t, PV, "missing-pv")
	report, err := ComputeEnergyPrices(context.Background(), "2026-10", start.Add(time.Hour), nil)
	require.NoError(t, err)
	assert.Equal(t, 4, report.Totals.Grid.Coverage.ValidSlots)
	assert.InDelta(t, 10, report.Totals.Grid.ImportKWh, 1e-9)
	assert.InDelta(t, 2.2, report.Totals.Grid.ImportCost, 1e-9)
	require.NotNil(t, report.Totals.Grid.WeightedPrice)
	assert.InDelta(t, .22, *report.Totals.Grid.WeightedPrice, 1e-9)
	assert.Equal(t, 2, report.Totals.Consumption.Coverage.ValidSlots, "export without feedin and missing home independently excluded")
	assert.InDelta(t, 4, report.Totals.Consumption.ConsumptionKWh, 1e-9)
	require.NotNil(t, report.Totals.Consumption.EffectivePrice)
	assert.InDelta(t, -.025, *report.Totals.Consumption.EffectivePrice, 1e-9)
	assert.Empty(t, report.Totals.Worlds)
	assert.Equal(t, 98, report.Totals.Grid.Coverage.TotalSlots, "missing start-of-month prefix is not clipped away")
	var cost, energy float64
	for _, d := range report.Days {
		cost += d.Grid.ImportCost
		energy += d.Grid.ImportKWh
	}
	assert.InDelta(t, report.Totals.Grid.ImportCost, cost, 1e-12)
	assert.InDelta(t, report.Totals.Grid.ImportKWh, energy, 1e-12)
}
func TestEnergyPricesZeroUnknownAndMultipleMeters(t *testing.T) {
	grid, _ := setupEnergyPrices(t)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	price := 0.0
	require.NoError(t, PersistTariffs(start, &price, nil, nil, nil))
	require.NoError(t, persist(grid, start, 0, 0, nil, false, false))
	report, err := ComputeEnergyPrices(context.Background(), "", start.Add(15*time.Minute), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Totals.Grid.Coverage.ValidSlots)
	assert.Nil(t, report.Totals.Grid.WeightedPrice)
	second := mustCreateEntity(t, Grid, "second-grid")
	require.NoError(t, PersistTariffs(start.Add(15*time.Minute), &price, nil, nil, nil))
	require.NoError(t, persist(grid, start.Add(15*time.Minute), 0, 0, nil, false, false))
	require.NoError(t, persist(second, start.Add(30*time.Minute), 0, 0, nil, false, false))
	report, err = ComputeEnergyPrices(context.Background(), "", start.Add(30*time.Minute), nil)
	require.NoError(t, err)
	assert.Equal(t, 2, report.Totals.Grid.Coverage.ValidSlots, "a member that first reported later cannot invalidate earlier slots")
	require.NoError(t, persist(second, start, 1, 0, nil, false, false))
	report, err = ComputeEnergyPrices(context.Background(), "", start.Add(30*time.Minute), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Totals.Grid.Coverage.ValidSlots, "partial multi-meter sum after the first reading must not masquerade as complete")
	report, err = ComputeEnergyPrices(context.Background(), "", start.Add(15*time.Minute), nil)
	require.NoError(t, err)
	require.NotNil(t, report.Totals.Grid.WeightedPrice)
	assert.Zero(t, *report.Totals.Grid.WeightedPrice, "recorded zero price is known")
}
func TestEnergyPricesCalendarDSTAndEmpty(t *testing.T) {
	setupEnergyPrices(t)
	loc, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)
	for _, tc := range []struct {
		month, day string
		slots      int
	}{{"2026-03", "2026-03-29", 92}, {"2026-10", "2026-10-25", 100}} {
		report, err := ComputeEnergyPrices(context.Background(), tc.month, time.Date(2026, 12, 1, 0, 0, 0, 0, loc), nil)
		require.NoError(t, err)
		found := false
		total := 0
		for _, d := range report.Days {
			total += d.Grid.Coverage.TotalSlots
			if d.Date == tc.day {
				found = true
				assert.Equal(t, tc.slots, d.Grid.Coverage.TotalSlots)
			}
		}
		require.True(t, found)
		assert.Equal(t, report.Totals.Grid.Coverage.TotalSlots, total)
	}
	now := time.Date(2026, 10, 1, 0, 7, 0, 0, loc)
	report, err := ComputeEnergyPrices(context.Background(), "", now, nil)
	require.NoError(t, err)
	assert.Empty(t, report.Days)
	assert.Nil(t, report.Totals.Grid.WeightedPrice)
	for _, m := range []string{"2026-11", "2026-1", "nonsense"} {
		_, err := ComputeEnergyPrices(context.Background(), m, now, nil)
		require.ErrorIs(t, err, ErrEnergyPriceMonth)
	}
}
func TestEVTimingSameEnergyConnectionCohort(t *testing.T) {
	grid, home := setupEnergyPrices(t)
	lp := mustCreateEntity(t, Loadpoint, "internal-lp-name")
	require.NoError(t, lp.updateTitle("lp1"))
	start := time.Date(2026, 10, 1, 23, 45, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	created := start.Add(30 * time.Minute)
	s := session.Session{Connected: &start, Disconnected: &end, Created: created, Finished: end, Loadpoint: "lp1", Vehicle: "Car", ChargedEnergy: 2}
	require.NoError(t, db.Instance.Create(&s).Error)
	for i, p := range []float64{.4, .4, .1, .1} {
		ts := start.Add(time.Duration(i) * 15 * time.Minute)
		feed := .05
		require.NoError(t, PersistTariffs(ts, &p, &feed, nil, nil))
		require.NoError(t, persist(grid, ts, 1, 0, nil, false, false))
		require.NoError(t, persist(home, ts, 0, 0, nil, false, false))
		ev := 0.0
		if i >= 2 {
			ev = 1
		}
		require.NoError(t, persist(lp, ts, ev, 0, nil, false, false))
		snap := OptimizerSnapshot{Version: 1, TS: ts.Unix(), Request: json.RawMessage(`{"batteries":[{"c_max":4000}]}`), Quality: json.RawMessage(`{"devices":[{"kind":"loadpoint","title":"lp1","name":"car"}]}`)}
		require.NoError(t, db.Instance.Create(&snap).Error)
	}
	report, err := ComputeEnergyPrices(context.Background(), "2026-10", end, nil)
	require.NoError(t, err)
	require.Equal(t, "estimated", report.EVTiming.Status)
	require.NotNil(t, report.EVTiming.Savings)
	assert.InDelta(t, .6, *report.EVTiming.Savings, 1e-9)
	assert.InDelta(t, .2, *report.EVTiming.ActualTariffCost, 1e-9)
	assert.InDelta(t, .8, *report.EVTiming.ImmediateTariffCost, 1e-9)
	assert.Equal(t, 1, report.Days[0].EVTiming.IncludedSessions)
	assert.Zero(t, report.Days[1].EVTiming.IncludedSessions)
	// A negative opportunity is a loss, not clamped to flattering zero.
	require.NoError(t, db.Instance.Model(new(tariffValue)).Where("ts < ?", created.Unix()).Update("grid", -.1).Error)
	report, err = ComputeEnergyPrices(context.Background(), "2026-10", end, nil)
	require.NoError(t, err)
	require.NotNil(t, report.EVTiming.Savings)
	assert.InDelta(t, -.4, *report.EVTiming.Savings, 1e-9)
	// A later observed cap cannot establish an earlier hypothetical charge rate.
	require.NoError(t, db.Instance.Where("ts = ?", start.Unix()).Delete(new(OptimizerSnapshot)).Error)
	report, err = ComputeEnergyPrices(context.Background(), "2026-10", end, nil)
	require.NoError(t, err)
	assert.Equal(t, "unavailable", report.EVTiming.Status)
	assert.Nil(t, report.EVTiming.Savings)
	assert.Equal(t, 1, report.EVTiming.ExcludedSessions)
}

func TestEnergyPricesOneBatteryTrajectoryAcrossMidnight(t *testing.T) {
	grid, home := setupEnergyPrices(t)
	pv := mustCreateEntity(t, PV, "pv")
	bat := mustCreateEntity(t, Battery, "bat")
	seedBatteryCalibration(t, bat, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	start := time.Date(2026, 10, 1, 23, 30, 0, 0, time.UTC)
	loads, pvs, imports, socs := []float64{.2, .2, 1, 1}, []float64{1.2, 0, 0, 0}, []float64{0, 0, .5, .5}, []float64{50, 55, 54, 49, 44}
	for i := range loads {
		ts := start.Add(time.Duration(i) * 15 * time.Minute)
		p, feed := .2+float64(i)*.1, .05
		require.NoError(t, PersistTariffs(ts, &p, &feed, nil, nil))
		require.NoError(t, persist(grid, ts, imports[i], 0, nil, false, false))
		require.NoError(t, persist(home, ts, loads[i], 0, nil, false, false))
		require.NoError(t, persist(pv, ts, pvs[i], 0, nil, false, false))
		require.NoError(t, persist(bat, ts, 0, .5, &socs[i], false, false))
	}
	require.NoError(t, persist(bat, start.Add(time.Hour), 0, 0, &socs[4], false, false))
	report, err := ComputeEnergyPrices(context.Background(), "2026-10", start.Add(time.Hour), nil)
	require.NoError(t, err)
	require.Len(t, report.Totals.Worlds, 4)
	require.NotNil(t, report.Totals.InventoryBenefit)
	sum := 0.0
	for _, day := range report.Days {
		require.NotNil(t, day.InventoryBenefit)
		sum += *day.InventoryBenefit
	}
	assert.InDelta(t, *report.Totals.InventoryBenefit, sum, 1e-9)
	for w, month := range report.Totals.Worlds {
		var cost, energy float64
		for _, day := range report.Days {
			cost += day.Worlds[w].NetCost
			energy += day.Worlds[w].ImportKWh
		}
		assert.InDelta(t, month.NetCost, cost, 1e-9)
		assert.InDelta(t, month.ImportKWh, energy, 1e-9)
	}
	// Independent day reset would restart from measured SoC. The canonical raw W2
	// must instead equal a single invocation over the month's four contiguous slots.
	set, err := buildLedgerSlots(context.Background(), start, start.Add(time.Hour), true, true, nil)
	require.NoError(t, err)
	phys, err := deriveBatteryPhysics(context.Background())
	require.NoError(t, err)
	flows, _, err := computeW2(set.Slots, phys)
	require.NoError(t, err)
	assert.InDelta(t, settleFlows(set.Slots, flows).PerSlot, report.Totals.Worlds[2].NetCost, 1e-9)
}

func TestHistoricalEVPowerRefusals(t *testing.T) {
	start := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	s := session.Session{Connected: &start, Disconnected: &end, Vehicle: "Car"}
	snap := func(ts time.Time, power float64, devices string) OptimizerSnapshot {
		return OptimizerSnapshot{Version: 1, TS: ts.Unix(), Request: json.RawMessage(fmt.Sprintf(`{"batteries":[{"c_max":%g}]}`, power)), Quality: json.RawMessage(`{"devices":` + devices + `}`)}
	}
	lp := `[{"kind":"loadpoint","title":"Charger","name":"car"}]`
	expected := `[{"kind":"expectedVehicle","title":"Car","name":"car"}]`
	for _, tc := range []struct {
		name      string
		snapshots []OptimizerSnapshot
		valid     bool
	}{
		{"unchanged", []OptimizerSnapshot{snap(start, 11000, lp), snap(start.Add(15*time.Minute), 11000, lp)}, true},
		{"expected before connection", []OptimizerSnapshot{snap(start.Add(-time.Minute), 11000, expected), snap(start.Add(10*time.Minute), 11000, lp), snap(start.Add(20*time.Minute), 11000, lp)}, true},
		{"postconnection only", []OptimizerSnapshot{snap(start.Add(time.Second), 11000, lp), snap(start.Add(15*time.Minute), 11000, lp)}, false},
		{"changed cap", []OptimizerSnapshot{snap(start, 11000, lp), snap(start.Add(15*time.Minute), 6000, lp)}, false},
		{"archive gap", []OptimizerSnapshot{snap(start, 11000, lp)}, false},
		{"renamed title", []OptimizerSnapshot{snap(start, 11000, `[{"kind":"loadpoint","title":"Old name"}]`)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := historicalEVPower(s, tc.snapshots, "Charger")
			assert.Equal(t, tc.valid, ok)
		})
	}
}

func TestEVTimingPartialQuarterAndAdjacentSessionRefusal(t *testing.T) {
	setupEnergyPrices(t)
	lp := mustCreateEntity(t, Loadpoint, "lp")
	floor := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	start := floor.Add(5 * time.Minute)
	end := floor.Add(25 * time.Minute)
	s := session.Session{Connected: &start, Disconnected: &end, Created: start, Finished: end, Loadpoint: "lp", ChargedEnergy: 1}
	prices := map[int64]tariffSlot{floor.Unix(): {Grid: .6}, floor.Add(15 * time.Minute).Unix(): {Grid: .3}}
	require.NoError(t, persist(lp, floor, .5, 0, nil, false, false))
	require.NoError(t, persist(lp, floor.Add(15*time.Minute), .5, 0, nil, false, false))
	snapshots := []OptimizerSnapshot{{Version: 1, TS: start.Unix(), Request: json.RawMessage(`{"batteries":[{"c_max":6000}]}`), Quality: json.RawMessage(`{"devices":[{"kind":"loadpoint","title":"lp"}]}`)}, {Version: 1, TS: floor.Add(15 * time.Minute).Unix(), Request: json.RawMessage(`{"batteries":[{"c_max":6000}]}`), Quality: json.RawMessage(`{"devices":[{"kind":"loadpoint","title":"lp"}]}`)}}
	actual, immediate, ok, err := priceEVSession(context.Background(), 0, s, []session.Session{s}, []entity{lp}, snapshots, prices, floor, floor.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, ok)
	assert.InDelta(t, .45, actual, 1e-9)
	assert.InDelta(t, .6, immediate, 1e-9, "ten available minutes at6kW deliver1kWh before tariff boundary")
	otherStart, otherEnd := floor.Add(-10*time.Minute), start
	other := session.Session{Connected: &otherStart, Disconnected: &otherEnd, Loadpoint: "lp"}
	_, _, ok, err = priceEVSession(context.Background(), 0, s, []session.Session{s, other}, []entity{lp}, snapshots, prices, floor, floor.Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, ok, "adjacent sessions within one meter quarter cannot be separately attributed")
}

func TestEnergyPricesNullReadingIsNotMeasuredZero(t *testing.T) {
	grid, home := setupEnergyPrices(t)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	p, f := .2, .05
	require.NoError(t, PersistTariffs(start, &p, &f, nil, nil))
	require.NoError(t, persist(grid, start, 1, 0, nil, false, false))
	require.NoError(t, persist(home, start, 1, 0, nil, false, false))
	query := db.Instance.Model(new(meter)).Where("meter = ? AND ts = ?", grid.Id, start.Unix())
	require.NoError(t, query.Update("return_energy", nil).Error)
	report, err := ComputeEnergyPrices(context.Background(), "", start.Add(15*time.Minute), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Totals.Grid.Coverage.ValidSlots, "purchase does not need export meter column")
	assert.Zero(t, report.Totals.Consumption.Coverage.ValidSlots)
	require.NoError(t, query.Update("energy", nil).Error)
	report, err = ComputeEnergyPrices(context.Background(), "", start.Add(15*time.Minute), nil)
	require.NoError(t, err)
	assert.Zero(t, report.Totals.Grid.Coverage.ValidSlots)
	assert.Nil(t, report.Totals.Grid.WeightedPrice)
	require.NoError(t, query.Updates(map[string]any{"energy": 1, "return_energy": 0}).Error)
	require.NoError(t, db.Instance.Model(new(meter)).Where("meter = ?", home.Id).Update("energy", nil).Error)
	report, err = ComputeEnergyPrices(context.Background(), "", start.Add(15*time.Minute), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Totals.Grid.Coverage.ValidSlots)
	assert.Zero(t, report.Totals.Consumption.Coverage.ValidSlots)
}

func TestEnergyPricesNegativeMemberCannotCancelIntoValidSum(t *testing.T) {
	grid, home := setupEnergyPrices(t)
	other := mustCreateEntity(t, Grid, "other-grid")
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	p, f := .2, .05
	require.NoError(t, PersistTariffs(start, &p, &f, nil, nil))
	require.NoError(t, persist(grid, start, 1, 1, nil, false, false))
	require.NoError(t, persist(other, start, -.5, -.5, nil, false, false))
	require.NoError(t, persist(home, start, 1, 0, nil, false, false))
	report, err := ComputeEnergyPrices(context.Background(), "", start.Add(15*time.Minute), nil)
	require.NoError(t, err)
	assert.Zero(t, report.Totals.Grid.Coverage.ValidSlots)
	require.NoError(t, db.Instance.Model(new(meter)).Where("meter = ?", other.Id).Update("energy", .5).Error)
	report, err = ComputeEnergyPrices(context.Background(), "", start.Add(15*time.Minute), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Totals.Grid.Coverage.ValidSlots)
	assert.Zero(t, report.Totals.Consumption.Coverage.ValidSlots, "negative export member must not cancel into a positive group sum")
	legacy, _, err := queryGroupSlots(context.Background(), Grid, start, start.Add(15*time.Minute))
	require.NoError(t, err)
	assert.False(t, legacy[start.Unix()].Excluded, "legacy query semantics remain unchanged")
}

// Mirrors the site's history: the heat pump loadpoint first reported on Aug 21 and
// the battery on Aug 22, a second grid entity (db:1) appeared later still. Entities
// count only from their first reading, and a gap after it still invalidates.
func TestEnergyPricesGroupMembershipStartsAtFirstReading(t *testing.T) {
	grid, home := setupEnergyPrices(t)
	pv := mustCreateEntity(t, PV, "db:3")
	car := mustCreateEntity(t, Loadpoint, "lp1")
	heatPump := mustCreateEntity(t, Loadpoint, "Wärmepumpe")
	bat := mustCreateEntity(t, Battery, "db:2")
	seedBatteryCalibration(t, bat, time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC))
	newGrid := mustCreateEntity(t, Grid, "db:1")
	p, f, soc := .3, .08, 50.0
	slot := func(day int, withHeatPump, withBattery bool) {
		ts := time.Date(2026, 8, day, 12, 0, 0, 0, time.UTC)
		require.NoError(t, PersistTariffs(ts, &p, &f, nil, nil))
		require.NoError(t, persist(grid, ts, 1, 0, nil, false, false))
		require.NoError(t, persist(home, ts, 1, 0, nil, false, false))
		require.NoError(t, persist(pv, ts, 0, 0, nil, false, false))
		if day > 20 { // no loadpoint had reported yet on Aug 20
			require.NoError(t, persist(car, ts, 0, 0, nil, false, false))
		}
		if withHeatPump {
			require.NoError(t, persist(heatPump, ts, .5, 0, nil, false, false))
		}
		if withBattery {
			require.NoError(t, persist(bat, ts, 0, 0, &soc, false, false))
		}
	}
	slot(20, false, false)
	slot(21, true, false)
	slot(22, true, true)
	slot(23, false, true) // heat pump gap after its first reading
	// The new grid entity reports for the first time on Aug 24 and is missing on Aug 25.
	for day, both := range map[int]bool{24: true, 25: false} {
		slot(day, true, true)
		if both {
			require.NoError(t, persist(newGrid, time.Date(2026, 8, day, 12, 0, 0, 0, time.UTC), .2, 0, nil, false, false))
		}
	}
	report, err := ComputeEnergyPrices(context.Background(), "2026-08", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), nil)
	require.NoError(t, err)
	valid := func(day int) (int, int, int) {
		d := report.Days[day-1]
		worlds := 0
		if len(d.Worlds) > 0 {
			worlds = d.Worlds[0].Coverage.ValidSlots
		}
		return d.Grid.Coverage.ValidSlots, d.Consumption.Coverage.ValidSlots, worlds
	}
	for _, tc := range []struct{ day, grid, consumption, worlds int }{
		{20, 1, 1, 0}, // no loadpoint yet (residual home holds it); no battery start state
		{21, 1, 1, 0},
		{22, 1, 1, 1},
		{23, 1, 0, 0}, // heat pump missing after commissioning
		{24, 1, 1, 1},
		{25, 0, 0, 0}, // second grid missing after commissioning
	} {
		g, c, w := valid(tc.day)
		assert.Equal(t, []int{tc.grid, tc.consumption, tc.worlds}, []int{g, c, w}, "Aug %d", tc.day)
	}
	assert.InDelta(t, 1.2, report.Days[23].Grid.ImportKWh, 1e-9, "both grid entities summed once both report")
	assert.InDelta(t, 1.5, report.Days[21].Consumption.ConsumptionKWh, 1e-9)
}

func TestReportTimezoneNeverLocal(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)
	summer := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	previous := localZoneName
	t.Cleanup(func() { localZoneName = previous })
	for _, tc := range []struct {
		name, resolved string
		loc            *time.Location
		want           string
	}{
		{"named zone", "", berlin, "Europe/Berlin"},
		{"local resolved", "Europe/Berlin", time.FixedZone("Local", 7200), "Europe/Berlin"},
		{"local unresolved", "", time.FixedZone("Local", 7200), "Etc/GMT-2"},
		{"local west", "", time.FixedZone("Local", -5*3600), "Etc/GMT+5"},
		{"resolved name disagrees", "Europe/Berlin", time.FixedZone("Local", 19800), "+05:30"},
		{"bogus name", "Not/AZone", time.FixedZone("Local", -9000), "-02:30"},
		{"utc", "", time.FixedZone("Local", 0), "UTC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			localZoneName = func() string { return tc.resolved }
			got := reportTimezone(tc.loc, summer)
			assert.Equal(t, tc.want, got)
			_, err := time.LoadLocation(got)
			if !strings.Contains(got, ":") {
				assert.NoError(t, err, "whole-hour names must be loadable IANA zones")
			}
		})
	}
	setupEnergyPrices(t)
	localZoneName = func() string { return "" }
	report, err := ComputeEnergyPrices(context.Background(), "", time.Date(2026, 10, 2, 0, 0, 0, 0, time.FixedZone("Local", 7200)), nil)
	require.NoError(t, err)
	assert.Equal(t, "Etc/GMT-2", report.Timezone)
}
