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

// savePlanSnapshot stores a run made at ts like the controller does: the first entry is
// the partial slot from ts to the next boundary, then three whole slots.
func savePlanSnapshot(t *testing.T, ts time.Time, status, action string, chargeWh float64) {
	t.Helper()
	var forecast, plan []map[string]any
	start := ts
	for end := ts.Truncate(15 * time.Minute).Add(15 * time.Minute); len(forecast) < 4; start, end = end, end.Add(15*time.Minute) {
		forecast = append(forecast, map[string]any{"start": start, "end": end, "gridChargeMinWh": chargeWh, "gridChargeMaxWh": chargeWh})
		plan = append(plan, map[string]any{"start": start, "chargeWh": chargeWh, "dischargeWh": 0, "soc": 20})
	}
	payload := map[string]any{
		"status":   status,
		"forecast": forecast,
		"devices": []map[string]any{{
			"key": "battery:db:11", "title": "Battery", "kind": "battery", "capacityKWh": 19.32, "plan": plan,
		}},
	}
	if action != "" {
		payload["scenarios"] = map[string]any{"control": map[string]any{"selectedAction": action, "reason": "minimumRegret"}}
	}
	quality, err := json.Marshal(payload)
	require.NoError(t, err)
	_, err = SaveOptimizerSnapshot(OptimizerSnapshot{Timestamp: ts, ControllerVersion: "test", Quality: quality})
	require.NoError(t, err)
}

func TestComputePlanHistoryPlannedReference(t *testing.T) {
	f := newPlanHistoryFixture(t)
	second := func(i int, s int) time.Time { return f.slot(i).Add(time.Duration(s) * time.Second) }
	savePlanSnapshot(t, second(0, -30), "ready", "hold", 0)        // before slot 0
	savePlanSnapshot(t, second(0, 32), "ready", "charge", 1200)    // governs slot 0
	savePlanSnapshot(t, second(1, 32), "ready", "hold", 0)         // governs slot 1 ...
	savePlanSnapshot(t, second(1, 482), "ready", "charge", 900)    // ... then changes; also slot 2's fallback
	savePlanSnapshot(t, second(3, 30), "unavailable", "charge", 0) // governs slot 3, unusable
	savePlanSnapshot(t, second(4, 30), "ready", "", 50)            // governs slot 4, no decision

	res, err := ComputePlanHistory(context.Background(), f.slot(0), f.slot(5), nil)
	require.NoError(t, err)
	s := res.Forecast

	p := s[0].Planned
	require.NotNil(t, p)
	require.Equal(t, second(0, 32).Unix(), p.Snapshot.Unix(), "the run inside the slot governs, not the one before")
	require.Equal(t, second(0, 32).Unix(), p.Start.Unix(), "its partial first entry is accepted")
	require.Equal(t, f.slot(1).Unix(), p.End.Unix())
	require.Equal(t, 1200.0, p.Devices[0].ChargeWh)
	require.Equal(t, 20.0, *p.Devices[0].Soc, "planned level is the slot-end level")
	require.Equal(t, 1200.0, *p.GridChargeMinWh)
	require.Equal(t, &PlanHistoryDecision{Action: "charge", Reason: "minimumRegret"}, p.Decision)

	require.Equal(t, &PlanHistoryDecision{Action: "hold", Reason: "minimumRegret", Sequence: []string{"hold", "charge"}}, s[1].Planned.Decision)

	require.NotNil(t, s[2].Planned, "without a run inside the slot the latest earlier run governs")
	require.Equal(t, second(1, 482).Unix(), s[2].Planned.Snapshot.Unix())
	require.Equal(t, f.slot(2).Unix(), s[2].Planned.Start.Unix(), "a whole-slot entry holds the slot start")
	require.Equal(t, "charge", s[2].Planned.Decision.Action)
	require.Nil(t, s[2].Planned.Decision.Sequence)

	require.Nil(t, s[3].Planned, "an unusable governing run is not replaced by an older one")

	require.NotNil(t, s[4].Planned)
	require.Nil(t, s[4].Planned.Decision, "no decision recorded, none invented")
	require.Equal(t, 50.0, s[4].Planned.Devices[0].ChargeWh)
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
