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

	"github.com/evcc-io/evcc/core/keys"
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
//
// Deleted marks a row that records a key's removal rather than a write - New
// is meaningless ("") on such a row. Without this, a deleted key would read
// as "still at its last value forever" to any later replay, since there
// would be no row at all marking the point after which it no longer applied.
type settingHistory struct {
	ID        uint    `gorm:"primarykey"`
	Timestamp int64   `json:"ts" gorm:"column:ts;index"`
	Key       string  `json:"key" gorm:"column:key;index"`
	Old       *string `json:"old" gorm:"column:old"`
	New       string  `json:"new" gorm:"column:new"`
	Deleted   bool    `json:"deleted,omitempty" gorm:"column:deleted"`
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

// historyKeys are the setting keys whose changes settings_history records:
// user-facing configuration that changes what the controller does, or how a
// later replay must price what it did (ADR-011). Everything else is not
// recorded.
//
// This is deliberately an allowlist and not a list of secret-looking names.
// The settings table is a single flat key space shared by configuration and
// by credentials, and the credential keys are largely NOT known in advance:
// plugin/auth's OAuth tokens are stored under "<clientID>-<hash>" subjects
// (plugin/auth/oauth.go), so no denylist written today can name the token
// key of a vehicle or tariff integration added tomorrow. An allowlist
// excludes every such dynamically-named key for free, along with the
// statically-named secrets (sponsorToken, eebus - which carries the SHIP
// private key and pairing secret -, adminPassword, jwtSecretKey, apiKey,
// mqtt/influx/ocpp credentials). A denylist would have had to be right about
// all of them, forever, and being wrong once writes a permanent cleartext
// archive: the live settings table only ever holds the CURRENT token, while
// settings_history is append-only and never pruned.
//
// The cost of the allowlist is the converse: a key added later is silently
// not recorded until it is added here.
var historyKeys = []string{
	// site
	keys.Currency, keys.TariffRefs,
	keys.GridMeter, keys.PvMeters, keys.BatteryMeters, keys.AuxMeters, keys.ConsumerMeters, keys.ExtMeters,
	keys.PrioritySoc, keys.BufferSoc, keys.BufferStartSoc, keys.ResidualPower, keys.GridExportLimit,
	keys.BatteryDischargeControl, keys.BatteryGridChargeLimit, keys.BatteryGridDischarge,
	keys.SolarAdjusted,
	keys.Experimental, keys.Optimizer, keys.OptimizerAutomatic, keys.OptimizerChargingStrategy,

	// loadpoint and vehicle, written namespaced ("lp1.", "vehicle.<name>.", "db:<id>.")
	keys.Disabled, keys.Mode, keys.DefaultMode,
	keys.Charger, keys.Meter, keys.Circuit, keys.DefaultVehicle,
	keys.Priority, keys.PhasesConfigured, keys.MinCurrent, keys.MaxCurrent, keys.Thresholds,
	keys.MinSoc, keys.LimitSoc, keys.LimitEnergy, keys.Soc,
	keys.BatteryBoostLimit, keys.SmartCostLimit, keys.SmartFeedInPriorityLimit,
	keys.PlanTime, keys.PlanEnergy, keys.PlanSoc, keys.PlanStrategy,
	keys.RepeatingPlans, keys.AdaptivePlans, keys.AdaptivePlanLearning,
}

// recordable reports whether key belongs in settings_history. Device-scoped
// keys arrive namespaced (dbSettings prefixes "lp1."/"vehicle.luna.",
// ConfigSettings prefixes "db:3."), so only the segment after the last dot is
// matched - the namespace itself carries no information the allowlist needs,
// and enumerating every loadpoint and vehicle name up front is impossible.
func recordable(key string) bool {
	if i := strings.LastIndex(key, "."); i >= 0 {
		key = key[i+1:]
	}
	return slices.Contains(historyKeys, key)
}

// persistHistory writes one already-built settings_history row for a
// recordable key (see historyKeys - a credential must never reach this
// append-only table). A nil db.Instance (unit tests exercising
// SetString/String in isolation, without a database) is a silent no-op,
// matching the test guard used throughout this codebase for optional
// persistence.
//
// Must be called without mu held. SetString and Delete build the row while
// mu is locked (so it reflects exactly the mutation that just happened, with
// no window for another writer to interleave), then unlock before calling
// this - settings.String() is read on the control-loop path (see
// core/vehicle/adapter.go), and a contended sqlite insert can hold a mutex
// for the length of its busy_timeout, which would otherwise stall every
// other settings reader/writer in the process for as long as this write is
// blocked, not just for the fraction of that time the insert itself needs.
func persistHistory(h settingHistory) {
	if db.Instance == nil || !recordable(h.Key) {
		return
	}

	if err := db.Instance.Create(&h).Error; err != nil {
		log.ERROR.Printf("persist settings history: %v", err)
	}
}

// RecordHistory appends a settings_history row for a value written by a
// Settings implementation other than this package's own SetString -
// currently core/settings.ConfigSettings, which persists database-configured
// loadpoints through the configs table (conf.Update) rather than through
// SetString, and would otherwise be entirely invisible to the audit trail
// this table exists for (ADR-011). Callers own their own dedup check; unlike
// SetString, every call here writes a row for any recordable key, with no
// change check of its own.
func RecordHistory(key string, old *string, val string) {
	persistHistory(settingHistory{Timestamp: time.Now().Unix(), Key: key, Old: old, New: val})
}

// DeleteHistory removes the settings_history rows in [from,to). Both bounds
// are required, a full wipe is /api/db/reset. F9 (ADR-011): the manual-
// delete endpoints only ever covered energy and tariffs before this - this
// closes that gap for settings_history.
func DeleteHistory(from, to time.Time) (int64, error) {
	if from.IsZero() || to.IsZero() {
		return 0, errors.New("missing from/to")
	}

	res := db.Instance.Where("ts >= ? AND ts < ?", from.Unix(), to.Unix()).Delete(new(settingHistory))
	return res.RowsAffected, res.Error
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

	idx := slices.IndexFunc(settings, equal(key))
	if idx < 0 {
		mu.Unlock()
		return nil
	}

	old := settings[idx].Key
	oldVal := settings[idx].Value

	if err := db.Instance.Delete(setting{Key: old}).Error; err != nil {
		mu.Unlock()
		return err
	}

	settings = slices.Delete(settings, idx, idx+1)

	mu.Unlock()

	// record the removal itself - without this a deleted key reads as "still
	// at its last value forever" to any later replay, since nothing marks
	// the point after which it stopped applying
	persistHistory(settingHistory{Timestamp: time.Now().Unix(), Key: key, Old: &oldVal, Deleted: true})

	return nil
}

func SetString(key string, val string) {
	mu.Lock()

	var h *settingHistory

	if idx := slices.IndexFunc(settings, equal(key)); idx < 0 {
		settings = append(settings, setting{true, key, val})
		h = &settingHistory{Timestamp: time.Now().Unix(), Key: key, New: val}
	} else if settings[idx].Value != val {
		old := settings[idx].Value
		settings[idx].dirty = true
		settings[idx].Value = val
		h = &settingHistory{Timestamp: time.Now().Unix(), Key: key, Old: &old, New: val}
	}

	mu.Unlock()

	if h != nil {
		persistHistory(*h)
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
