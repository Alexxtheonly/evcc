package metrics

import (
	"time"

	"github.com/evcc-io/evcc/server/db"
	"gorm.io/gorm/clause"
)

// optimizerRun records the diagnostic outcome of one optimizer run per
// completed 15min slot - status, and the economic/constraint numbers behind
// it. Diagnostic only: this is the cheapest input to "is the optimizer
// earning anything", not itself a savings figure (see ADR-011, which also
// forbids ever rendering ObjectiveValue in a currency context).
type optimizerRun struct {
	Timestamp int64  `gorm:"column:ts;uniqueIndex"` // 15min slot boundary
	Status    string `gorm:"column:status"`         // solver status: Optimal, Feasible, Infeasible, ...

	// The optimizer client's wire format zero-values these fields for any
	// status other than Optimal/Feasible (no schedule was produced, so there
	// is nothing to measure) - see optimizer.OptimizationResult.ObjectiveValue's
	// doc comment. Persisting res's zero value in that case would silently
	// misrepresent "no schedule" as "a schedule with zero overshoot", so all
	// four are left NULL together whenever Status isn't Optimal or Feasible.
	ObjectiveValue          *float64 `gorm:"column:objective_value"`       // solver units, never currency (see ADR-011 rule 6)
	GridImportOvershoot     *float64 `gorm:"column:grid_import_overshoot"` // Wh above PMaxImp across the horizon
	GridExportOvershoot     *float64 `gorm:"column:grid_export_overshoot"` // Wh curtailed above PMaxExp across the horizon
	GridImportLimitExceeded *bool    `gorm:"column:grid_import_limit_exceeded"`
	GridExportLimitHit      *bool    `gorm:"column:grid_export_limit_hit"`
}

func (optimizerRun) TableName() string {
	return "optimizer_runs"
}

// PersistOptimizerRun stores the diagnostic outcome of the optimizer run
// representing one completed 15min slot. OnConflict DoNothing mirrors
// PersistTariffs/PersistControlSlot: the caller's own slot gate means this
// should only ever be called once per slot, but a duplicate call keeps the
// first observation rather than overwriting it.
func PersistOptimizerRun(ts time.Time, status string, objectiveValue, gridImportOvershoot, gridExportOvershoot *float64, gridImportLimitExceeded, gridExportLimitHit *bool) error {
	return db.Instance.Clauses(clause.OnConflict{DoNothing: true}).Create(&optimizerRun{
		Timestamp:               ts.Unix(),
		Status:                  status,
		ObjectiveValue:          objectiveValue,
		GridImportOvershoot:     gridImportOvershoot,
		GridExportOvershoot:     gridExportOvershoot,
		GridImportLimitExceeded: gridImportLimitExceeded,
		GridExportLimitHit:      gridExportLimitHit,
	}).Error
}
