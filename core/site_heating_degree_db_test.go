package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/jinzhu/now"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// TestHeatingDegreeFromDatabase evaluates the "on" gate on a real database copy
// for each local day of EVCC_HEATING_DAYS (from,to as YYYY-MM-DD, default the 30
// days before today), as the midnight refit of that day would, and writes the
// current status to EVCC_HEATING_OUT (for review and UI fixtures). Skipped
// unless EVCC_HEATING_DB names a database file; the file is copied first.
func TestHeatingDegreeFromDatabase(t *testing.T) {
	src := os.Getenv("EVCC_HEATING_DB")
	if src == "" {
		t.Skip("EVCC_HEATING_DB not set")
	}
	b, err := os.ReadFile(src)
	require.NoError(t, err)
	dst := filepath.Join(t.TempDir(), "evcc.db")
	require.NoError(t, os.WriteFile(dst, b, 0o600))
	require.NoError(t, db.NewInstance("sqlite", dst))
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, metrics.SetupSchema())

	from, to := now.BeginningOfDay().AddDate(0, 0, -30), now.BeginningOfDay()
	if v := os.Getenv("EVCC_HEATING_DAYS"); v != "" {
		f, l, ok := strings.Cut(v, ",")
		require.True(t, ok)
		from, err = time.ParseInLocation(time.DateOnly, f, time.Local)
		require.NoError(t, err)
		to, err = time.ParseInLocation(time.DateOnly, l, time.Local)
		require.NoError(t, err)
	}

	// the query's row order is what the auto fit sums in: selecting the slot
	// start must not change it
	all, err := metrics.QueryHomeTemperatureSamples(from.AddDate(0, 0, -30))
	require.NoError(t, err)
	var before []metrics.TemperatureSample
	require.NoError(t, db.Instance.Table("meters h").
		Select(`COALESCE(h.energy, 0) * 1000 AS energy, t.soc_temp AS temperature`).
		Joins(`JOIN entities eh ON eh.id = h.meter AND eh."group" = ?`, metrics.Home).
		Joins(`JOIN meters t ON t.ts = h.ts`).
		Joins(`JOIN entities et ON et.id = t.meter AND et."group" = ?`, metrics.Temperature).
		Where("h.ts >= ? AND t.soc_temp IS NOT NULL", from.AddDate(0, 0, -30).Unix()).
		Where("COALESCE(h.recovered, 0) = 0 AND COALESCE(h.incomplete, 0) = 0").
		Scan(&before).Error)
	require.Len(t, all, len(before))
	for i := range all {
		require.Equal(t, before[i].Energy, all[i].Energy, i)
		require.Equal(t, before[i].Temperature, all[i].Temperature, i)
	}
	t.Logf("query: %d rows, same order and values as before the slot start was selected", len(all))

	t.Log("date        samples days dailyR  slotR   slope Wh/slot/K kWh/day/K meanHDD on-gate             auto-gate")
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		lo, hi := day.AddDate(0, 0, -30).Unix(), day.Unix()
		var window []metrics.TemperatureSample
		for _, s := range all {
			if s.Ts >= lo && s.Ts < hi {
				window = append(window, s)
			}
		}
		a := analyzeHeatingDegree(window, time.Local)
		_, autoOK := fitHeatingDegree(window)
		gate := "open"
		if !a.Open {
			gate = "shut: " + a.Reason
		}
		t.Logf("%s %7d %4d %7s %7s %7s %9s %7.2f %-19s %v", day.Format(time.DateOnly), a.Samples, a.CompleteDays, ptr(a.DailyR, 3), ptr(a.SlotR, 3),
			fmt.Sprintf("%.2f", a.Fit.slope), fmt.Sprintf("%.3f", a.Fit.slope*96/1e3), a.Fit.meanHDD, gate, autoOK)
	}

	out := os.Getenv("EVCC_HEATING_OUT")
	if out == "" {
		return
	}

	// status as of now, with the recorded temperatures of the last two days as
	// the forecast (the copy holds no forecast)
	var temps []struct {
		Ts          int64
		Temperature float64
	}
	require.NoError(t, db.Instance.Raw(`SELECT t.ts, t.soc_temp AS temperature FROM meters t JOIN entities e ON e.id = t.meter AND e."group" = ? WHERE t.soc_temp IS NOT NULL ORDER BY t.ts DESC LIMIT 192`, metrics.Temperature).Scan(&temps).Error)
	start := time.Now().Truncate(tariff.SlotDuration)
	var rates api.Rates
	for i := range temps {
		s := start.Add(time.Duration(i) * tariff.SlotDuration)
		rates = append(rates, api.Rate{Start: s, End: s.Add(tariff.SlotDuration), Value: temps[len(temps)-1-i].Temperature})
	}
	ctrl := gomock.NewController(t)
	temp := api.NewMockTariff(ctrl)
	temp.EXPECT().Type().Return(api.TariffTypeTemperature).AnyTimes()
	temp.EXPECT().Rates().Return(rates, nil).AnyTimes()

	site := &Site{log: util.NewLogger("heating"), tariffs: &tariff.Tariffs{Temperature: temp}}
	site.heatingDegreeCached = func() (heatingDegreeResult, error) { return site.queryHeatingDegreeFit() }
	site.heatingDegreeDaily = site.heatingDegreeDailyAnalysis
	profile, err := metrics.HomeForecast(start, start.Add(48*time.Hour))
	require.NoError(t, err)
	kwh := make([]float64, len(profile.Rates))
	for i, r := range profile.Rates {
		kwh[i] = r.Base / 1e3
	}
	site.heatingDegreeVariants(heatingDegreeAuto, kwh)
	st := site.HeatingDegree()
	res, err := json.MarshalIndent(st, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(out, res, 0o600))
	t.Logf("status: open %v (%s), %d days, %d samples", st.Open, st.Reason, st.CompleteDays, st.Samples)
}

func ptr(v *float64, prec int) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%.*f", prec, *v)
}
