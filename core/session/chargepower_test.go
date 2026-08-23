package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// powerSession builds a session with a given soc range, duration and average power (W),
// derived into the ChargedEnergy/ChargeDuration fields PriorChargeTaper actually reads.
func powerSession(socStart, socEnd float64, duration time.Duration, avgPowerW float64) Session {
	energyKWh := avgPowerW * duration.Hours() / 1e3
	return Session{
		SocStart:       &socStart,
		SocEnd:         &socEnd,
		ChargedEnergy:  energyKWh,
		ChargeDuration: &duration,
	}
}

func TestPriorChargeTaper(t *testing.T) {
	t.Run("below the minimum session count in either bucket: no prior", func(t *testing.T) {
		_, _, ok := PriorChargeTaper(Sessions{
			powerSession(10, 40, time.Hour, 11000), // plateau
			powerSession(10, 40, time.Hour, 11000), // plateau
			powerSession(60, 100, 2*time.Hour, 1200),
		})
		assert.False(t, ok, "tail bucket only has 1 sample")
	})

	t.Run("enough sessions in both buckets: median of each", func(t *testing.T) {
		sessions := Sessions{
			powerSession(10, 40, time.Hour, 11000),
			powerSession(5, 35, time.Hour, 11200),
			powerSession(15, 45, time.Hour, 10800),
			powerSession(60, 100, 2*time.Hour, 1200),
			powerSession(55, 95, 2*time.Hour, 1300),
			powerSession(70, 100, time.Hour, 1100),
		}

		minPower, maxPower, ok := PriorChargeTaper(sessions)
		assert.True(t, ok)
		assert.InDelta(t, 1200.0, minPower, 1e-6)
		assert.InDelta(t, 11000.0, maxPower, 1e-6)
	})

	t.Run("sessions spanning the knee count toward neither bucket", func(t *testing.T) {
		sessions := Sessions{
			powerSession(10, 40, time.Hour, 11000),
			powerSession(5, 35, time.Hour, 11200),
			powerSession(15, 45, time.Hour, 10800),
			powerSession(60, 100, 2*time.Hour, 1200),
			powerSession(55, 95, 2*time.Hour, 1300),
			powerSession(70, 100, time.Hour, 1100),
			// spans the 50% knee both ways - a blended average that would bias
			// whichever bucket it landed in
			powerSession(30, 90, 3*time.Hour, 6000),
		}

		minPower, maxPower, ok := PriorChargeTaper(sessions)
		assert.True(t, ok)
		assert.InDelta(t, 1200.0, minPower, 1e-6, "spanning session must not shift the tail bucket")
		assert.InDelta(t, 11000.0, maxPower, 1e-6, "spanning session must not shift the plateau bucket")
	})

	t.Run("short-swing sessions are excluded from both buckets", func(t *testing.T) {
		sessions := Sessions{
			powerSession(10, 40, time.Hour, 11000),
			powerSession(5, 35, time.Hour, 11200),
			powerSession(15, 45, time.Hour, 10800),
			powerSession(60, 100, 2*time.Hour, 1200),
			powerSession(55, 95, 2*time.Hour, 1300),
			powerSession(70, 100, time.Hour, 1100),
			powerSession(48, 49, time.Minute, 50000), // 1% swing: excluded regardless of power
		}

		minPower, maxPower, ok := PriorChargeTaper(sessions)
		assert.True(t, ok)
		assert.InDelta(t, 1200.0, minPower, 1e-6)
		assert.InDelta(t, 11000.0, maxPower, 1e-6)
	})

	t.Run("missing fields disqualify a session", func(t *testing.T) {
		zeroDuration := time.Duration(0)
		sessions := Sessions{
			powerSession(10, 40, time.Hour, 11000),
			powerSession(5, 35, time.Hour, 11200),
			powerSession(15, 45, time.Hour, 10800),
			powerSession(60, 100, 2*time.Hour, 1200),
			powerSession(55, 95, 2*time.Hour, 1300),
			powerSession(70, 100, time.Hour, 1100),
			{SocStart: ptr(10.0), SocEnd: nil, ChargedEnergy: 5, ChargeDuration: ptr(time.Hour)},
			{SocStart: ptr(10.0), SocEnd: ptr(40.0), ChargedEnergy: 0, ChargeDuration: ptr(time.Hour)},
			{SocStart: ptr(10.0), SocEnd: ptr(40.0), ChargedEnergy: 5, ChargeDuration: &zeroDuration},
		}

		minPower, maxPower, ok := PriorChargeTaper(sessions)
		assert.True(t, ok)
		assert.InDelta(t, 1200.0, minPower, 1e-6)
		assert.InDelta(t, 11000.0, maxPower, 1e-6)
	})
}
