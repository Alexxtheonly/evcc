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

	// AppliedMode is site.appliedBatteryMode() as observed shortly after the
	// slot began (the first control-loop tick to cross the boundary) - a
	// point sample taken seconds into the slot, not an end-of-slot summary.
	// Same forward-looking convention as persistTariffs: Timestamp is the
	// slot's start, not its end.
	//
	// "normal" therefore covers both an explicitly applied normal mode and a
	// site running with no evcc override at all - see appliedBatteryMode for
	// why those are the same fact. "unknown" here means only "this site has
	// no battery configured". Rows written before that distinction existed
	// (any row where a site with a battery recorded "unknown") predate it
	// and mean "no override".
	AppliedMode string `gorm:"column:applied_mode"`

	// ModeChanged is true if AppliedMode was observed to differ from this
	// row's own sample at least once later in the slot (see
	// site.persistControlSlot). AppliedMode itself is only ever the one
	// point sample taken at slot start - a reader must not assume it held
	// for the whole 15 minutes when this is true.
	ModeChanged bool `gorm:"column:mode_changed"`

	// SuggestedMode is what the optimizer derived this run, independent of
	// any veto (see optimizerDecision.suggestedMode / setOptimizerBatteryMode),
	// before any downstream override such as HEMS dimming. "charge" alongside
	// VetoReason "payback" is expected and is the whole point of persisting
	// both: the suggestion that was rejected, and why.
	//
	// Nil when no run produced a suggestion at all - the same reason Price is a
	// pointer, one field down. It used to be api.BatteryMode.String(), and
	// api.BatteryUnknown stringifies to "unknown": the identical token written
	// after clearSuggestions() fires on a failed run, and the one a site with no
	// controllable battery produces. On this site's own database that made 35 of
	// 57 rows (62%) indistinguishable from deliberate decisions, all with
	// health_ok = 1. ADR-011 rule 3: absence is never a sentinel.
	//
	// Rows written before this was nullable still carry the literal "unknown"
	// string; that legacy value is decoded back to nil on read (see
	// decodeSuggestedMode) rather than migrated, so a historical row is never
	// rewritten to say something it did not say.
	SuggestedMode *string `gorm:"column:suggested_mode"`

	// VetoReason explains why AppliedMode and SuggestedMode differ, empty
	// when they don't. Empty is not a placeholder for "unknown" here - it is
	// the reason enum's own zero value, matching the live wire format
	// (optimizerDecisionPublish.VetoReason has the same `omitempty` semantics).
	VetoReason string `gorm:"column:veto_reason"`

	// HealthOk is whether the optimizer was producing results at all at the
	// time of this slot, independent of what it decided.
	HealthOk bool `gorm:"column:health_ok"`

	// Price is the price (currency/kWh) the suggested charge decision was
	// based on. Nil unless SuggestedMode was an accepted (non-vetoed)
	// grid-charge decision - a charge can legitimately be justified at a
	// price of zero or below (see gridChargeJustified), so 0 cannot double
	// as "no price recorded" without silently discarding exactly the
	// free/negative-price slots the ledger most needs to see.
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
func PersistControlSlot(ts time.Time, appliedMode string, suggestedMode *string, vetoReason string, healthOk bool, price *float64) error {
	if db.Instance == nil {
		return nil
	}

	return db.Instance.Clauses(clause.OnConflict{DoNothing: true}).Create(&controlSlot{
		Timestamp:     ts.Unix(),
		AppliedMode:   appliedMode,
		SuggestedMode: suggestedMode,
		VetoReason:    vetoReason,
		HealthOk:      healthOk,
		Price:         price,
	}).Error
}

// MarkControlSlotModeChanged flags an already-persisted slot's row as having
// seen the applied mode diverge from its initial sample later in the slot
// (see ModeChanged and site.persistControlSlot). A no-op if the row does not
// exist (e.g. legacy data, or called with a stale slot) - there is nothing
// to flag on a row that was never written.
func MarkControlSlotModeChanged(ts time.Time) error {
	if db.Instance == nil {
		return nil
	}

	return db.Instance.Model(new(controlSlot)).Where("ts = ?", ts.Unix()).Update("mode_changed", true).Error
}

// DeleteControlSlots removes the slots in [from,to). Both bounds are
// required, a full wipe is /api/db/reset. F9 (ADR-011): the manual-delete
// endpoints only ever covered energy and tariffs before this - this closes
// that gap for control_slots.
func DeleteControlSlots(from, to time.Time) (int64, error) {
	return deleteRange[controlSlot](from, to)
}
