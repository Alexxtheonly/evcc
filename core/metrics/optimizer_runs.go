package metrics

import (
	"time"

	"github.com/evcc-io/evcc/server/db"
	"gorm.io/gorm/clause"
)

// optimizerRun records the diagnostic outcome of one optimizer run - status,
// and the economic/constraint numbers behind it. Diagnostic only: this is
// the cheapest input to "is the optimizer earning anything", not itself a
// savings figure - and ObjectiveValue must never be rendered in a currency
// context.
type optimizerRun struct {
	// Timestamp is the 15min slot boundary for a sampled Optimal/Feasible run
	// (one representative row per slot), or the run's own real timestamp for
	// any other status - every such run is recorded, not just the first per
	// slot. See PersistOptimizerRun.
	Timestamp int64  `gorm:"column:ts;uniqueIndex"`
	Status    string `gorm:"column:status"` // solver status: Optimal, Feasible, Infeasible, ...

	// The optimizer client's wire format zero-values these fields for any
	// status other than Optimal/Feasible (no schedule was produced, so there
	// is nothing to measure) - see optimizer.OptimizationResult.ObjectiveValue's
	// doc comment. Persisting res's zero value in that case would silently
	// misrepresent "no schedule" as "a schedule with zero overshoot", so all
	// four are left NULL together whenever Status isn't Optimal or Feasible.
	ObjectiveValue          *float64 `gorm:"column:objective_value"`       // LP objective over a moving horizon, in solver units - never currency
	GridImportOvershoot     *float64 `gorm:"column:grid_import_overshoot"` // Wh above PMaxImp across the horizon
	GridExportOvershoot     *float64 `gorm:"column:grid_export_overshoot"` // Wh curtailed above PMaxExp across the horizon
	GridImportLimitExceeded *bool    `gorm:"column:grid_import_limit_exceeded"`
	GridExportLimitHit      *bool    `gorm:"column:grid_export_limit_hit"`
}

func (optimizerRun) TableName() string {
	return "optimizer_runs"
}

// PersistOptimizerRun stores the diagnostic outcome of one optimizer run -
// either the sampled representative for a completed 15min slot, or (for any
// status other than Optimal/Feasible) the run's own timestamp, since the
// caller records every such run rather than sampling one per slot (see
// persistOptimizerRun).
//
// sampled distinguishes the two conflict strategies. For the sampled case,
// OnConflict DoNothing mirrors PersistTariffs/PersistControlSlot: the
// caller's own slot gate means this should only ever be called once for a
// given slot, but a duplicate call keeps the first observation rather than
// overwriting it. For the non-sampled case there is no such gate - every run
// is recorded at its own real timestamp - so two failing runs landing in the
// same wall-clock second (unix seconds is this table's timestamp
// granularity) use OnConflict UpdateAll instead: DoNothing would silently
// drop the second one.
func PersistOptimizerRun(ts time.Time, status string, objectiveValue, gridImportOvershoot, gridExportOvershoot *float64, gridImportLimitExceeded, gridExportLimitHit *bool, sampled bool) error {
	if db.Instance == nil {
		return nil
	}

	conflict := clause.OnConflict{DoNothing: true}
	if !sampled {
		conflict = clause.OnConflict{UpdateAll: true}
	}

	return db.Instance.Clauses(conflict).Create(&optimizerRun{
		Timestamp:               ts.Unix(),
		Status:                  status,
		ObjectiveValue:          objectiveValue,
		GridImportOvershoot:     gridImportOvershoot,
		GridExportOvershoot:     gridExportOvershoot,
		GridImportLimitExceeded: gridImportLimitExceeded,
		GridExportLimitHit:      gridExportLimitHit,
	}).Error
}

// DeleteOptimizerRuns removes the runs in [from,to). Both bounds are
// required, a full wipe is /api/db/reset.
func DeleteOptimizerRuns(from, to time.Time) (int64, error) {
	return deleteRange[optimizerRun](from, to)
}
