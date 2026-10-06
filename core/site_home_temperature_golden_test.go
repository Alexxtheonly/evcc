package core

import (
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// Real-shaped golden input: the house's measured load (kWh per slot) and the recorded
// outdoor temperature (°C) for 2026-10-05, 00:00-23:45 local, from the evcc database.
const (
	goldenProfile = "0.136,0.1666,0.166,0.1535,0.2104,0.6308,0.5976,0.1602,0.1623,0.1576,0.1661,0.1518,0.1415,0.1497,0.1593,0.1447,0.1539,0.155,0.162,0.154,0.1615,0.1727,0.1576,0.1541,0.1343,0.142,0.1553,0.3789,0.8519,0.4401,0.1966,0.21,0.2064,0.1414,0.1473,0.1658,0.1678,0.2091,0.2007,0.2027,0.1897,0.2134,0.2121,0.1966,0.2061,0.2048,0.1982,0.1863,0.2184,0.3442,0.6526,0.3283,0.7106,0.6715,0.3176,0.2784,0.2833,0.2194,0.1811,0.2285,0.2128,0.1856,0.1964,0.1759,0.1403,0.1451,0.16,0.1561,0.1731,0.1764,0.1743,0.1678,0.1875,0.139,0.1649,0.4811,0.5076,0.7857,0.5141,0.5086,0.7164,0.492,0.3332,0.2535,0.168,0.1238,0.1389,0.2005,0.1405,0.1303,0.1252,0.1238,0.1282,0.1317,0.1026,0.1053"
	goldenTemps   = "13.1,12.9,13.6,13.4,13.3,13.2,12.6,12.5,12.3,12.2,12.1,11.9,11.7,11.4,11.4,11.2,11.0,10.9,10.9,10.8,10.7,10.5,10.4,10.3,10.1,10.0,9.9,9.8,9.5,9.6,9.7,9.8,9.6,10.0,10.4,10.9,11.1,11.7,12.3,13.0,13.8,14.5,15.2,15.8,16.6,17.1,17.6,18.2,19.2,19.6,20.0,20.4,20.9,21.1,21.4,21.6,22.0,22.1,22.3,22.4,22.4,22.4,22.4,22.4,22.6,22.4,22.1,21.9,22.1,21.1,20.5,19.7,18.9,18.3,17.3,16.4,15.5,14.7,13.8,13.4,12.9,12.3,11.8,11.5,11.2,10.8,10.6,10.3,10.1,9.8,9.5,9.3,9.2,8.9,8.7,8.5"
)

func goldenFloats(t *testing.T, s string) []float64 {
	t.Helper()
	var res []float64
	for _, v := range strings.Split(s, ",") {
		f, err := strconv.ParseFloat(v, 64)
		require.NoError(t, err)
		res = append(res, f)
	}
	return res
}

// goldenRates repeats the recorded day twice (a 48 h horizon), shifted by offset K, starting
// at the current slot as applyHeatingDegree expects.
func goldenRates(t *testing.T, offset float64) api.Rates {
	temps := goldenFloats(t, goldenTemps)
	start := time.Now().Truncate(tariff.SlotDuration)
	var res api.Rates
	for i := range 2 * len(temps) {
		s := start.Add(time.Duration(i) * tariff.SlotDuration)
		res = append(res, api.Rate{Start: s, End: s.Add(tariff.SlotDuration), Value: temps[i%len(temps)] + offset})
	}
	return res
}

func goldenBits(v []float64) []uint64 {
	res := make([]uint64, len(v))
	for i, x := range v {
		res[i] = math.Float64bits(x)
	}
	return res
}

// goldenCases are the fits the golden file was recorded with: a February-like fit on the
// recorded day, the same 14 K colder, and a steep fit that drives the per-slot clamp.
var goldenCases = []struct {
	name   string
	offset float64
	fit    heatingDegreeFit
}{
	{"mild", 0, heatingDegreeFit{slope: 40, meanHDD: 6}},
	{"cold", -14, heatingDegreeFit{slope: 40, meanHDD: 6}},
	{"steep", -14, heatingDegreeFit{slope: 400, meanHDD: 2}},
}

// readGolden returns the recorded outputs by case name. testdata/heating_degree_golden.txt
// was written by applyHeatingDegree before the heatingDegree setting existed (b13afa707),
// as the IEEE-754 bits of each slot, so a comparison is exact.
func readGolden(t *testing.T) map[string][]uint64 {
	t.Helper()
	b, err := os.ReadFile("testdata/heating_degree_golden.txt")
	require.NoError(t, err)
	res := make(map[string][]uint64)
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		name, list, ok := strings.Cut(line, " ")
		require.True(t, ok)
		for _, h := range strings.Split(list, ",") {
			v, err := strconv.ParseUint(h, 16, 64)
			require.NoError(t, err)
			res[name] = append(res[name], v)
		}
	}
	return res
}

// TestHeatingDegreeAutoGolden pins the default: with heatingDegree unset or "auto", the
// corrected profile is bit-for-bit what the code produced before the setting existed.
func TestHeatingDegreeAutoGolden(t *testing.T) {
	golden := readGolden(t)
	profile := goldenFloats(t, goldenProfile)
	profile = append(profile, profile...)
	ctrl := gomock.NewController(t)

	for _, mode := range []string{"", heatingDegreeAuto} {
		for _, c := range goldenCases {
			temp := api.NewMockTariff(ctrl)
			temp.EXPECT().Type().Return(api.TariffTypeTemperature).AnyTimes()
			temp.EXPECT().Rates().Return(goldenRates(t, c.offset), nil).AnyTimes()
			site := &Site{log: util.NewLogger("foo"), tariffs: &tariff.Tariffs{Temperature: temp}}
			fit := c.fit
			site.heatingDegreeCached = func() (heatingDegreeResult, error) { return heatingDegreeResult{fit: fit, ok: true}, nil }
			site.heatingDegreeDaily = func() (*heatingDegreeAnalysis, error) {
				t.Fatal("auto must not consult the daily gate")
				return nil, nil
			}

			got, _ := site.heatingDegreeProfile(mode, profile)
			require.Equal(t, golden[c.name], goldenBits(got), "mode %q case %s", mode, c.name)
			require.NotEqual(t, goldenBits(profile), goldenBits(got), "the golden cases do correct")
		}
	}
}
