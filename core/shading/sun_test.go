package shading

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Reference values: NREL SPA (pvlib 0.13 solarposition, method nrel_numpy,
// geometric "elevation" column), an independent implementation.
func TestSunPosition(t *testing.T) {
	for _, tc := range []struct {
		ts      string
		loc     Location
		az, el  float64
		comment string
	}{
		{"2026-10-05T13:15:00+02:00", Location{49.2115, 9.1949}, 181.0346, 35.9457, "ADR site, CEST"},
		{"2026-06-21T04:00:00Z", Location{49.2115, 9.1949}, 59.1144, 4.8162, "summer morning"},
		{"2026-12-21T15:00:00Z", Location{49.2115, 9.1949}, 228.5246, 2.6067, "winter afternoon"},
		{"2026-03-20T18:30:00Z", Location{49.2115, 9.1949}, 281.3957, -9.5969, "below horizon"},
		{"2026-06-21T04:00:00Z", Location{-33.87, 151.21}, 328.6174, 25.6470, "southern hemisphere"},
		{"2026-10-05T11:15:00Z", Location{64.15, -21.94}, 148.2111, 17.4114, "high latitude, west"},
	} {
		ts, err := time.Parse(time.RFC3339, tc.ts)
		assert.NoError(t, err)
		az, el := SunPosition(ts, tc.loc)
		assert.InDelta(t, tc.az, az, 0.1, "%s azimuth", tc.comment)
		assert.InDelta(t, tc.el, el, 0.1, "%s elevation", tc.comment)
	}
}

func TestLocationValid(t *testing.T) {
	assert.True(t, Location{49.2, 9.2}.Valid())
	assert.False(t, Location{91, 0}.Valid())
	assert.False(t, Location{0, 181}.Valid())
}
