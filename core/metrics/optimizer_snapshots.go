package metrics

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"gorm.io/gorm"
)

// OptimizerSnapshot freezes the versioned inputs and result of a planning run.
type OptimizerSnapshot struct {
	ID                uint64          `json:"id" gorm:"primaryKey"`
	Version           int             `json:"version"`
	Timestamp         time.Time       `json:"timestamp" gorm:"-"`
	TS                int64           `json:"-" gorm:"column:ts;index"`
	ControllerVersion string          `json:"controllerVersion"`
	Request           json.RawMessage `json:"request" gorm:"type:blob"`
	Result            json.RawMessage `json:"result" gorm:"type:blob"`
	Economics         json.RawMessage `json:"economics" gorm:"type:blob"`
	Quality           json.RawMessage `json:"quality" gorm:"type:blob"`
}

// SnapshotBatteryEconomics describes the assumptions used for one stable device identity.
type SnapshotBatteryEconomics struct {
	ChargeCeilingFrac *float64 `json:"chargeCeilingFrac,omitempty"`
	MeasurementPlane  string   `json:"measurementPlane,omitempty"`
	Name              string   `json:"name"`
	CapacityKWh       float64  `json:"capacityKWh"`
	EtaC              float64  `json:"etaC"`
	EtaD              float64  `json:"etaD"`
	FloorFrac         float64  `json:"floorFrac"`
	MaxChargeKWh      float64  `json:"maxChargeKWh"`
	MaxDischargeKWh   float64  `json:"maxDischargeKWh"`
	WearPerKWh        *float64 `json:"wearPerKWh,omitempty"`
	Source            string   `json:"source"`
}

// SaveOptimizerSnapshot bounds payload size and retention without rewriting prior runs.
func SaveOptimizerSnapshot(s OptimizerSnapshot) (uint64, error) {
	if db.Instance == nil {
		return 0, errors.New("metrics database unavailable")
	}
	if s.Timestamp.IsZero() || s.ControllerVersion == "" {
		return 0, errors.New("snapshot timestamp and controller version required")
	}
	total := 0
	for _, v := range []json.RawMessage{s.Request, s.Result, s.Economics, s.Quality} {
		total += len(v)
		if len(v) > 0 && !json.Valid(v) {
			return 0, errors.New("invalid snapshot JSON")
		}
	}
	if total > 512*1024 {
		return 0, errors.New("optimizer snapshot exceeds 512 KiB")
	}
	s.ID = 0
	s.Version = 1
	s.TS = s.Timestamp.Unix()
	err := db.Instance.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&s).Error; err != nil {
			return err
		}
		// Keep at most 10,000 snapshots and thirty days even during a failing tight loop.
		var expired []uint64
		if err := tx.Model(new(OptimizerSnapshot)).Where("ts < ? OR id IN (SELECT id FROM optimizer_snapshots ORDER BY id DESC LIMIT -1 OFFSET 10000)", s.Timestamp.AddDate(0, 0, -30).Unix()).Pluck("id", &expired).Error; err != nil {
			return err
		}
		return deleteSnapshotIDs(tx, expired)
	})
	return s.ID, err
}

func deleteSnapshotIDs(tx *gorm.DB, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	for _, model := range []any{new(controlSlot), new(optimizerRun)} {
		if err := tx.Model(model).Where("optimizer_snapshot_id IN ?", ids).Updates(map[string]any{"optimizer_snapshot_id": nil, "snapshot_unavailable": true}).Error; err != nil {
			return err
		}
	}
	return tx.Where("id IN ?", ids).Delete(new(OptimizerSnapshot)).Error
}

// GetOptimizerSnapshot returns an immutable run for a decision explanation.
func GetOptimizerSnapshot(id uint64) (*OptimizerSnapshot, error) {
	var s OptimizerSnapshot
	if err := db.Instance.First(&s, id).Error; err != nil {
		return nil, err
	}
	s.Timestamp = time.Unix(s.TS, 0)
	return &s, nil
}

// BindControlSlotSnapshot links only the slot's first observed decision.
func BindControlSlotSnapshot(ts time.Time, id uint64) error {
	return bindSnapshot(new(controlSlot), ts, id)
}

// InvalidateControlSlotSnapshot refuses a whole-slot replay when its planning
// assumptions changed mid-slot. Keep the original audit link, never reprice it.
func InvalidateControlSlotSnapshot(ts time.Time) error {
	return db.Instance.Model(new(controlSlot)).Where("ts = ?", ts.Unix()).Update("snapshot_unavailable", true).Error
}

// BindOptimizerRunSnapshot links an archived diagnostic run to its frozen inputs.
func BindOptimizerRunSnapshot(ts time.Time, id uint64) error {
	return bindSnapshot(new(optimizerRun), ts, id)
}

func bindSnapshot(model any, ts time.Time, id uint64) error {
	if id == 0 {
		return nil
	}
	var count int64
	if err := db.Instance.Model(new(OptimizerSnapshot)).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("optimizer snapshot %d unavailable", id)
	}
	return db.Instance.Model(model).Where("ts = ? AND optimizer_snapshot_id IS NULL AND COALESCE(snapshot_unavailable,0)=0", ts.Unix()).Update("optimizer_snapshot_id", id).Error
}

// DeleteOptimizerSnapshots deletes the selected history and clears its audit links.
func DeleteOptimizerSnapshots(from, to time.Time) (int64, error) {
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return 0, errors.New("invalid snapshot deletion interval")
	}
	var ids []uint64
	err := db.Instance.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(new(OptimizerSnapshot)).Where("ts >= ? AND ts < ?", from.Unix(), to.Unix()).Pluck("id", &ids).Error; err != nil {
			return err
		}
		return deleteSnapshotIDs(tx, ids)
	})
	return int64(len(ids)), err
}

// DeleteHomeForecastSamples removes predictions targeting the selected period.
func DeleteHomeForecastSamples(from, to time.Time) (int64, error) {
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return 0, errors.New("invalid forecast deletion interval")
	}
	r := db.Instance.Where("slot >= ? AND slot < ?", from.Unix(), to.Unix()).Delete(new(homeForecastSample))
	return r.RowsAffected, r.Error
}
