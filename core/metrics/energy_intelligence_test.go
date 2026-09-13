package metrics

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"github.com/stretchr/testify/require"
)

func TestHomeForecastCalendarCoverage(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)
	at := time.Date(2026, 9, 14, 0, 0, 0, 0, loc)
	var rows []meter
	for ts := at.AddDate(0, 0, -30); ts.Before(at); ts = ts.Add(tariff.SlotDuration) {
		i := profileIndex(ts)
		if i == 18 || i == 26 {
			continue
		}
		v := .2
		if dayType(ts) == 1 {
			v = .8
		}
		if i == 0 {
			v = 0
		}
		rows = append(rows, meter{Timestamp: ts.Unix(), Energy: v})
	}
	p, q, err := buildHomeProfile(rows, at)
	require.NoError(t, err)
	require.Equal(t, 94, q.CoveredBuckets)
	require.Equal(t, "interpolated", q.Source)
	require.InDelta(t, 200, p[0][18].Base, 1e-8)
	require.InDelta(t, 800, p[1][30].Base, 1e-8)
	require.Zero(t, p[0][0].Base)
	for i := range rows {
		rows[i].Recovered = true
	}
	_, q, err = buildHomeProfile(rows, at)
	require.ErrorIs(t, err, ErrIncomplete)
	require.Zero(t, q.Samples)
}

func TestHomeForecastRecencyAndSparseRefusal(t *testing.T) {
	at := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	var rows []meter
	for ts := at.AddDate(0, 0, -30); ts.Before(at); ts = ts.Add(tariff.SlotDuration) {
		v := .1
		if ts.After(at.AddDate(0, 0, -7)) {
			v = .5
		}
		rows = append(rows, meter{Timestamp: ts.Unix(), Energy: v})
	}
	p, _, err := buildHomeProfile(rows, at)
	require.NoError(t, err)
	require.Greater(t, p[0][50].Base, 200.)
	_, _, err = buildHomeProfile(rows[:100], at)
	require.ErrorIs(t, err, ErrIncomplete)
	for i := range rows {
		if profileIndex(time.Unix(rows[i].Timestamp, 0)) == 19 || profileIndex(time.Unix(rows[i].Timestamp, 0)) == 20 {
			rows[i].Incomplete = true
		}
	}
	_, _, err = buildHomeProfile(rows, at)
	require.ErrorIs(t, err, ErrIncomplete)
}

func TestHomeForecastDSTAndWalkForward(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)
	for _, date := range []time.Time{time.Date(2026, 3, 29, 0, 0, 0, 0, loc), time.Date(2026, 10, 25, 0, 0, 0, 0, loc)} {
		require.NoError(t, db.NewInstance("sqlite", ":memory:"))
		require.NoError(t, SetupSchema())
		var rows []meter
		for ts := date.AddDate(0, 0, -10); ts.Before(date); ts = ts.Add(tariff.SlotDuration) {
			rows = append(rows, meter{Meter: 1, Timestamp: ts.Unix(), Energy: .25})
		}
		require.NoError(t, db.Instance.CreateInBatches(rows, 100).Error)
		future := meter{Meter: 1, Timestamp: date.Add(time.Hour).Unix(), Energy: 999}
		require.NoError(t, db.Instance.Create(&future).Error)
		res, err := HomeForecast(date, date.AddDate(0, 0, 1))
		require.NoError(t, err)
		require.Len(t, res.Rates, int(date.AddDate(0, 0, 1).Sub(date)/tariff.SlotDuration))
		for i, s := range res.Rates {
			require.InDelta(t, 250, s.Base, 1e-8)
			if i > 0 {
				require.Equal(t, tariff.SlotDuration, s.Start.Sub(res.Rates[i-1].Start))
			}
		}
	}
}

func TestHomeForecastBoundedLastGood(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	at := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	var rows []meter
	for ts := at.AddDate(0, 0, -10); ts.Before(at.AddDate(0, 0, -3)); ts = ts.Add(tariff.SlotDuration) {
		rows = append(rows, meter{Meter: 1, Timestamp: ts.Unix(), Energy: .2})
	}
	require.NoError(t, db.Instance.CreateInBatches(rows, 100).Error)
	res, err := HomeForecast(at, at.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, "cached", res.Quality.Source)
	require.NotNil(t, res.Quality.LastGoodAgeSeconds)
	_, err = HomeForecast(at.AddDate(0, 0, 10), at.AddDate(0, 0, 10).Add(time.Hour))
	require.ErrorIs(t, err, ErrIncomplete)
}

func replayExample(t *testing.T, wear *float64) (*DecisionOutcome, []controlSlot, map[int64]slotData, batteryPhysics) {
	t.Helper()
	at := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	soc := 0.
	charge := "charge"
	rows := []controlSlot{{Timestamp: at.Unix(), AppliedMode: "normal", SuggestedMode: &charge}, {Timestamp: at.Add(tariff.SlotDuration).Unix(), AppliedMode: "normal"}}
	slots := map[int64]slotData{at.Unix(): {Start: at, BatterySocFrac: &soc, PriceGrid: .20}, at.Add(tariff.SlotDuration).Unix(): {Start: at.Add(tariff.SlotDuration), HomeKWh: 4.5, PriceGrid: .40}}
	p := batteryPhysics{CapacityKWh: 10, EtaC: .9, EtaD: 1, MaxChargeKWh: 5, MaxDischargeKWh: 5, WearPerKWh: wear}
	return replayOutcome(0, rows, slots, p, "test", at.Add(2*tariff.SlotDuration), nil), rows, slots, p
}

func TestDecisionOutcomeDelayedPayoffAndWear(t *testing.T) {
	// Rejecting a EUR 1 charge loses EUR 1.80 avoided import in the next slot.
	o, rows, slots, p := replayExample(t, nil)
	require.Equal(t, "completed", o.Status)
	require.InDelta(t, .8, *o.CashDeltaEUR, 1e-9)
	require.InDelta(t, .8, *o.NetDeltaEUR, 1e-9)
	require.Nil(t, o.WearDeltaEUR)
	require.Zero(t, *o.TerminalEnergyDeltaKWh)
	// At EUR 0.20 per DC kWh, the rejected strategy wears EUR 0.90, reversing net benefit.
	wear := .2
	o, _, _, _ = replayExample(t, &wear)
	require.InDelta(t, -.9, *o.WearDeltaEUR, 1e-9)
	require.InDelta(t, -.1, *o.NetDeltaEUR, 1e-9)
	rows[1].ModeChanged = true
	o = replayOutcome(0, rows, slots, p, "test", time.Unix(rows[1].Timestamp, 0).Add(tariff.SlotDuration), nil)
	require.Equal(t, "interrupted", o.Status)
	require.Equal(t, 1, o.Slots)
	require.InDelta(t, -1, *o.CashDeltaEUR, 1e-9)
	require.InDelta(t, -4.5, *o.TerminalEnergyDeltaKWh, 1e-9)
	require.InDelta(t, -.1, *o.NetDeltaEUR, 1e-9)
	rows[0].ModeChanged = true
	o = replayOutcome(0, rows, slots, p, "test", time.Now(), nil)
	require.Equal(t, "unpriced", o.Status)
	require.Nil(t, o.NetDeltaEUR)
}

func TestSnapshotsRetentionAndDeletion(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	at := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	s := OptimizerSnapshot{Timestamp: at, ControllerVersion: "v1", Request: json.RawMessage(`{"price":0}`)}
	id, err := SaveOptimizerSnapshot(s)
	require.NoError(t, err)
	require.NoError(t, PersistControlSlot(at, "normal", nil, "", true, nil))
	require.NoError(t, BindControlSlotSnapshot(at, id))
	got, err := GetOptimizerSnapshot(id)
	require.NoError(t, err)
	require.Equal(t, s.Request, got.Request)
	require.Equal(t, 1, got.Version)
	s.Timestamp = at.AddDate(0, 0, 31)
	_, err = SaveOptimizerSnapshot(s)
	require.NoError(t, err)
	_, err = GetOptimizerSnapshot(id)
	require.Error(t, err)
	var control controlSlot
	require.NoError(t, db.Instance.First(&control).Error)
	require.Nil(t, control.OptimizerSnapshotID)
	n, err := DeleteOptimizerSnapshots(s.Timestamp, s.Timestamp.Add(time.Second))
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
}

func TestHomeForecastRealAnonymizedHistory(t *testing.T) {
	path := os.Getenv("EVCC_HOME_HISTORY_FIXTURE")
	if path == "" {
		t.Skip("set EVCC_HOME_HISTORY_FIXTURE to an external anonymized history JSON")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var rows []struct {
		TS     int64   `json:"ts"`
		Energy float64 `json:"energy"`
	}
	require.NoError(t, json.Unmarshal(raw, &rows))
	loc, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)
	var samples []meter
	var last int64
	for _, r := range rows {
		samples = append(samples, meter{Timestamp: r.TS, Energy: r.Energy})
		last = max(last, r.TS)
	}
	_, q, err := buildHomeProfile(samples, time.Unix(last, 0).Add(tariff.SlotDuration).In(loc))
	require.NoError(t, err)
	t.Logf("real profile: %d samples, %d/96 buckets, source=%s, missing=%v", q.Samples, q.CoveredBuckets, q.Source, q.MissingBuckets)
	require.Equal(t, 94, q.CoveredBuckets)
	require.Equal(t, []int{18, 26}, q.MissingBuckets)
}

func TestInventoryBoundaryCannotCreditFreeDrain(t *testing.T) {
	at := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	soc := .45
	p := batteryPhysics{CapacityKWh: 10, EtaC: .9, EtaD: 1, MaxChargeKWh: 5, MaxDischargeKWh: 5}
	// Ordinary operation empties 4.5 kWh of opening stock. Actual control holds it.
	slots := []slotData{{Start: at, HomeKWh: 4.5, GridImportKWh: 4.5, PriceGrid: .4, BatterySocFrac: &soc}}
	r := inventoryAdjustedFromSlots(slots, []batteryPhysics{p}, &InventoryAdjustedChain{}, map[int64]float64{at.Add(tariff.SlotDuration).Unix(): soc})
	require.InDelta(t, 1.8, r.TerminalAdjustment.PerSlot, 1e-9)
	require.InDelta(t, 0, r.Control.Full, 1e-9, "EUR 1.80 avoided cash purchase consumes EUR 1.80 opening inventory")
	require.Zero(t, r.EstimatedEndpoints)
	sum := 0.
	for _, c := range r.Contributions {
		sum += c.Settled.PerSlot
	}
	require.InDelta(t, r.Worlds[0].Settled.PerSlot-r.Worlds[3].Settled.PerSlot, sum, 1e-9)
	// A missing interval starts another explicitly partial inventory-neutral segment.
	slots = append(slots, slotData{Start: at.Add(time.Hour), HomeKWh: 4.5, GridImportKWh: 4.5, PriceGrid: .4, BatterySocFrac: &soc})
	r = inventoryAdjustedFromSlots(slots, []batteryPhysics{p, p}, &InventoryAdjustedChain{}, nil)
	require.Equal(t, "partial", r.Status)
	require.Equal(t, 2, r.Segments)
	require.InDelta(t, 0, r.Control.Full, 1e-9)
}

func TestBatteryEfficiencyRequiresIndependentCapacityAndACPlane(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	at := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	capacity := 10.
	b := entity{Group: Battery, Name: "battery", CapacityKWh: &capacity}
	require.NoError(t, db.Instance.Create(&b).Error)
	var rows []meter
	soc := 20.
	for i := 0; i <= 24; i++ {
		v := soc
		r := meter{Meter: b.Id, Timestamp: at.Add(time.Duration(i) * tariff.SlotDuration).Unix(), SocTemp: &v}
		if i < 12 {
			r.ReturnEnergy = .4
			soc += 3.6
		} else if i < 24 {
			r.Energy = .324
			soc -= 3.6
		}
		rows = append(rows, r)
	}
	require.NoError(t, db.Instance.Create(&rows).Error)
	c, err := BatteryEfficiencyCandidates(at)
	require.NoError(t, err)
	require.Len(t, c, 1)
	require.False(t, c[0].Applicable)
	require.Equal(t, "unknown", c[0].MeasurementPlane)
	require.InDelta(t, .9, *c[0].ChargeEfficiency, 1e-9)
	require.InDelta(t, .9, *c[0].DischargeEfficiency, 1e-9)
	require.NoError(t, SetBatteryMeasurementPlane("battery", "dc"))
	c, err = BatteryEfficiencyCandidates(at)
	require.NoError(t, err)
	require.False(t, c[0].Applicable)
	require.NoError(t, SetBatteryMeasurementPlane("battery", "ac"))
	c, err = BatteryEfficiencyCandidates(at)
	require.NoError(t, err)
	require.True(t, c[0].Applicable)
	require.NoError(t, db.Instance.Model(new(meter)).Where("meter = ? AND ts = ?", b.Id, at.Unix()).Update("incomplete", true).Error)
	c, err = BatteryEfficiencyCandidates(at)
	require.NoError(t, err)
	require.Nil(t, c[0].ChargeEfficiency)
	require.NoError(t, db.Instance.Model(&b).Update("capacity_kwh", nil).Error)
	c, err = BatteryEfficiencyCandidates(at)
	require.NoError(t, err)
	require.Nil(t, c[0].ChargeEfficiency)
	require.Equal(t, "capacity_unavailable", c[0].Source)
}

func TestForecastCalibrationUsesOnlyIssuedCleanPastPairs(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	at := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 31; i++ {
		ts := at.Add(-time.Duration(i+1) * tariff.SlotDuration)
		f := homeForecastSample{Slot: ts.Unix(), Issued: ts.Add(-time.Hour).Unix(), LeadMinutes: 60, Base: .1, Low: 0, High: .2}
		require.NoError(t, db.Instance.Create(&f).Error)
		require.NoError(t, persist(entity{Id: 1}, ts, .3, 0, nil, false, i == 0))
	}
	res := &HomeForecastResult{Rates: []HomeForecastSlot{{Start: at.Add(time.Hour), Base: 100, Low: 50, High: 150}}}
	require.NoError(t, calibrateHomeRanges(at, res))
	require.Equal(t, 30, res.Quality.CalibrationSamples)
	require.Equal(t, "forecast_errors", res.Quality.RangeSource)
	require.InDelta(t, 300, res.Rates[0].High, 1e-9)
	// A forecast issued after its target cannot become calibration evidence.
	future := homeForecastSample{Slot: at.Add(time.Hour).Unix(), Issued: at.Add(2 * time.Hour).Unix(), LeadMinutes: 60, Base: 0}
	require.NoError(t, db.Instance.Create(&future).Error)
	require.NoError(t, persist(entity{Id: 1}, time.Unix(future.Slot, 0), 999, 0, nil, false, false))
	require.NoError(t, calibrateHomeRanges(at, res))
	require.Equal(t, 30, res.Quality.CalibrationSamples)
}

func TestSolarArchiveRejectsInvalidEnergyAndIncompleteArrays(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	at := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	require.Error(t, ArchiveForecastSample(at, func(time.Time, time.Time) (float64, bool) { return -1, true }))
	var n int64
	require.NoError(t, db.Instance.Model(new(forecastSample)).Count(&n).Error)
	require.Zero(t, n)
	p1 := entity{Group: PV, Name: "roof1"}
	p2 := entity{Group: PV, Name: "roof2"}
	require.NoError(t, db.Instance.Create(&p1).Error)
	require.NoError(t, db.Instance.Create(&p2).Error)
	f := forecastSample{Slot: at.Unix(), LeadMinutes: 60, Energy: .5}
	require.NoError(t, db.Instance.Create(&f).Error)
	require.NoError(t, persist(p1, at, .3, 0, nil, false, false))
	s, err := QueryLeadTimeSamples(at)
	require.NoError(t, err)
	require.Empty(t, s)
	require.NoError(t, persist(p2, at, .2, 0, nil, false, true))
	s, err = QueryLeadTimeSamples(at)
	require.NoError(t, err)
	require.Empty(t, s)
	require.NoError(t, db.Instance.Model(new(meter)).Where("meter = ?", p2.Id).Update("incomplete", false).Error)
	s, err = QueryLeadTimeSamples(at)
	require.NoError(t, err)
	require.Len(t, s, 1)
	require.InDelta(t, .5, s[0].Actual, 1e-9)
}

func TestDecisionSnapshotsSurviveNewRunsButNotDeletedEconomics(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	_, rows, slots, p := replayExample(t, nil)
	encoded, err := json.Marshal(struct {
		Batteries []SnapshotBatteryEconomics `json:"batteries"`
	}{[]SnapshotBatteryEconomics{{Name: "battery", CapacityKWh: 10, EtaC: .9, EtaD: 1, MaxChargeKWh: 5, MaxDischargeKWh: 5, Source: "known"}}})
	require.NoError(t, err)
	for i := range rows {
		id, err := SaveOptimizerSnapshot(OptimizerSnapshot{Timestamp: time.Unix(rows[i].Timestamp, 0), ControllerVersion: "v1", Economics: encoded})
		require.NoError(t, err)
		rows[i].OptimizerSnapshotID = &id
		require.NoError(t, db.Instance.Create(&rows[i]).Error)
	}
	from := time.Unix(rows[0].Timestamp, 0)
	to := from.Add(2 * tariff.SlotDuration)
	set := &ledgerSlotSet{Slots: []slotData{slots[rows[0].Timestamp], slots[rows[1].Timestamp]}}
	out, err := DecisionDeltas(context.Background(), from, to, set, &p)
	require.NoError(t, err)
	require.Equal(t, "completed", out[0].Outcome.Status)
	require.Equal(t, "optimizer_snapshot", out[0].Outcome.AssumptionsSource)
	require.InDelta(t, .8, *out[0].Outcome.NetDeltaEUR, 1e-9)
	_, err = DeleteOptimizerSnapshots(from, from.Add(time.Second))
	require.NoError(t, err)
	out, err = DecisionDeltas(context.Background(), from, to, set, &p)
	require.NoError(t, err)
	require.Equal(t, "unpriced", out[0].Outcome.Status)
	require.Equal(t, "historical_snapshot_expired_or_deleted", out[0].Outcome.Reason)
	require.Nil(t, out[0].SlotFlowDeltaEUR)
}
