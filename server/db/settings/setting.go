package settings

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/util"
	"github.com/samber/lo"
	"go.yaml.in/yaml/v4"
	"gorm.io/gorm"
)

var ErrNotFound = errors.New("not found")

var log = util.NewLogger("settings")

// setting is a settings entry
type setting struct {
	dirty bool
	Key   string `json:"key" gorm:"primarykey"`
	Value string `json:"value"`
}

// settingHistory is one recorded write to a settings key. Settings on this
// project have drifted silently more than once, unnoticed until much later;
// without this, any later reconstruction of "what was configured at time T"
// would have no choice but to substitute today's value for the whole past.
//
// Old is nil for a key's first-ever write - "the key did not exist yet" and
// "the key existed with an empty value" are different facts, and collapsing
// them into "" would erase exactly the distinction this table exists for.
type settingHistory struct {
	ID        uint    `gorm:"primarykey"`
	Timestamp int64   `json:"ts" gorm:"column:ts;index"`
	Key       string  `json:"key" gorm:"column:key;index"`
	Old       *string `json:"old" gorm:"column:old"`
	New       string  `json:"new" gorm:"column:new"`
}

func (settingHistory) TableName() string {
	return "settings_history"
}

var (
	mu       sync.RWMutex
	settings []setting
)

func init() {
	db.Register(func(db *gorm.DB) error {
		if err := db.AutoMigrate(new(setting), new(settingHistory)); err != nil {
			return err
		}

		return db.Find(&settings).Error
	})
}

// recordHistory appends a settings_history row for a value actually written by
// SetString. Called with mu already held, so the change to the in-memory
// settings slice and its history entry can never be observed out of order by
// a concurrent reader. A nil db.Instance (unit tests exercising SetString/
// String in isolation, without a database) is a silent no-op, matching the
// test guard used throughout this codebase for optional persistence.
func recordHistory(key string, old *string, val string) {
	if db.Instance == nil {
		return
	}

	if err := db.Instance.Create(&settingHistory{
		Timestamp: time.Now().Unix(),
		Key:       key,
		Old:       old,
		New:       val,
	}).Error; err != nil {
		log.ERROR.Printf("persist settings history: %v", err)
	}
}

func Persist() error {
	mu.Lock()
	defer mu.Unlock()

	if dirty := lo.FilterMap(settings, func(s setting, _ int) (*setting, bool) {
		return &s, s.dirty
	}); len(dirty) > 0 {
		if err := db.Instance.Save(dirty).Error; err != nil {
			return err
		}

		for _, s := range dirty {
			s.dirty = false
		}
	}

	return nil
}

func All() []setting {
	mu.RLock()
	defer mu.RUnlock()

	res := slices.SortedFunc(slices.Values(settings), func(i, j setting) int {
		return cmp.Compare(i.Key, j.Key)
	})

	return res
}

func equal(key string) func(setting) bool {
	return func(s setting) bool {
		return s.Key == key
	}
}

func Delete(key string) error {
	mu.Lock()
	defer mu.Unlock()

	if idx := slices.IndexFunc(settings, equal(key)); idx >= 0 {
		if err := db.Instance.Delete(setting{
			Key: settings[idx].Key,
		}).Error; err != nil {
			return err
		}

		settings = slices.Delete(settings, idx, idx+1)
	}

	return nil
}

func SetString(key string, val string) {
	mu.Lock()
	defer mu.Unlock()

	if idx := slices.IndexFunc(settings, equal(key)); idx < 0 {
		settings = append(settings, setting{true, key, val})
		recordHistory(key, nil, val)
	} else if settings[idx].Value != val {
		old := settings[idx].Value
		settings[idx].dirty = true
		settings[idx].Value = val
		recordHistory(key, &old, val)
	}
}

func SetInt(key string, val int64) {
	SetString(key, strconv.FormatInt(val, 10))
}

func SetFloat(key string, val float64) {
	SetString(key, strconv.FormatFloat(val, 'f', -1, 64))
}

func SetTime(key string, val time.Time) {
	SetString(key, val.Format(time.RFC3339))
}

func SetBool(key string, val bool) {
	SetString(key, strconv.FormatBool(val))
}

func SetJson(key string, val any) error {
	b, err := json.Marshal(val)
	if err == nil {
		SetString(key, string(b))
	}
	return err
}

func SetYaml(key string, val any) error {
	var b bytes.Buffer
	err := yaml.NewEncoder(&b).Encode(val)
	if err == nil {
		SetString(key, strings.TrimSpace(b.String()))
	}
	return err
}

func Exists(key string) bool {
	mu.RLock()
	defer mu.RUnlock()

	s, err := String(key)
	return err == nil && len(s) > 0
}

func String(key string) (string, error) {
	mu.RLock()
	defer mu.RUnlock()

	idx := slices.IndexFunc(settings, equal(key))
	if idx < 0 {
		return "", ErrNotFound
	}
	return settings[idx].Value, nil
}

func Int(key string) (int64, error) {
	s, err := String(key)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(s, 10, 64)
}

func Float(key string) (float64, error) {
	s, err := String(key)
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(s, 64)
}

func Time(key string) (time.Time, error) {
	s, err := String(key)
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, s)
}

func Bool(key string) (bool, error) {
	s, err := String(key)
	if err != nil {
		return false, err
	}
	return strconv.ParseBool(s)
}

func Json(key string, res any) error {
	s, err := String(key)
	if err != nil {
		return err
	}
	if s == "" {
		return ErrNotFound
	}
	return json.Unmarshal([]byte(s), &res)
}

func DecodeOtherSliceOrMap(other, res any) error {
	var len int

	val := reflect.ValueOf(other)
	typ := reflect.TypeOf(other)

	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
		val = reflect.Indirect(val)
	}

	if typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
		len = val.Len()
	} else {
		return fmt.Errorf("cannot decode into slice or map: %v", other)
	}

	if len == 0 {
		return nil
	}

	return util.DecodeOther(other, &res)
}

func Yaml(key string, other, res any) error {
	s, err := String(key)
	if err != nil {
		return err
	}

	if s == "" {
		return ErrNotFound
	}

	if err := yaml.Unmarshal([]byte(s), &other); err != nil {
		return err
	}

	return DecodeOtherSliceOrMap(other, res)
}

func IsJson(key string) bool {
	s, err := String(key)
	return err == nil && json.Unmarshal([]byte(s), &json.RawMessage{}) == nil
}

// wrapping Settings into a struct for better decoupling
type Settings struct{}

func (s Settings) String(key string) (string, error) {
	return String(key)
}

func (s Settings) SetString(key string, value string) {
	SetString(key, value)
}
