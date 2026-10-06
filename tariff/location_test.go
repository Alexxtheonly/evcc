package tariff

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLocationFromConfig(t *testing.T) {
	for _, tc := range []struct {
		other map[string]any
		want  Location
		ok    bool
	}{
		{map[string]any{"template": "forecast-solar", "lat": "49.1579", "lon": "9.1737"}, Location{49.1579, 9.1737}, true},
		{map[string]any{"Lat": 49.1, "LON": 9}, Location{49.1, 9}, true},
		{map[string]any{"lat": "49.1"}, Location{}, false},
		{map[string]any{"lat": "north", "lon": "9"}, Location{}, false},
		{map[string]any{"lat": 91, "lon": 9}, Location{}, false},
		{map[string]any{"lat": 0, "lon": 0}, Location{}, false},
		{nil, Location{}, false},
	} {
		got, ok := LocationFromConfig(tc.other)
		assert.Equal(t, tc.ok, ok, "%v", tc.other)
		assert.Equal(t, tc.want, got, "%v", tc.other)
	}
}

func TestSolarLocation(t *testing.T) {
	a, b, far := &Location{49.15, 9.17}, &Location{49.16, 9.18}, &Location{48, 9.17}
	for _, tc := range []struct {
		locs []*Location
		want Location
		err  string
	}{
		{nil, Location{}, "no solar forecast configured"},
		{[]*Location{a}, *a, ""},
		{[]*Location{a, b}, *a, ""},
		{[]*Location{a, nil}, Location{}, "solar forecast has no coordinates"},
		{[]*Location{a, far}, Location{}, "solar forecasts disagree on coordinates"},
	} {
		got, err := (&Tariffs{SolarLocations: tc.locs}).SolarLocation()
		if tc.err != "" {
			assert.EqualError(t, err, tc.err)
			continue
		}
		assert.NoError(t, err)
		assert.Equal(t, tc.want, got)
	}
	_, err := (*Tariffs)(nil).SolarLocation()
	assert.Error(t, err)
}
