package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func chargeSession(socStart, socEnd, chargedKWh float64) Session {
	return Session{SocStart: &socStart, SocEnd: &socEnd, ChargedEnergy: chargedKWh}
}

func TestPriorSocGradient(t *testing.T) {
	// below the minimum session count: no prior
	_, ok := PriorSocGradient(Sessions{
		chargeSession(20, 50, 9), // 300 Wh/%
		chargeSession(30, 60, 9), // 300 Wh/%
	})
	assert.False(t, ok)

	// three qualifying sessions: median gradient
	sessions := Sessions{
		chargeSession(20, 50, 9),  // 30% swing, 9kWh -> 300 Wh/%
		chargeSession(30, 60, 9),  // 30% swing, 9kWh -> 300 Wh/%
		chargeSession(10, 80, 21), // 70% swing, 21kWh -> 300 Wh/%
	}
	got, ok := PriorSocGradient(sessions)
	assert.True(t, ok)
	assert.InDelta(t, 300.0, got, 1e-9)

	// sessions with too small a soc swing are excluded
	sessions = append(sessions, chargeSession(50, 55, 100)) // 5% swing, huge energy - would skew hard
	got, ok = PriorSocGradient(sessions)
	assert.True(t, ok)
	assert.InDelta(t, 300.0, got, 1e-9, "small-swing session must not be counted")

	// a single wildly different session moves the median only slightly, not fully
	sessions = Sessions{
		chargeSession(20, 50, 9),   // 300 Wh/%
		chargeSession(30, 60, 9),   // 300 Wh/%
		chargeSession(10, 60, 15),  // 300 Wh/%
		chargeSession(10, 90, 800), // 80% swing, huge energy: 10000 Wh/% outlier
	}
	got, ok = PriorSocGradient(sessions)
	assert.True(t, ok)
	assert.Less(t, got, 1000.0, "one outlier session must not dominate the median")
}

func TestPriorSocGradientIgnoresIncompleteSessions(t *testing.T) {
	sessions := Sessions{
		chargeSession(20, 50, 9),
		chargeSession(30, 60, 9),
		{SocStart: nil, SocEnd: ptr(50.0), ChargedEnergy: 9},       // missing start
		{SocStart: ptr(20.0), SocEnd: nil, ChargedEnergy: 9},       // missing end
		{SocStart: ptr(20.0), SocEnd: ptr(50.0), ChargedEnergy: 0}, // no energy
	}

	_, ok := PriorSocGradient(sessions)
	assert.False(t, ok, "only 2 qualifying sessions, below the minimum of 3")
}

func ptr[T any](v T) *T { return &v }
