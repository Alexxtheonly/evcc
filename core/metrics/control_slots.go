package metrics

import (
	"time"

	"github.com/evcc-io/evcc/server/db"
	"gorm.io/gorm/clause"
)

// controlSlot records one completed 15min control decision: what the
// optimizer suggested and what was actually applied. The two differ whenever
// a gate (damping, payback, a live-rate move, disagreeing batteries) vetoes
// or delays the suggestion - see ADR-011. Capturing both, rather than only
// the applied mode, is what makes that gap auditable after the fact instead
// of merely asserted in the moment.
type controlSlot struct {
	Timestamp int64 `gorm:"column:ts;uniqueIndex"` // 15min slot boundary

	// AppliedMode is site.GetBatteryMode() at the end of the slot: the mode
	// actually in effect, after any veto or damping already took place.
	AppliedMode string `gorm:"column:applied_mode"`

	// SuggestedMode is the optimizer's vetted decision for the slot (the
	// confirmed candidate, not a raw per-run suggestion - see
	// setOptimizerBatteryMode), before any downstream override such as HEMS
	// dimming.
	SuggestedMode string `gorm:"column:suggested_mode"`

	// VetoReason explains why AppliedMode and SuggestedMode differ, empty
	// when they don't. Empty is not a placeholder for "unknown" here - it is
	// the reason enum's own zero value, matching the live wire format
	// (optimizerDecisionPublish.VetoReason has the same `omitempty` semantics).
	VetoReason string `gorm:"column:veto_reason"`

	// HealthOk is whether the optimizer was producing results at all at the
	// time of this slot, independent of what it decided.
	HealthOk bool `gorm:"column:health_ok"`

	// Price is the price (currency/kWh) the suggested charge decision was
	// based on. Nil unless SuggestedMode was an active grid-charge decision -
	// a charge can legitimately be justified at a price of zero or below
	// (see gridChargeJustified), so 0 cannot double as "no price recorded"
	// without silently discarding exactly the free/negative-price slots the
	// ledger most needs to see.
	Price *float64 `gorm:"column:price"`
}

func (controlSlot) TableName() string {
	return "control_slots"
}

// PersistControlSlot stores the control decision for one completed 15min
// slot. OnConflict DoNothing mirrors PersistTariffs: a slot's first
// observation is authoritative and later calls within the same slot (there
// should be none, given the caller's own slot gate) are silently ignored
// rather than overwriting it.
func PersistControlSlot(ts time.Time, appliedMode, suggestedMode, vetoReason string, healthOk bool, price *float64) error {
	return db.Instance.Clauses(clause.OnConflict{DoNothing: true}).Create(&controlSlot{
		Timestamp:     ts.Unix(),
		AppliedMode:   appliedMode,
		SuggestedMode: suggestedMode,
		VetoReason:    vetoReason,
		HealthOk:      healthOk,
		Price:         price,
	}).Error
}
