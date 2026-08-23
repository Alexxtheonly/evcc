package settings

import (
	"testing"

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

	SetString("foo", "bar")

	rows := history(t, "foo")
	require.Len(t, rows, 1)
	// absence, not a sentinel: a brand new key has no prior value, recorded as
	// SQL NULL rather than "" - an empty string is itself a valid setting value
	assert.Nil(t, rows[0].Old)
	assert.Equal(t, "bar", rows[0].New)
}

func TestSettingsHistoryChange(t *testing.T) {
	setupHistoryTest(t)

	SetString("foo", "bar")
	SetString("foo", "baz")

	rows := history(t, "foo")
	require.Len(t, rows, 2)

	require.NotNil(t, rows[1].Old)
	assert.Equal(t, "bar", *rows[1].Old)
	assert.Equal(t, "baz", rows[1].New)
}

func TestSettingsHistoryNoChangeIsNoOp(t *testing.T) {
	setupHistoryTest(t)

	SetString("foo", "bar")
	SetString("foo", "bar") // repeat write of the same value
	SetString("foo", "bar")

	// a replay must not see three configuration changes when the value never
	// actually moved - the setter's own dedup must suppress the history row
	rows := history(t, "foo")
	require.Len(t, rows, 1)
}

func TestSettingsHistoryWithoutDatabase(t *testing.T) {
	// no serverdb.Instance set up: SetString/String must still work purely
	// in-memory, exactly as before this table existed
	mu.Lock()
	settings = nil
	mu.Unlock()

	assert.NotPanics(t, func() { SetString("foo", "bar") })

	v, err := String("foo")
	require.NoError(t, err)
	assert.Equal(t, "bar", v)
}

// TestSettingsHistoryDelete asserts F1's second half: deleting a key must
// leave a trace, or a replay has no way to tell "still at its last value"
// apart from "removed at time T" - both look identical from the surviving
// write rows alone.
func TestSettingsHistoryDelete(t *testing.T) {
	setupHistoryTest(t)

	SetString("foo", "bar")
	require.NoError(t, Delete("foo"))

	rows := history(t, "foo")
	require.Len(t, rows, 2)

	assert.True(t, rows[1].Deleted)
	require.NotNil(t, rows[1].Old)
	assert.Equal(t, "bar", *rows[1].Old)

	// deleting a key that was never set is a no-op, not a fabricated removal
	require.NoError(t, Delete("never-set"))
	assert.Empty(t, history(t, "never-set"))
}

func TestSettingsHistoryIndependentKeys(t *testing.T) {
	setupHistoryTest(t)

	SetString("foo", "1")
	SetString("bar", "2")
	SetString("foo", "3")

	assert.Len(t, history(t, "foo"), 2)
	assert.Len(t, history(t, "bar"), 1)
}
