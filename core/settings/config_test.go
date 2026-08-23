package settings

import (
	"strconv"
	"testing"

	serverdb "github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/evcc-io/evcc/util/templates"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// historyRow reads back a settings_history row via raw SQL, since the
// underlying struct is unexported inside server/db/settings.
type historyRow struct {
	Old *string `gorm:"column:old"`
	New string  `gorm:"column:new"`
}

func historyFor(t *testing.T, key string) []historyRow {
	t.Helper()

	var rows []historyRow
	require.NoError(t, serverdb.Instance.Table("settings_history").Where("key = ?", key).Order("id").Find(&rows).Error)
	return rows
}

// TestConfigSettingsHistory covers F1: a database-configured loadpoint (the
// ConfigSettings adapter, handed out by cmd/setup.go for every loadpoint
// configured via the UI/API rather than evcc.yaml) must produce the same
// settings_history rows a YAML-configured loadpoint gets through
// server/db/settings.SetString - before this, it produced none at all.
func TestConfigSettingsHistory(t *testing.T) {
	require.NoError(t, serverdb.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { serverdb.Instance = nil })

	conf, err := config.AddConfig(templates.Loadpoint, map[string]any{})
	require.NoError(t, err)

	key := "db:" + strconv.Itoa(conf.ID) + ".mode"

	s := NewConfigSettingsAdapter(util.NewLogger("foo"), &conf)

	// first write: no prior value, Old is nil - not ""
	s.SetString("mode", "pv")
	rows := historyFor(t, key)
	require.Len(t, rows, 1)
	assert.Nil(t, rows[0].Old)
	assert.Equal(t, "pv", rows[0].New)

	// repeat write of the same value: no new row
	s.SetString("mode", "pv")
	assert.Len(t, historyFor(t, key), 1)

	// changed value: recorded, with the previous value as Old
	s.SetString("mode", "minpv")
	rows = historyFor(t, key)
	require.Len(t, rows, 2)
	require.NotNil(t, rows[1].Old)
	assert.Equal(t, "pv", *rows[1].Old)
	assert.Equal(t, "minpv", rows[1].New)
}

// TestConfigSettingsHistoryPerLoadpoint ensures two database-configured
// loadpoints writing the same key ("mode") don't conflate their history
// under one row - settings_history has no column of its own for which
// device a key belongs to, so the adapter must namespace the key itself.
func TestConfigSettingsHistoryPerLoadpoint(t *testing.T) {
	require.NoError(t, serverdb.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { serverdb.Instance = nil })

	conf1, err := config.AddConfig(templates.Loadpoint, map[string]any{})
	require.NoError(t, err)
	conf2, err := config.AddConfig(templates.Loadpoint, map[string]any{})
	require.NoError(t, err)

	s1 := NewConfigSettingsAdapter(util.NewLogger("foo"), &conf1)
	s2 := NewConfigSettingsAdapter(util.NewLogger("foo"), &conf2)

	s1.SetString("mode", "pv")
	s2.SetString("mode", "now")

	rows1 := historyFor(t, "db:"+strconv.Itoa(conf1.ID)+".mode")
	rows2 := historyFor(t, "db:"+strconv.Itoa(conf2.ID)+".mode")

	require.Len(t, rows1, 1)
	require.Len(t, rows2, 1)
	assert.Equal(t, "pv", rows1[0].New)
	assert.Equal(t, "now", rows2[0].New)
}

// TestConfigSettingsHistoryFailedWriteNotRecorded asserts a write that never
// reached the configs table (conf.Update failing) must not be recorded as if
// it had happened.
func TestConfigSettingsHistoryFailedWriteNotRecorded(t *testing.T) {
	require.NoError(t, serverdb.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { serverdb.Instance = nil })

	conf, err := config.AddConfig(templates.Loadpoint, map[string]any{})
	require.NoError(t, err)

	// remove the backing row so conf.Update's internal lookup fails
	require.NoError(t, conf.Delete())

	s := NewConfigSettingsAdapter(util.NewLogger("foo"), &conf)
	s.SetString("mode", "pv")

	assert.Empty(t, historyFor(t, "db:"+strconv.Itoa(conf.ID)+".mode"))
}
