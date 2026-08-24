package settings

import (
	"testing"
	"time"

	serverdb "github.com/evcc-io/evcc/server/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupHistoryTest gives each test its own in-memory database and resets the
// package-level in-memory settings cache, so tests don't see each other's keys.
func setupHistoryTest(t *testing.T) {
	t.Helper()

	instance, err := serverdb.New("sqlite", ":memory:")
	require.NoError(t, err)
	require.NoError(t, instance.AutoMigrate(new(setting), new(settingHistory)))
	serverdb.Instance = instance

	mu.Lock()
	settings = nil
	mu.Unlock()

	t.Cleanup(func() {
		serverdb.Instance = nil
	})
}

func history(t *testing.T, key string) []settingHistory {
	t.Helper()

	var rows []settingHistory
	require.NoError(t, serverdb.Instance.Where("key = ?", key).Order("id").Find(&rows).Error)
	return rows
}

func TestSettingsHistoryFirstWrite(t *testing.T) {
	setupHistoryTest(t)

	SetString("prioritySoc", "bar")

	rows := history(t, "prioritySoc")
	require.Len(t, rows, 1)
	// absence, not a sentinel: a brand new key has no prior value, recorded as
	// SQL NULL rather than "" - an empty string is itself a valid setting value
	assert.Nil(t, rows[0].Old)
	assert.Equal(t, "bar", rows[0].New)
}

func TestSettingsHistoryChange(t *testing.T) {
	setupHistoryTest(t)

	SetString("prioritySoc", "bar")
	SetString("prioritySoc", "baz")

	rows := history(t, "prioritySoc")
	require.Len(t, rows, 2)

	require.NotNil(t, rows[1].Old)
	assert.Equal(t, "bar", *rows[1].Old)
	assert.Equal(t, "baz", rows[1].New)
}

func TestSettingsHistoryNoChangeIsNoOp(t *testing.T) {
	setupHistoryTest(t)

	SetString("prioritySoc", "bar")
	SetString("prioritySoc", "bar") // repeat write of the same value
	SetString("prioritySoc", "bar")

	// a replay must not see three configuration changes when the value never
	// actually moved - the setter's own dedup must suppress the history row
	rows := history(t, "prioritySoc")
	require.Len(t, rows, 1)
}

func TestSettingsHistoryWithoutDatabase(t *testing.T) {
	// no serverdb.Instance set up: SetString/String must still work purely
	// in-memory, exactly as before this table existed
	mu.Lock()
	settings = nil
	mu.Unlock()

	assert.NotPanics(t, func() { SetString("prioritySoc", "bar") })

	v, err := String("prioritySoc")
	require.NoError(t, err)
	assert.Equal(t, "bar", v)
}

// TestSettingsHistoryDelete asserts F1's second half: deleting a key must
// leave a trace, or a replay has no way to tell "still at its last value"
// apart from "removed at time T" - both look identical from the surviving
// write rows alone.
func TestSettingsHistoryDelete(t *testing.T) {
	setupHistoryTest(t)

	SetString("prioritySoc", "bar")
	require.NoError(t, Delete("prioritySoc"))

	rows := history(t, "prioritySoc")
	require.Len(t, rows, 2)

	assert.True(t, rows[1].Deleted)
	require.NotNil(t, rows[1].Old)
	assert.Equal(t, "bar", *rows[1].Old)

	// deleting a key that was never set is a no-op, not a fabricated removal
	require.NoError(t, Delete("residualPower"))
	assert.Empty(t, history(t, "residualPower"))
}

// TestDeleteHistory covers F9: a manual-delete endpoint for settings_history,
// matching the existing energy/tariffs ones.
func TestDeleteHistory(t *testing.T) {
	setupHistoryTest(t)

	SetString("prioritySoc", "1")
	SetString("prioritySoc", "2")
	SetString("prioritySoc", "3")
	rows := history(t, "prioritySoc")
	require.Len(t, rows, 3)

	// give the three rows deterministic, distinct timestamps - persistHistory
	// stamps real time, which a fast test run could otherwise collapse into
	// the same wall-clock second
	base := time.Date(2026, 4, 15, 16, 0, 0, 0, time.UTC)
	for i, r := range rows {
		require.NoError(t, serverdb.Instance.Table("settings_history").
			Where("id = ?", r.ID).
			Update("ts", base.Add(time.Duration(i)*time.Minute).Unix()).Error)
	}

	// both bounds are required
	_, err := DeleteHistory(time.Time{}, base)
	require.Error(t, err)

	// half-open range covers the first two rows, not the third
	deleted, err := DeleteHistory(base, base.Add(90*time.Second))
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)
	assert.Len(t, history(t, "prioritySoc"), 1)
}

func TestSettingsHistoryIndependentKeys(t *testing.T) {
	setupHistoryTest(t)

	SetString("prioritySoc", "1")
	SetString("bufferSoc", "2")
	SetString("prioritySoc", "3")

	assert.Len(t, history(t, "prioritySoc"), 2)
	assert.Len(t, history(t, "bufferSoc"), 1)
}

// TestSettingsHistoryExcludesCredentials is the regression test for a leak
// found in production: settings_history had accumulated 14 rows, ~36 KB, of
// full OAuth access and refresh tokens in cleartext under a plugin/auth
// subject key, appended roughly hourly as the token refreshed, into a table
// that is append-only and never pruned. The live settings table holds only
// the current token; the history table was building a permanent archive of
// every credential the process had ever held.
func TestSettingsHistoryExcludesCredentials(t *testing.T) {
	setupHistoryTest(t)

	// a plugin/auth OAuth subject ("<clientID>-<hash>", plugin/auth/oauth.go)
	// - the key that actually leaked, and one no denylist could have named
	SetString("41c41da0aab6f052df12164523bb1b80-5bac2d30", `{"access_token":"ey.secret","refresh_token":"ey.also-secret"}`)
	// and the statically named secrets sharing the same key space
	SetString("sponsorToken", "ey.sponsor")
	SetString("eebus", `{"ski":"abc","privateKey":"-----BEGIN EC PRIVATE KEY-----"}`)
	SetString("jwtSecretKey", "s3cret")
	SetString("adminPassword", "$2a$10$hash")
	SetString("apiKey", "$2a$10$hash")

	// deleting a credential must not archive its old value either - the
	// OAuth handler calls Delete on an invalidated token
	require.NoError(t, Delete("41c41da0aab6f052df12164523bb1b80-5bac2d30"))

	var n int64
	require.NoError(t, serverdb.Instance.Table("settings_history").Count(&n).Error)
	assert.Zero(t, n, "no credential-bearing key may reach settings_history")

	// the settings themselves are still stored, unchanged - only the history
	// of them is withheld
	v, err := String("sponsorToken")
	require.NoError(t, err)
	assert.Equal(t, "ey.sponsor", v)
}

// TestSettingsHistoryRecordsNamespacedDeviceKeys asserts the allowlist match
// ignores the device namespace dbSettings/ConfigSettings prepend, so a
// loadpoint's or vehicle's configuration is still recorded.
func TestSettingsHistoryRecordsNamespacedDeviceKeys(t *testing.T) {
	setupHistoryTest(t)

	SetString("lp1.mode", "pv")
	SetString("vehicle.luna.minSoc", "20")
	RecordHistory("db:3.smartCostLimit", nil, "0.15")

	assert.Len(t, history(t, "lp1.mode"), 1)
	assert.Len(t, history(t, "vehicle.luna.minSoc"), 1)
	assert.Len(t, history(t, "db:3.smartCostLimit"), 1)

	// but a namespace alone does not make a key recordable
	SetString("vehicle.luna.accessToken", "ey.secret")
	assert.Empty(t, history(t, "vehicle.luna.accessToken"))
}
