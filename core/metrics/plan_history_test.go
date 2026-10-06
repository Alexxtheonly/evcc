package metrics

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/require"
)

func TestPlanHistoryWindow(t *testing.T) {
	now := time.Date(2026, 10, 5, 21, 33, 0, 0, time.UTC)
	from, to, err := PlanHistoryWindow(now, 24)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 10, 5, 21, 30, 0, 0, time.UTC), to, "the in-progress slot is not measured yet")
	require.Equal(t, to.Add(-24*time.Hour), from)
	for _, hours := range []int{0, -1, 49} {
		_, _, err := PlanHistoryWindow(now, hours)
		require.ErrorIs(t, err, ErrPlanHistoryRange)
	}
}

type planHistoryFixture struct {
	home, grid, pv, battery, carport, garage entity
	base                                     time.Time
}

func newPlanHistoryFixture(t *testing.T) planHistoryFixture {
	t.Helper()
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	f := planHistoryFixture{
		home:    mustCreateEntity(t, Home, Home),
		grid:    mustCreateEntity(t, Grid, Grid),
		pv:      mustCreateEntity(t, PV, "db:5"),
		battery: mustCreateEntity(t, Battery, "db:11"),
		base:    time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC),
	}
	var err error
	f.carport, err = createEntity(Loadpoint, "db:3", "Carport")
	require.NoError(t, err)
	f.garage, err = createEntity(Loadpoint, "db:10", "Garage")
	require.NoError(t, err)
	capacity := 19.32
	require.NoError(t, db.Instance.Model(&f.battery).UpdateColumn("capacity_kwh", capacity).Error)
	return f
}

func (f planHistoryFixture) slot(i int) time.Time {
	return f.base.Add(time.Duration(i) * 15 * time.Minute)
}

func TestComputePlanHistory(t *testing.T) {
	f := newPlanHistoryFixture(t)
	soc := func(v float64) *float64 { return &v }
	price := 0.2
	feedIn := 0.08

	// slot 0: battery grid-charges 1.5 kWh while solar covers part of home
	// slot 1: battery charges from surplus only; a flagged (recovered) pv row
	// slot 2: no grid row - every grid-dependent value is a gap, not zero
	// slot 3: an EV takes the surplus, so the battery's charge came from the grid
	type row struct{ home, pv, gridIn, gridOut, chg, dis, carport float64 }
	rows := []row{
		{home: 0.6, pv: 0.1, gridIn: 2.0, chg: 1.5},
		{home: 0.2, pv: 1.5, gridOut: 0.3, chg: 1.0},
		{home: 0.3, pv: 0.0, chg: 0, dis: 0.3},
		{home: 0.2, pv: 2.0, gridIn: 1.0, chg: 1.0, carport: 1.8},
	}
	for i, r := range rows {
		ts := f.slot(i)
		require.NoError(t, persist(f.home, ts, r.home, 0, nil, false, false))
		require.NoError(t, persist(f.pv, ts, r.pv, 0, nil, i == 1, false))
		if i != 2 {
			require.NoError(t, persist(f.grid, ts, r.gridIn, r.gridOut, nil, false, false))
		}
		require.NoError(t, persist(f.battery, ts, r.chg, r.dis, soc(10+float64(10*i)), false, false))
		require.NoError(t, persist(f.carport, ts, r.carport, 0, soc(45), false, false))
		require.NoError(t, PersistTariffs(ts, &price, &feedIn, nil, nil))
	}

	res, err := ComputePlanHistory(context.Background(), f.slot(0), f.slot(5), map[string]string{"Carport": "loadpoint:0"})
	require.NoError(t, err)
	require.Len(t, res.Forecast, 5)

	s := res.Forecast
	require.True(t, s[0].Measured)
	require.InDelta(t, 1500, *s[0].BatteryChargeWh, 1e-6)
	require.InDelta(t, 1500, *s[0].GridChargeWh, 1e-6, "no surplus: every charged Wh came from the grid")
	require.InDelta(t, 0.4, *s[0].GridCost, 1e-9)
	require.False(t, s[0].Flagged)

	require.True(t, s[1].Flagged, "recovered pv row is rendered but flagged")
	require.InDelta(t, 0, *s[1].GridChargeWh, 1e-6, "1.3 kWh surplus covers the 1 kWh charge")
	require.InDelta(t, 300, *s[1].GridExportWh, 1e-6)
	require.InDelta(t, 0.024, *s[1].FeedInIncome, 1e-9)

	require.Nil(t, s[2].GridImportWh, "missing grid row stays a gap")
	require.Nil(t, s[2].GridExportWh)
	require.Nil(t, s[2].GridChargeWh)
	require.Nil(t, s[2].GridCost)
	require.NotNil(t, s[2].HomeWh)

	require.InDelta(t, 1000, *s[3].GridChargeWh, 1e-6, "surplus went to the EV, so the battery charge was grid energy")

	require.Nil(t, s[4].HomeWh, "a slot without any row is a gap")
	require.Nil(t, s[4].GridPrice)

	require.Len(t, res.Devices, 2)
	battery := res.Devices[0]
	require.Equal(t, "battery:db:11", battery.Key)
	require.Equal(t, 19.32, battery.CapacityKWh)
	require.Len(t, battery.Plan, 5)
	require.Equal(t, 30.0, *battery.Plan[2].Soc, "soc is the slot-start level")
	require.InDelta(t, 300, *battery.Plan[2].DischargeWh, 1e-6)
	require.Nil(t, battery.Plan[4].ChargeWh)

	carport := res.Devices[1]
	require.Equal(t, "loadpoint:0", carport.Key)
	require.Nil(t, carport.Plan[0].Soc, "loadpoint soc_temp is not a device charge level")
	require.InDelta(t, 1800, *carport.Plan[3].ChargeWh, 1e-6)

	require.Empty(t, res.Unmapped, "the garage never reported, so there is nothing to map")
}

func TestComputePlanHistoryUnmappedLoadpoint(t *testing.T) {
	f := newPlanHistoryFixture(t)
	require.NoError(t, persist(f.garage, f.slot(0), 2, 0, nil, false, false))
	res, err := ComputePlanHistory(context.Background(), f.slot(0), f.slot(1), nil)
	require.NoError(t, err)
	require.Equal(t, []PlanHistoryUnknown{{Name: "db:10", Title: "Garage"}}, res.Unmapped)
	require.Empty(t, res.Devices)
}

// A slot only shows a group total when every entity that had reported by then did.
func TestComputePlanHistoryPartialGroupIsGap(t *testing.T) {
	f := newPlanHistoryFixture(t)
	second := mustCreateEntity(t, PV, "db:6")
	require.NoError(t, persist(f.pv, f.slot(0), 1, 0, nil, false, false))
	require.NoError(t, persist(second, f.slot(0), 1, 0, nil, false, false))
	require.NoError(t, persist(f.pv, f.slot(1), 1, 0, nil, false, false))

	res, err := ComputePlanHistory(context.Background(), f.slot(0), f.slot(2), nil)
	require.NoError(t, err)
	require.InDelta(t, 2000, *res.Forecast[0].SolarWh, 1e-6)
	require.Nil(t, res.Forecast[1].SolarWh, "one of two strings missing is not a measured total")
}

func savePlanSnapshot(t *testing.T, ts time.Time, status string, start time.Time, chargeWh float64) {
	t.Helper()
	end := start.Add(15 * time.Minute)
	quality, err := json.Marshal(map[string]any{
		"status": status,
		"forecast": []map[string]any{
			{"start": start.Add(-15 * time.Minute), "end": start},
			{"start": start, "end": end, "gridChargeMinWh": chargeWh, "gridChargeMaxWh": chargeWh},
		},
		"devices": []map[string]any{{
			"key": "battery:db:11", "title": "Battery", "kind": "battery", "capacityKWh": 19.32,
			"plan": []map[string]any{
				{"start": start.Add(-15 * time.Minute), "chargeWh": 0, "dischargeWh": 0, "soc": 10},
				{"start": start, "chargeWh": chargeWh, "dischargeWh": 0, "soc": 20},
			},
		}},
	})
	require.NoError(t, err)
	_, err = SaveOptimizerSnapshot(OptimizerSnapshot{Timestamp: ts, ControllerVersion: "test", Quality: quality})
	require.NoError(t, err)
}

func TestComputePlanHistoryPlannedReference(t *testing.T) {
	f := newPlanHistoryFixture(t)
	// snapshots: before slot 0 (plans slot 0), during slot 0 (plans slot 1),
	// an unavailable plan before slot 2, nothing that plans slot 3
	savePlanSnapshot(t, f.slot(0).Add(-30*time.Second), "ready", f.slot(0), 1500)
	savePlanSnapshot(t, f.slot(0).Add(30*time.Second), "ready", f.slot(1), 700)
	savePlanSnapshot(t, f.slot(2).Add(-30*time.Second), "unavailable", f.slot(2), 900)
	savePlanSnapshot(t, f.slot(3).Add(-30*time.Second), "ready", f.slot(5), 100)

	res, err := ComputePlanHistory(context.Background(), f.slot(0), f.slot(4), nil)
	require.NoError(t, err)
	s := res.Forecast

	require.NotNil(t, s[0].Planned)
	require.Equal(t, f.slot(0).Add(-30*time.Second).Unix(), s[0].Planned.Snapshot.Unix())
	require.Len(t, s[0].Planned.Devices, 1)
	require.Equal(t, 1500.0, s[0].Planned.Devices[0].ChargeWh)
	require.Equal(t, 20.0, *s[0].Planned.Devices[0].Soc, "planned level is the slot-end level")
	require.Equal(t, 1500.0, *s[0].Planned.GridChargeMinWh)

	require.NotNil(t, s[1].Planned, "the snapshot taken during slot 0 was in force at slot 1's start")
	require.Equal(t, 700.0, s[1].Planned.Devices[0].ChargeWh)

	require.Nil(t, s[2].Planned, "an unavailable plan is not replaced by an older one")
	require.Nil(t, s[3].Planned, "the latest snapshot did not plan this slot")
}

func TestComputePlanHistoryEmpty(t *testing.T) {
	f := newPlanHistoryFixture(t)
	res, err := ComputePlanHistory(context.Background(), f.slot(0), f.slot(2), nil)
	require.NoError(t, err)
	require.Len(t, res.Forecast, 2)
	require.Nil(t, res.Forecast[0].HomeWh)
	require.Nil(t, res.Forecast[0].SolarWh)
	require.Nil(t, res.Forecast[0].Planned)
	require.Empty(t, res.Devices)
}
