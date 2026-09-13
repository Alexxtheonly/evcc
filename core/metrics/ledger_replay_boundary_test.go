package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"github.com/stretchr/testify/require"
)

func TestDecisionReplayIncludesPayoffAfterSelectedDay(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	grid := mustCreateEntity(t, Grid, Grid)
	home := mustCreateEntity(t, Home, Home)
	bat := mustCreateEntity(t, Battery, "battery")
	from := time.Date(2026, 8, 6, 23, 45, 0, 0, time.UTC)
	to := from.Add(tariff.SlotDuration)
	soc, feedIn, cheap, expensive := 0., 0., .2, .4
	require.NoError(t, persist(grid, from, 0, 0, nil, false, false))
	require.NoError(t, persist(home, from, 0, 0, nil, false, false))
	require.NoError(t, persist(bat, from, 0, 0, &soc, false, false))
	require.NoError(t, PersistTariffs(from, &cheap, &feedIn, nil, nil))
	suggestion := "charge"
	require.NoError(t, PersistControlSlot(from, "normal", &suggestion, "payback", true, nil))
	require.NoError(t, persist(grid, to, 4.5, 0, nil, false, false))
	require.NoError(t, persist(home, to, 4.5, 0, nil, false, false))
	require.NoError(t, persist(bat, to, 0, 0, &soc, false, false))
	require.NoError(t, PersistTariffs(to, &expensive, &feedIn, nil, nil))
	require.NoError(t, PersistControlSlot(to, "normal", nil, "", true, nil))
	set, err := buildLedgerSlots(context.Background(), from, to, true, true, nil)
	require.NoError(t, err)
	phys := batteryPhysics{CapacityKWh: 10, EtaC: .9, EtaD: 1, MaxChargeKWh: 5, MaxDischargeKWh: 5}
	rows, err := DecisionDeltas(context.Background(), from, to, set, &phys)
	require.NoError(t, err)
	require.Len(t, rows, 1, "future evidence must not add rows to the selected view")
	require.Equal(t, "completed", rows[0].Outcome.Status)
	require.Equal(t, to.Add(tariff.SlotDuration), *rows[0].Outcome.Through)
	require.InDelta(t, .8, *rows[0].Outcome.NetDeltaEUR, 1e-9)
	for i := 1; i <= minFeedInWitnessSlots; i++ {
		require.NoError(t, PersistTariffs(from.Add(-time.Duration(i)*tariff.SlotDuration), &cheap, &feedIn, nil, nil))
	}
	require.NoError(t, db.Instance.Model(new(tariffValue)).Where("ts = ?", to.Unix()).Update("feedin", nil).Error)
	rows, err = DecisionDeltas(context.Background(), from, to, set, &phys, &feedIn)
	require.NoError(t, err)
	require.Equal(t, "completed", rows[0].Outcome.Status)
	rows, err = DecisionDeltas(context.Background(), from, to, set, &phys)
	require.NoError(t, err)
	require.Equal(t, "interrupted", rows[0].Outcome.Status)
}

func TestDecisionReplayExcludesUncompletedCurrentSlot(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	to := time.Now().Truncate(tariff.SlotDuration)
	from := to.Add(-tariff.SlotDuration)
	soc := 0.
	suggestion := "charge"
	require.NoError(t, PersistControlSlot(from, "normal", &suggestion, "", true, nil))
	require.NoError(t, PersistControlSlot(to, "normal", nil, "", true, nil))
	set := &ledgerSlotSet{Slots: []slotData{{Start: from, BatterySocFrac: &soc, PriceGrid: .2}, {Start: to, BatterySocFrac: &soc, HomeKWh: 4.5, PriceGrid: .4}}}
	p := batteryPhysics{CapacityKWh: 10, EtaC: .9, EtaD: 1, MaxChargeKWh: 5, MaxDischargeKWh: 5}
	rows, err := DecisionDeltas(context.Background(), from, to, set, &p)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "pending", rows[0].Outcome.Status)
	require.Equal(t, 1, rows[0].Outcome.Slots)
}

func TestBatteryModeReplayMatchesDirectionalInverterLimits(t *testing.T) {
	p := batteryPhysics{CapacityKWh: 10, EtaC: .9, EtaD: .9, MaxChargeKWh: 5, MaxDischargeKWh: 5}
	// Fronius model124: hold sets only the discharge limit; holdcharge sets only the charge limit.
	for _, tt := range []struct {
		name, mode                                string
		load, pv, wantSoc, wantImport, wantExport float64
	}{
		{"hold permits solar charge", "hold", 0, 2, 6.8, 0, 0},
		{"hold prevents discharge", "hold", 2, 0, 5, 2, 0},
		{"holdcharge prevents solar charge", "holdcharge", 0, 2, 5, 0, 2},
		{"holdcharge permits discharge", "holdcharge", 1.8, 0, 3, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			soc, flow, ok := simulateSlotStep(tt.mode, tt.load, tt.pv, 5, p)
			require.True(t, ok)
			require.InDelta(t, tt.wantSoc, soc, 1e-9)
			require.InDelta(t, tt.wantImport, flow.ImportKWh, 1e-9)
			require.InDelta(t, tt.wantExport, flow.ExportKWh, 1e-9)
		})
	}
}
