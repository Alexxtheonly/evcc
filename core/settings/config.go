package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	dbsettings "github.com/evcc-io/evcc/server/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/spf13/cast"
)

var _ Settings = (*ConfigSettings)(nil)

type ConfigSettings struct {
	mu   sync.Mutex
	log  *util.Logger
	conf *config.Config
}

func NewConfigSettingsAdapter(log *util.Logger, conf *config.Config) *ConfigSettings {
	return &ConfigSettings{log: log, conf: conf}
}

func (s *ConfigSettings) get(key string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	val := s.conf.Named().Other[key]
	if val == nil {
		return nil, errors.New("not found")
	}
	return val, nil
}

// TODO remove broken error handling when settings api is retired
func (s *ConfigSettings) set(key string, val any) {
	s.mu.Lock()

	data := s.conf.Named().Other
	oldVal, existed := data[key]
	data[key] = val
	err := s.conf.Update(data)
	if err != nil {
		s.log.ERROR.Println(err)
	}

	s.mu.Unlock()

	// audit trail for the settings ledger (ADR-011 / F1): this adapter is
	// handed to every database-configured loadpoint (cmd/setup.go) and
	// persists through the configs table (conf.Update above) instead of
	// server/db/settings.SetString, so without this hook every such
	// loadpoint's mode/limitSoc/minSoc/planTime/... changes were entirely
	// missing from settings_history - a failed write must not be recorded
	// as if it had happened.
	if err != nil {
		return
	}
	if newStr := fmt.Sprint(val); !existed {
		dbsettings.RecordHistory(s.historyKey(key), nil, newStr)
	} else if oldStr := fmt.Sprint(oldVal); oldStr != newStr {
		dbsettings.RecordHistory(s.historyKey(key), &oldStr, newStr)
	}
}

// historyKey namespaces a settings_history key by the owning config record's
// id (the same "db:<id>" identity util/config.NameForID already uses
// elsewhere), so two database-configured loadpoints writing e.g. "mode"
// don't conflate their history under one key - settings_history has no
// column of its own for which device a key belongs to.
func (s *ConfigSettings) historyKey(key string) string {
	return config.NameForID(s.conf.ID) + "." + key
}

func (s *ConfigSettings) SetString(key string, val string) {
	s.set(key, val)
}

func (s *ConfigSettings) SetInt(key string, val int64) {
	s.set(key, val)
}

func (s *ConfigSettings) SetFloat(key string, val float64) {
	s.set(key, val)
}

func (s *ConfigSettings) SetFloatPtr(key string, val *float64) {
	s.set(key, val)
}

func (s *ConfigSettings) SetTime(key string, val time.Time) {
	s.set(key, val)
}

func (s *ConfigSettings) SetBool(key string, val bool) {
	s.set(key, val)
}

func (s *ConfigSettings) SetJson(key string, val any) error {
	s.set(key, val)
	return nil
}

func (s *ConfigSettings) String(key string) (string, error) {
	val, err := s.get(key)
	if err != nil {
		return "", err
	}
	return cast.ToStringE(val)
}

func (s *ConfigSettings) Int(key string) (int64, error) {
	val, err := s.get(key)
	if err != nil {
		return 0, err
	}
	return cast.ToInt64E(val)
}

func (s *ConfigSettings) Float(key string) (float64, error) {
	val, err := s.get(key)
	if err != nil {
		return 0, err
	}
	return cast.ToFloat64E(val)
}

func (s *ConfigSettings) Time(key string) (time.Time, error) {
	val, err := s.get(key)
	if err != nil {
		return time.Time{}, err
	}
	return cast.ToTimeE(val)
}

func (s *ConfigSettings) Bool(key string) (bool, error) {
	val, err := s.get(key)
	if err != nil {
		return false, err
	}
	return cast.ToBoolE(val)
}

func (s *ConfigSettings) Json(key string, res any) error {
	str, err := s.String(key)
	if str == "" || err != nil {
		return err
	}
	return json.Unmarshal([]byte(str), &res)
}
