package core

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/shading"
	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/server/db/settings"
	"github.com/evcc-io/evcc/tariff"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/jinzhu/now"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var shadingTestLoc = &tariff.Location{Lat: 49.2115, Lon: 9.1949}

// newShadingSite returns a site on an in-memory database holding 21 completed
// days of 60 min lead forecasts and PV readings: every daylight slot measured
// 40 % or 60 % of its forecast, alternating, so the learned factor is about 0.5
// with a visible spread.
func newShadingSite(t *testing.T, withData bool, locs ...*tariff.Location) *Site {
	t.Helper()
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())
	t.Cleanup(func() {
		_ = settings.SetJson(energyIntelligenceKey, EnergyIntelligenceSettings{SettlementMode: "simulation"})
	})

	require.NoError(t, db.Instance.Exec(`INSERT INTO entities ("group", name, title) VALUES ('pv', 'db:5', 'PV')`).Error)
	var pv int
	require.NoError(t, db.Instance.Raw(`SELECT id FROM entities WHERE name = 'db:5'`).Scan(&pv).Error)

	if withData {
		bod := now.BeginningOfDay()
		for ts, i := bod.AddDate(0, 0, -21), 0; ts.Before(bod); ts, i = ts.Add(15*time.Minute), i+1 {
			if _, el := shading.SunPosition(ts.Add(shading.SlotDuration/2), shading.Location{Lat: 49.2115, Lon: 9.1949}); el <= 0 {
				continue
			}
			measured := 0.3 * []float64{0.4, 0.6}[(i+i/96)%2] // alternates per slot and per day
			require.NoError(t, db.Instance.Exec(`INSERT INTO forecast_samples (slot, lead_minutes, energy) VALUES (?, 60, 0.3)`, ts.Unix()).Error)
			require.NoError(t, db.Instance.Exec(`INSERT INTO meters (meter, ts, energy) VALUES (?, ?, ?)`, pv, ts.Unix(), measured).Error)
		}
	}

	site := NewSite()
	site.tariffs = &tariff.Tariffs{SolarLocations: locs}
	return site
}

func setSolarShading(t *testing.T, on bool) {
	t.Helper()
	require.NoError(t, settings.SetJson(energyIntelligenceKey, EnergyIntelligenceSettings{SettlementMode: "simulation", SolarShading: on}))
}

// tomorrow's slots at a flat 100 Wh each
func flatSolar() api.Rates {
	start := now.BeginningOfDay().AddDate(0, 0, 1)
	var res api.Rates
	for i := range 96 {
		s := start.Add(time.Duration(i) * tariff.SlotDuration)
		res = append(res, api.Rate{Start: s, End: s.Add(tariff.SlotDuration), Value: 100})
	}
	return res
}

func TestSolarShadingOffIsUnchanged(t *testing.T) {
	site := newShadingSite(t, true, shadingTestLoc)
	setSolarShading(t, false)

	solar := flatSolar()
	ft, spread := site.solarSlots(solar, time.Now(), len(solar), tariff.SlotDuration)
	assert.Nil(t, spread)
	assert.Equal(t, prorate(scaleAndPruneByLead(solar, time.Now(), func(time.Duration) float64 { return 1 }, len(solar)), tariff.SlotDuration), ft)
	assert.Nil(t, site.correctedSolarDetails(solar, solarDetails{}))
	assert.Nil(t, site.activeSolarShading())

	// the map is still learned for review
	st := site.SolarShading()
	assert.False(t, st.Enabled)
	assert.False(t, st.Active)
	assert.True(t, st.Available)
	assert.NotEmpty(t, st.Bins)
	assert.Equal(t, 21, st.Days)
	assert.Equal(t, "none", st.Fallback, "solarAdjusted is off")
}

func TestSolarShadingOnCorrectsConsumers(t *testing.T) {
	site := newShadingSite(t, true, shadingTestLoc)
	setSolarShading(t, true)

	m := site.activeSolarShading()
	require.NotNil(t, m)

	solar := flatSolar()
	ft, spread := site.solarSlots(solar, time.Now(), len(solar), tariff.SlotDuration)
	require.Len(t, ft, len(solar))
	require.Len(t, spread, len(solar))

	var day, night int
	for i, r := range solar {
		c := m.Factor(r.Start, 1)
		assert.InDelta(t, 100*c.Factor, float64(ft[i]), 1e-3, "slot %v", r.Start)
		switch c.Source {
		case shading.SourceNight:
			night++
			assert.Equal(t, float32(100), ft[i])
		case shading.SourceLearned:
			day++
			assert.InDelta(t, 0.5, c.Factor, 0.05)
			assert.Less(t, spread[i].low, 1.0)
			assert.Greater(t, spread[i].high, 1.0)
		}
	}
	assert.Positive(t, day)
	assert.Positive(t, night)

	// the published forecast carries the same correction
	corrected := site.correctedSolarDetails(solar, solarDetails{})
	require.NotNil(t, corrected)
	raw := solarEnergy(solar, now.BeginningOfDay().AddDate(0, 0, 1), now.BeginningOfDay().AddDate(0, 0, 2))
	assert.Less(t, corrected.Tomorrow.Yield, raw)
	assert.Greater(t, corrected.Tomorrow.Yield, 0.4*raw)

	// the Plan view's forecast and the scenarios' low/high get the widened band
	site.energySolarSpread = spread
	site.energyHome = make([]float64, len(solar))
	site.energyProfile = &metrics.HomeForecastResult{Rates: make([]metrics.HomeForecastSlot, len(solar))}
	req := optimizer.OptimizationInput{TimeSeries: optimizer.TimeSeries{
		Dt: make([]int, len(solar)), Ft: ft, PN: make([]float32, len(solar)),
	}}
	details := requestDetails{}
	for i, r := range solar {
		req.TimeSeries.Dt[i] = int(tariff.SlotDuration.Seconds())
		details.Timestamps = append(details.Timestamps, r.Start)
	}
	require.NoError(t, site.energyForecast(req, details))
	require.Len(t, site.energyInsights.Forecast, len(solar))
	for i, f := range site.energyInsights.Forecast {
		if m.Factor(solar[i].Start, 1).Source != shading.SourceLearned {
			continue
		}
		assert.InDelta(t, float64(ft[i]), f.SolarWh, 1e-3)
		assert.GreaterOrEqual(t, f.SolarHighWh, f.SolarWh*spread[i].high-1e-6, "high widened by the bin spread")
		assert.LessOrEqual(t, f.SolarLowWh, f.SolarWh*spread[i].low+1e-6, "low widened by the bin spread")
	}

	st := site.SolarShading()
	assert.True(t, st.Enabled)
	assert.True(t, st.Active)
	assert.Equal(t, &shading.Location{Lat: 49.2115, Lon: 9.1949}, st.Location)
	assert.Equal(t, metrics.ShadingLeadMinutes, st.LeadMinutes)
	assert.Len(t, st.Windows, 1+shading.DefaultConfig.SeasonYears)
}

func TestSolarShadingUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		withData bool
		locs     []*tariff.Location
		reason   string
	}{
		{"no solar forecast", true, nil, solarShadingNoCoordinates},
		{"forecast without coordinates", true, []*tariff.Location{nil}, solarShadingNoCoordinates},
		{"no history", false, []*tariff.Location{shadingTestLoc}, solarShadingNoData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			site := newShadingSite(t, tc.withData, tc.locs...)
			setSolarShading(t, true)

			assert.Nil(t, site.activeSolarShading())

			// falls back to the daily path
			solar := flatSolar()
			ft, spread := site.solarSlots(solar, time.Now(), len(solar), tariff.SlotDuration)
			assert.Nil(t, spread)
			for _, v := range ft {
				assert.Equal(t, float32(100), v)
			}

			st := site.SolarShading()
			assert.True(t, st.Enabled)
			assert.False(t, st.Active)
			assert.False(t, st.Available)
			assert.Equal(t, tc.reason, st.Reason)
			assert.NotNil(t, st.Bins, "an empty list, never null")
		})
	}
}

func TestWidenSolarBand(t *testing.T) {
	low, high := widenSolarBand(100, 80, 110, solarSpread{0.6, 1.4})
	assert.Equal(t, 60.0, low)
	assert.InDelta(t, 140.0, high, 1e-9)

	// never narrows
	low, high = widenSolarBand(100, 20, 300, solarSpread{0.6, 1.4})
	assert.Equal(t, 20.0, low)
	assert.Equal(t, 300.0, high)

	// no information
	low, high = widenSolarBand(100, 80, 110, solarSpread{})
	assert.Equal(t, 80.0, low)
	assert.Equal(t, 110.0, high)
	assert.False(t, math.IsNaN(low))
}

func TestSolarShadingCachedPerDay(t *testing.T) {
	site := newShadingSite(t, true, shadingTestLoc)
	_, _, first, err := site.solarShadingMap()
	require.NoError(t, err)
	require.NoError(t, db.Instance.Exec(`DELETE FROM forecast_samples`).Error)
	_, _, again, err := site.solarShadingMap()
	require.NoError(t, err, "same day: the learned map is reused, not recomputed")
	assert.Equal(t, first, again)
}

// TestSolarShadingFromDatabase writes the status a real database copy produces
// (for review and UI fixtures). Skipped unless EVCC_SHADING_DB names a database
// file and EVCC_SHADING_OUT the JSON output; the database is copied first.
func TestSolarShadingFromDatabase(t *testing.T) {
	src, out := os.Getenv("EVCC_SHADING_DB"), os.Getenv("EVCC_SHADING_OUT")
	if src == "" || out == "" {
		t.Skip("EVCC_SHADING_DB/EVCC_SHADING_OUT not set")
	}
	b, err := os.ReadFile(src)
	require.NoError(t, err)
	dst := filepath.Join(t.TempDir(), "evcc.db")
	require.NoError(t, os.WriteFile(dst, b, 0o600))
	require.NoError(t, db.NewInstance("sqlite", dst))
	t.Cleanup(func() { _ = db.Close() })

	var other string
	require.NoError(t, db.Instance.Raw(`SELECT value FROM configs WHERE class = 4 AND value LIKE '%forecast-solar%'`).Scan(&other).Error)
	var conf map[string]any
	require.NoError(t, json.Unmarshal([]byte(other), &conf))
	loc, ok := tariff.LocationFromConfig(conf)
	require.True(t, ok)

	site := NewSite()
	site.tariffs = &tariff.Tariffs{SolarLocations: []*tariff.Location{&loc}}
	st := site.SolarShading()
	require.True(t, st.Available, st.Reason)

	res, err := json.MarshalIndent(st, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(out, res, 0o600))
	t.Logf("location %+v, %d days, %d slots, %d bins", loc, st.Days, st.Samples, len(st.Bins))
}
