package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpectedArrivalNeedsEvidenceAndRejectsFutureHistory(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	require.Nil(t, ExpectedArrival(nil, now, now.Add(36*time.Hour)))
	var sessions Sessions
	for d := 30; d >= 0; d-- {
		at := now.AddDate(0, 0, -d)
		start := time.Date(at.Year(), at.Month(), at.Day(), 18, 0, 0, 0, at.Location())
		end := start.Add(13*time.Hour + time.Duration(d)*time.Second)
		low, high, odo := 35.0, 70.0, float64(1000+30-d)*20
		sessions = append(sessions, Session{Created: start, Disconnected: &end, SocStart: &low, SocEnd: &high, Odometer: &odo})
	}
	got := ExpectedArrival(sessions, now, now.Add(36*time.Hour))
	require.NotNil(t, got)
	assert.Equal(t, 18, got.Time.Hour())
	assert.Equal(t, 35.0, got.Soc)
	assert.GreaterOrEqual(t, got.Samples, 4)
	assert.True(t, got.Time.After(now))
}
