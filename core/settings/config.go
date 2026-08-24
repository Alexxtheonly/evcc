package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
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

	// audit trail for the settings ledger: this adapter is handed to every
	// database-configured loadpoint (cmd/setup.go) and persists through the
	// configs table (conf.Update above) instead of
	// server/db/settings.SetString, so without this hook every such
	// loadpoint's mode/limitSoc/minSoc/planTime/... changes are missing from
	// settings_history - and a failed write must not be recorded as if it had
	// happened.
	if err != nil {
		return
	}

	newStr := render(val)
	if !existed {
		dbsettings.RecordHistory(s.historyKey(key), nil, newStr)
		return
	}

	// oldVal and val are frequently not the same Go representation of the
	// same data: after a restart, conf.Data holds whatever gorm's
	// serializer:json last decoded (plain maps/slices/float64s), while a
	// fresh Set* call passes a typed Go struct. Comparing their rendered
	// strings treats every such re-serialization as a change, even when
	// nothing moved, and floods the history with rows after every restart.
	// Round-tripping both sides through JSON first normalizes them to the
	// same shape, so the comparison reflects the actual data rather than its
	// current Go type.
	if valuesEqual(oldVal, val) {
		return
	}

	oldStr := render(oldVal)
	dbsettings.RecordHistory(s.historyKey(key), &oldStr, newStr)
}

// valuesEqual reports whether a and b represent the same data once both are
// normalized to the same shape via a JSON round trip - see the comment in
// set(). Marshaling failure is treated as "not equal" so a value we can't
// reason about is never silently dropped from the audit trail.
func valuesEqual(a, b any) bool {
	ca, errA := canonicalize(a)
	if errA != nil {
		return false
	}
	cb, errB := canonicalize(b)
	if errB != nil {
		return false
	}
	return reflect.DeepEqual(ca, cb)
}

func canonicalize(val any) (any, error) {
	b, err := json.Marshal(val)
	if err != nil {
		return nil, err
	}
	var res any
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, err
	}
	return res, nil
}

// render renders val the way fmt's default %v verb would, except that
// pointers - at any depth, not just the top level - are dereferenced instead
// of printed as their memory address. A *float64 field (SmartCostLimit, the
// Estimate field nested inside SocConfig, ...) would otherwise render as
// something like "0x2f1b02b637d8": meaningless once the process restarts and
// unrecoverable for a settings_history replay. A nil pointer still renders as
// "<nil>", same as fmt - that is a real, distinct value and must not be
// confused with the SQL NULL the Old column uses for "no previous value".
func render(val any) string {
	return renderValue(reflect.ValueOf(val))
}

func renderValue(v reflect.Value) string {
	if !v.IsValid() {
		return "<nil>"
	}

	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return "<nil>"
		}
		return renderValue(v.Elem())
	}

	// respect an existing Stringer/error before descending further - time.Time,
	// time.Duration and enum types like PollMode must keep rendering the way
	// they always did
	if v.CanInterface() {
		switch iv := v.Interface().(type) {
		case fmt.Stringer:
			return iv.String()
		case error:
			return iv.Error()
		}
	}

	switch v.Kind() {
	case reflect.Struct:
		parts := make([]string, 0, v.NumField())
		for i := range v.NumField() {
			f := v.Field(i)
			if !f.CanInterface() {
				// unexported field: none of the settings payloads have these;
				// render a placeholder rather than panicking on Interface()
				parts = append(parts, "?")
				continue
			}
			parts = append(parts, renderValue(f))
		}
		return "{" + strings.Join(parts, " ") + "}"

	case reflect.Slice, reflect.Array:
		parts := make([]string, v.Len())
		for i := range parts {
			parts[i] = renderValue(v.Index(i))
		}
		return "[" + strings.Join(parts, " ") + "]"

	case reflect.Map:
		keys := v.MapKeys()
		rendered := make([]string, len(keys))
		for i, k := range keys {
			rendered[i] = fmt.Sprint(k.Interface()) + ":" + renderValue(v.MapIndex(k))
		}
		slices.Sort(rendered)
		return "map[" + strings.Join(rendered, " ") + "]"

	default:
		return fmt.Sprint(v.Interface())
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
