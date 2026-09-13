package core

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/util"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnergyGridAllocation(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		solar, demand, imp, exp          float32
		charge, discharge                []float32
		importOvershoot, exportOvershoot float32
		demandRate                       bool
		wantImport, low, high            float64
	}{
		{name: "grid only", demand: 200, imp: 700, charge: []float32{500}, discharge: []float32{0}, wantImport: 700, low: 500, high: 500},
		{name: "solar only", solar: 800, demand: 200, exp: 100, charge: []float32{500}, discharge: []float32{0}},
		{name: "mixed supply", solar: 300, demand: 200, imp: 400, charge: []float32{500}, discharge: []float32{0}, wantImport: 400, low: 200, high: 400},
		{name: "house consumes import", solar: 500, demand: 200, imp: 200, charge: []float32{500}, discharge: []float32{0}, wantImport: 200, high: 200},
		{name: "uncontrolled demand without charging", demand: 1200, imp: 1200, charge: []float32{0}, discharge: []float32{0}, wantImport: 1200},
		{name: "battery supplies vehicle", demand: 200, imp: 200, charge: []float32{0, 500}, discharge: []float32{500, 0}, wantImport: 200, high: 200},
		{name: "multiple devices", solar: 300, demand: 200, imp: 1100, charge: []float32{500, 700, 0}, discharge: []float32{0, 0, 100}, wantImport: 1100, low: 800, high: 1100},
		{name: "import limit overshoot", demand: 200, imp: 400, importOvershoot: 300, charge: []float32{500}, discharge: []float32{0}, wantImport: 700, low: 500, high: 500},
		{name: "demand rate includes overshoot", demand: 200, imp: 700, importOvershoot: 300, demandRate: true, charge: []float32{500}, discharge: []float32{0}, wantImport: 700, low: 500, high: 500},
		{name: "export limit overshoot", solar: 500, demand: 200, imp: 400, exp: 100, exportOvershoot: 100, charge: []float32{500}, discharge: []float32{0}, wantImport: 400, high: 400},
		{name: "empty plan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Date(2026, 1, 1, 12, 7, 0, 0, time.UTC)
			site := &Site{energyInsights: optimizerInsights{Forecast: []energyForecastSlot{{Start: start, End: start.Add(8 * time.Minute)}}}}
			req := optimizer.OptimizationInput{EtaC: .8, EtaD: .9, TimeSeries: optimizer.TimeSeries{Dt: []int{480}, Ft: []float32{tc.solar}, Gt: []float32{tc.demand}}}
			res := optimizer.OptimizationResult{GridImport: []float32{tc.imp}, GridExport: []float32{tc.exp}}
			for i, charge := range tc.charge {
				res.Batteries = append(res.Batteries, optimizer.BatteryResult{ChargingPower: []float32{charge}, DischargingPower: []float32{tc.discharge[i]}})
			}
			if tc.importOvershoot > 0 {
				req.Grid.PMaxImp = 3000
				res.GridImportOvershoot = []float32{tc.importOvershoot}
			}
			if tc.demandRate {
				req.Grid.PrcPExcImp = .001
			}
			if tc.exportOvershoot > 0 {
				req.Grid.PMaxExp = 1000
				res.GridExportOvershoot = []float32{tc.exportOvershoot}
			}
			site.populateEnergyGrid(req, res)
			slot := site.energyInsights.Forecast[0]
			require.NotNil(t, slot.GridImportWh)
			require.NotNil(t, slot.GridChargeMinWh)
			require.NotNil(t, slot.GridChargeMaxWh)
			assert.Equal(t, tc.wantImport, *slot.GridImportWh)
			assert.Equal(t, tc.low, *slot.GridChargeMinWh)
			assert.Equal(t, tc.high, *slot.GridChargeMaxWh)
			assert.Equal(t, start, slot.Start)
			assert.Equal(t, start.Add(8*time.Minute), slot.End)
			encoded, err := json.Marshal(slot)
			require.NoError(t, err)
			assert.Contains(t, string(encoded), `"gridImportWh":`)
			assert.Contains(t, string(encoded), `"gridChargeMinWh":`)
			assert.Contains(t, string(encoded), `"gridChargeMaxWh":`)
		})
	}
}

func TestEnergyGridRejectsInconsistentBounds(t *testing.T) {
	for _, imp := range []float32{499.95, 400} {
		site := &Site{energyInsights: optimizerInsights{Forecast: []energyForecastSlot{{}}}}
		req := optimizer.OptimizationInput{TimeSeries: optimizer.TimeSeries{Ft: []float32{0}}}
		res := optimizer.OptimizationResult{GridImport: []float32{imp}, Batteries: []optimizer.BatteryResult{{ChargingPower: []float32{500}, DischargingPower: []float32{0}}}}
		site.populateEnergyGrid(req, res)
		slot := site.energyInsights.Forecast[0]
		if imp == 400 {
			assert.Nil(t, slot.GridImportWh)
			assert.Nil(t, slot.GridChargeMinWh)
			assert.Nil(t, slot.GridChargeMaxWh)
		} else {
			require.NotNil(t, slot.GridChargeMinWh)
			assert.Equal(t, float64(imp), *slot.GridChargeMinWh)
			assert.Equal(t, slot.GridChargeMinWh, slot.GridChargeMaxWh)
		}
	}
}

func TestEnergyGridUnavailable(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	params := make(chan util.Param, 1)
	zero := 0.0
	site := &Site{log: util.NewLogger("energy-grid-test"), valueChan: params, energyInsights: optimizerInsights{
		Forecast: []energyForecastSlot{{GridImportWh: &zero, GridChargeMinWh: &zero, GridChargeMaxWh: &zero}},
		Devices:  []energyDevice{{Key: "battery"}},
	}}
	site.publishEnergyInsights(errors.New("plan unavailable"))
	value := (<-params).Val.(optimizerInsights)
	assert.Equal(t, "unavailable", value.Status)
	assert.Nil(t, value.Devices)
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), `"gridImportWh"`)
	assert.NotContains(t, string(encoded), `"gridChargeMinWh"`)
	assert.NotContains(t, string(encoded), `"gridChargeMaxWh"`)
}
