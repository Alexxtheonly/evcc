package metrics

// Savings ledger world chain (ADR-011 item 2): W0..W2, the counterfactual worlds
// priced (via ledger_settlement.go's settleFlows) on top of the shared slot series
// from ledger_slots.go. See ledger_slots.go for the honesty rules this and every
// other ledger file share.
//
// W0/W1/W2 all price slotData.modelledLoadKWh() (household residual + every
// loadpoint's EV charging), not HomeKWh alone. HomeKWh is a derived residual that
// core/site.go's updatePower already has loadpoint charge power subtracted out of, so
// pricing HomeKWh by itself modelled a household with no cars while W3 (the real grid
// meter, priced via GridImportKWh/GridExportKWh) paid for every EV kWh - the
// counterfactuals were then compared against a world that never happened. Worked
// example from one real August month, before this fix: reported Control contribution
// was -EUR 38 (the truth is a small positive), and the PV contribution was
// simultaneously overstated because W1 booked EV charging energy that actually went
// into a car as if it had been exported for feed-in revenue.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"gorm.io/gorm"
)

// batteryModeNormal etc. mirror api.BatteryMode.String() as plain strings, the same
// choice control_slots.go already made for AppliedMode/SuggestedMode - importing the
// api package here just to re-derive four constants isn't worth the coupling.
const (
	batteryModeNormal     = "normal"
	batteryModeHold       = "hold"
	batteryModeCharge     = "charge"
	batteryModeHoldCharge = "holdcharge"
)

// batteryEta is the round-trip-efficiency building block this package falls back to
// when the data can't defensibly support a derived one (see deriveBatteryPhysics).
// Duplicated from core/site_optimizer.go's `eta = 0.9` rather than imported: core
// already imports core/metrics, so the reverse import would cycle. Used one-way
// (charge and discharge each apply it once), matching how core/site_optimizer.go
// itself applies eta*eta for a round trip.
const batteryEta = 0.9

// computeW0 is the no-PV, no-battery baseline: every kWh of load - household plus
// every loadpoint's EV charging (see slotData.modelledLoadKWh) - is bought from the
// grid the moment it occurred.
func computeW0(slots []slotData) []worldFlow {
	out := make([]worldFlow, len(slots))
	for i, s := range slots {
		out[i] = worldFlow{ImportKWh: s.modelledLoadKWh()}
	}
	return out
}

// computeW1 adds direct PV self-consumption on top of W0: PV offsets load (household
// plus EV charging) first, any shortfall is bought, any surplus is exported at the
// feed-in price.
func computeW1(slots []slotData) []worldFlow {
	out := make([]worldFlow, len(slots))
	for i, s := range slots {
		load := s.modelledLoadKWh()
		out[i] = worldFlow{
			ImportKWh: max(0, load-s.PVKWh),
			ExportKWh: max(0, s.PVKWh-load),
		}
	}
	return out
}

// batteryPhysics bundles the assumptions the W2 counterfactual battery and the
// per-slot decision replay (item 4) share, so both use the same efficiency, capacity
// and rate ceiling rather than silently drifting apart.
//
// Deriving BOTH round-trip efficiency and capacity independently from matched
// ΔSoC/ΔEnergy windows isn't defensible - two unknowns, one equation per window. This
// instead treats batteryEta as known (see its doc comment) and derives CapacityKWh
// from data using it, which the ADR explicitly allows ("otherwise use the existing
// constant and say which"). Every field carries a Source string precisely so a caller
// can render "derived from N slots" vs "no data, defaulted" rather than hiding which
// one happened.
type batteryPhysics struct {
	CapacityKWh    float64
	CapacitySource string

	EtaC, EtaD float64
	EtaSource  string

	FloorFrac   float64
	FloorSource string

	// MaxChargeKWh/MaxDischargeKWh are the largest single-slot charge/discharge
	// energy ever observed for this battery - an empirical, data-derived stand-in
	// for an inverter rate limit that isn't persisted anywhere the ledger can read.
	// Only binds api.BatteryCharge's grid-forced branch (see simulateSlotStep);
	// unconstrained by rate would let one slot "charge" from empty to full, which
	// is unphysical and would overstate what a rejected alternative could have done.
	MaxChargeKWh    float64
	MaxDischargeKWh float64
}

// minSocDeltaFrac is the smallest SoC movement (as a fraction, e.g. 0.02 = 2
// percentage points) a matched charge/discharge-only window must show before it's
// trusted for capacity derivation - filters out sensor-resolution noise, not real
// movement.
const minSocDeltaFrac = 0.02

// minCapacityEvidenceFrac is the cumulative |ΔSoC| a side (charge or discharge) needs
// across all its qualifying windows before that side's capacity estimate is used at
// all - one or two noisy windows must not anchor a number this consequential.
const minCapacityEvidenceFrac = 0.20

// ErrBatteryPhysicsUnavailable means neither a persisted device capacity nor the
// battery's history (enough clean, single-direction SoC movement, agreeing across
// charge and discharge) is available to establish a capacity - the counterfactual
// battery (W2) and the decision replay (item 4) both refuse rather than guess a number
// with no basis (ADR-011 rule 4).
var ErrBatteryPhysicsUnavailable = errors.New("not enough battery history to derive capacity")

// capacityDisagreementFrac is the largest fractional difference between the
// charge-derived and discharge-derived capacity estimates that's still trusted enough
// to average. Averaging two estimates that disagree by more than this (e.g. 20kWh and
// 8kWh into 14kWh) fabricates a number neither side's evidence actually supports -
// refuse instead.
const capacityDisagreementFrac = 0.15

// batteryPhysicsCacheTTL bounds how long a derived batteryPhysics is reused across
// requests. Even on the persisted-capacity path this still runs a battery-history scan
// for the floor/max-rate fields, and ComputeChain calls it on every /api/savingsledger
// request against a database with a single connection (server/db/db.go's
// SetMaxOpenConns(1)) - capacity, efficiency and rate ceilings change at most a few
// times a year, so a few minutes of staleness costs nothing a caller would notice.
const batteryPhysicsCacheTTL = 5 * time.Minute

var batteryPhysicsCache struct {
	sync.Mutex
	db   *gorm.DB // keys the cache to the current db.Instance, so a test's fresh :memory: DB never sees another test's entry
	at   time.Time
	phys batteryPhysics
	err  error
}

// deriveBatteryPhysics establishes the counterfactual battery's capacity, preferring
// the device-reported capacity persisted via Collector.SetCapacity (core/site.go reads
// api.BatteryCapacity every battery-meter cycle - a hardware fact evcc already knows)
// and falling back to deriving one from the site's own charge/discharge SoC history,
// labelled as a fallback, only when persisted capacity isn't available for every
// configured battery. Either way this uses the full recorded history (a hardware
// property, not something scoped to the requested period) rather than [from,to) -
// bounded to the last MaxLedgerRangeDays and cached for batteryPhysicsCacheTTL, see
// batteryHistoryRows and this function's cache.
func deriveBatteryPhysics(ctx context.Context) (batteryPhysics, error) {
	batteryPhysicsCache.Lock()
	if batteryPhysicsCache.db == db.Instance && !batteryPhysicsCache.at.IsZero() && time.Since(batteryPhysicsCache.at) < batteryPhysicsCacheTTL {
		phys, err := batteryPhysicsCache.phys, batteryPhysicsCache.err
		batteryPhysicsCache.Unlock()
		return phys, err
	}
	batteryPhysicsCache.Unlock()

	phys, err := deriveBatteryPhysicsUncached(ctx)

	batteryPhysicsCache.Lock()
	batteryPhysicsCache.db = db.Instance
	batteryPhysicsCache.at = time.Now()
	batteryPhysicsCache.phys = phys
	batteryPhysicsCache.err = err
	batteryPhysicsCache.Unlock()

	return phys, err
}

func deriveBatteryPhysicsUncached(ctx context.Context) (batteryPhysics, error) {
	ids, err := batteryEntityIDs(ctx)
	if err != nil {
		return batteryPhysics{}, err
	}
	if len(ids) == 0 {
		return batteryPhysics{}, errors.New("no battery configured")
	}

	rows, err := batteryHistoryRows(ctx, ids)
	if err != nil {
		return batteryPhysics{}, err
	}

	var (
		minSocFrac                      = 1.0
		maxChargeSlot, maxDischargeSlot float64
		haveSoc                         bool
	)
	for _, r := range rows {
		if r.SocFrac != nil {
			haveSoc = true
			minSocFrac = min(minSocFrac, *r.SocFrac)
		}
		if r.ChargeKWh > 0 {
			maxChargeSlot = max(maxChargeSlot, r.ChargeKWh)
		}
		if r.DischargeKWh > 0 {
			maxDischargeSlot = max(maxDischargeSlot, r.DischargeKWh)
		}
	}

	capacityKWh, capacitySource, err := resolveBatteryCapacity(ctx, ids, rows)
	if err != nil {
		return batteryPhysics{}, err
	}

	floorFrac, floorSource := 0.0, "no SoC history, defaulted to 0%"
	if haveSoc {
		floorFrac, floorSource = minSocFrac, "lowest observed SoC in history"
	}

	return batteryPhysics{
		CapacityKWh:     capacityKWh,
		CapacitySource:  capacitySource,
		EtaC:            batteryEta,
		EtaD:            batteryEta,
		EtaSource:       "constant (core/site_optimizer.go eta=0.9), not derived - see batteryEta",
		FloorFrac:       floorFrac,
		FloorSource:     floorSource,
		MaxChargeKWh:    maxChargeSlot,
		MaxDischargeKWh: maxDischargeSlot,
	}, nil
}

// persistedBatteryCapacityKWh sums each battery entity's persisted capacity_kwh
// column. ok is true only when EVERY entity in ids has a value - a site with two
// batteries where only one reports capacity has no honest total, so this falls
// through to full derivation rather than silently summing a partial figure.
func persistedBatteryCapacityKWh(ctx context.Context, ids []int) (sum float64, ok bool, err error) {
	var caps []sql.NullFloat64
	if err := db.Instance.WithContext(ctx).Model(new(entity)).Where("id IN ?", ids).Pluck("capacity_kwh", &caps).Error; err != nil {
		return 0, false, err
	}
	if len(caps) != len(ids) {
		return 0, false, nil
	}
	for _, c := range caps {
		if !c.Valid {
			return 0, false, nil
		}
		sum += c.Float64
	}
	return sum, true, nil
}

// resolveBatteryCapacity prefers persisted device capacity over derivation - see
// deriveBatteryPhysics' doc comment for why.
func resolveBatteryCapacity(ctx context.Context, ids []int, rows []batteryHistoryRow) (float64, string, error) {
	if sum, ok, err := persistedBatteryCapacityKWh(ctx, ids); err != nil {
		return 0, "", err
	} else if ok {
		return sum, "device-reported capacity, persisted", nil
	}

	return deriveBatteryCapacityFromHistory(rows)
}

// deriveBatteryCapacityFromHistory is the fallback capacity estimator: matched,
// contiguous, single-direction SoC windows from the site's own charge/discharge
// history. Deriving BOTH round-trip efficiency and capacity independently from the
// same ΔSoC/ΔEnergy windows isn't defensible (two unknowns, one equation per window),
// so this treats batteryEta as known and solves for capacity using it.
func deriveBatteryCapacityFromHistory(rows []batteryHistoryRow) (float64, string, error) {
	var chargeKWhSum, chargeSocSum float64
	var dischargeKWhSum, dischargeSocSum float64

	for i := 1; i < len(rows); i++ {
		prev, cur := rows[i-1], rows[i]

		// only trust a window that is contiguous (no restart/gap between the two
		// readings) and has both SoC readings. soc_temp is recorded at SLOT START
		// (see meter.SocTemp's doc comment in db.go), so a delta between consecutive
		// readings was caused by the EARLIER row's energy (prev), not the later
		// row's (cur). Pairing it with cur instead attributes each slot's real
		// energy to the wrong SoC movement - on a smooth, uniform run the error is
		// small, but a ramp immediately followed by a stop (a grid-forced charge
		// that then holds) can attribute an entire real charge to a slot that moved
		// zero energy and drop the transition that actually caused the movement,
		// fabricating a capacity off by a large, unpredictable factor.
		if cur.Ts-prev.Ts != int64(quarterHourSeconds) || prev.SocFrac == nil || cur.SocFrac == nil {
			continue
		}

		delta := *cur.SocFrac - *prev.SocFrac

		switch {
		case prev.ChargeKWh > 0 && prev.DischargeKWh == 0 && delta >= minSocDeltaFrac:
			chargeKWhSum += prev.ChargeKWh
			chargeSocSum += delta
		case prev.DischargeKWh > 0 && prev.ChargeKWh == 0 && delta <= -minSocDeltaFrac:
			dischargeKWhSum += prev.DischargeKWh
			dischargeSocSum += -delta
		}
	}

	var capFromCharge, capFromDischarge float64
	haveCharge := chargeSocSum >= minCapacityEvidenceFrac
	haveDischarge := dischargeSocSum >= minCapacityEvidenceFrac

	if haveCharge {
		capFromCharge = chargeKWhSum * batteryEta / chargeSocSum
	}
	if haveDischarge {
		capFromDischarge = dischargeKWhSum / batteryEta / dischargeSocSum
	}

	switch {
	case haveCharge && haveDischarge:
		hi, lo := max(capFromCharge, capFromDischarge), min(capFromCharge, capFromDischarge)
		if hi <= 0 || (hi-lo)/hi > capacityDisagreementFrac {
			return 0, "", fmt.Errorf("%w: charge-derived %.2fkWh and discharge-derived %.2fkWh disagree by more than %.0f%%",
				ErrBatteryPhysicsUnavailable, capFromCharge, capFromDischarge, capacityDisagreementFrac*100)
		}
		return (capFromCharge + capFromDischarge) / 2, "derived (fallback) from charge and discharge SoC windows", nil
	case haveCharge:
		return capFromCharge, "derived (fallback) from charge-only SoC windows", nil
	case haveDischarge:
		return capFromDischarge, "derived (fallback) from discharge-only SoC windows", nil
	default:
		return 0, "", ErrBatteryPhysicsUnavailable
	}
}

const quarterHourSeconds = 15 * 60

// batteryHistoryRow is one 15min slot's aggregated battery reading, across every
// battery entity the site has, used only for capacity/rate derivation - unlike
// slotData this deliberately covers the battery's ENTIRE recorded history, not just
// the requested period, since capacity is a hardware property.
type batteryHistoryRow struct {
	Ts                      int64
	ChargeKWh, DischargeKWh float64
	SocFrac                 *float64
}

// batteryEntityIDs returns the entity ids for every configured battery.
func batteryEntityIDs(ctx context.Context) ([]int, error) {
	var ids []int
	err := db.Instance.WithContext(ctx).Model(new(entity)).Where(`"group" = ?`, Battery).Pluck("id", &ids).Error
	return ids, err
}

// batteryHistoryRows aggregates the meters table by slot across the given battery
// entities, excluding recovered/incomplete rows (an unreliable slot must not seed a
// capacity estimate), ordered so consecutive rows can be compared for contiguity.
// Bounded to the last MaxLedgerRangeDays: capacity is a hardware property that doesn't
// need the battery's ENTIRE lifetime to establish confidently, and an unbounded scan
// only grows more expensive, on every request, for the life of the installation.
func batteryHistoryRows(ctx context.Context, ids []int) ([]batteryHistoryRow, error) {
	type row struct {
		Ts           int64
		Energy       float64
		ReturnEnergy float64
		SocFrac      *float64
	}

	since := time.Now().AddDate(0, 0, -MaxLedgerRangeDays).Unix()

	var res []row
	if err := db.Instance.WithContext(ctx).Table("meters").
		Select(`ts, COALESCE(SUM(energy), 0) AS energy, COALESCE(SUM(return_energy), 0) AS return_energy, AVG(soc_temp) AS soc_frac`).
		// recovered/incomplete were added by AutoMigrate with no DEFAULT, so every
		// pre-migration row has them NULL. "= false" excludes a NULL via SQL's
		// three-valued logic (NULL = false is NULL, not true), which would silently
		// discard a site's entire pre-migration battery history - COALESCE(...) = 0
		// treats NULL the same as false (not recovered, not incomplete), matching
		// Collector.LastSlotEnergy's convention and ledger_slots.go's
		// queryGroupSlots (whose CASE/OR NULL-propagation happens to agree, but
		// implicitly - this makes both paths say the same thing explicitly).
		Where("meter IN ? AND COALESCE(recovered, 0) = 0 AND COALESCE(incomplete, 0) = 0 AND ts >= ?", ids, since).
		Group("ts").
		Order("ts").
		Scan(&res).Error; err != nil {
		return nil, err
	}

	out := make([]batteryHistoryRow, len(res))
	for i, r := range res {
		row := batteryHistoryRow{Ts: r.Ts, ChargeKWh: r.Energy, DischargeKWh: r.ReturnEnergy}
		if r.SocFrac != nil {
			// soc_temp is persisted as a percentage (see meter.SocTemp); normalise to
			// a 0..1 fraction here, matching slotData.BatterySocFrac's convention, so
			// minSocDeltaFrac/minCapacityEvidenceFrac mean the same thing everywhere
			frac := *r.SocFrac / 100
			row.SocFrac = &frac
		}
		out[i] = row
	}
	return out, nil
}

// simulateSlotStep applies one battery mode's rule to a single slot's home/PV
// balance, returning the resulting grid flow and the battery's new state of charge
// (in kWh). It is the one physics model shared by the W2 counterfactual (always
// batteryModeNormal) and the per-slot decision replay in ledger_decisions.go (applied
// vs. suggested mode), so both rest on the same capacity/efficiency/rate assumptions.
//
// Mode semantics modeled here (inferred from the mode names and how
// core/site_battery.go uses them, not re-derived from inverter docs - stated
// plainly, not hidden, since this is exactly the kind of assumption ADR-011 rule 7
// asks to be labelled rather than buried):
//   - hold: no charge, no discharge.
//   - normal: charge from surplus only, discharge to cover a deficit only - the "dumb
//     rule" W2 is defined as.
//   - charge: forces a charge from surplus AND, if surplus doesn't fill the
//     headroom, the grid. Never discharges.
//   - holdcharge: hold's "never discharge" combined with charge's "still absorb
//     surplus", but never draws from the grid - the charge half is surplus-only.
func simulateSlotStep(mode string, homeKWh, pvKWh, socKWh float64, phys batteryPhysics) (newSocKWh float64, flow worldFlow, chargeKWh, dischargeKWh float64) {
	surplus := max(0, pvKWh-homeKWh)
	deficit := max(0, homeKWh-pvKWh)
	floorKWh := phys.FloorFrac * phys.CapacityKWh
	headroomKWh := max(0, phys.CapacityKWh-socKWh)
	availableKWh := max(0, socKWh-floorKWh)

	switch mode {
	case batteryModeHold:
		return socKWh, worldFlow{ImportKWh: deficit, ExportKWh: surplus}, 0, 0

	case batteryModeCharge:
		chargeAC := min(phys.MaxChargeKWh, headroomKWh/phys.EtaC)
		fromSurplus := min(chargeAC, surplus)
		fromGrid := chargeAC - fromSurplus
		newSoc := socKWh + chargeAC*phys.EtaC
		return newSoc, worldFlow{ImportKWh: deficit + fromGrid, ExportKWh: max(0, surplus-fromSurplus)}, chargeAC, 0

	case batteryModeHoldCharge:
		chargeAC := min(phys.MaxChargeKWh, headroomKWh/phys.EtaC, surplus)
		newSoc := socKWh + chargeAC*phys.EtaC
		return newSoc, worldFlow{ImportKWh: deficit, ExportKWh: surplus - chargeAC}, chargeAC, 0

	default: // batteryModeNormal, and the fallback for any unrecognised mode string
		if surplus > 0 {
			chargeAC := min(phys.MaxChargeKWh, headroomKWh/phys.EtaC, surplus)
			newSoc := socKWh + chargeAC*phys.EtaC
			return newSoc, worldFlow{ExportKWh: surplus - chargeAC}, chargeAC, 0
		}
		dischargeAC := min(phys.MaxDischargeKWh, availableKWh*phys.EtaD, deficit)
		newSoc := socKWh - dischargeAC/phys.EtaD
		return newSoc, worldFlow{ImportKWh: deficit - dischargeAC}, 0, dischargeAC
	}
}

// ErrSocGap means a day in the requested period has no measured SoC at its first
// valid slot, so the counterfactual battery has nothing to re-anchor to. ADR-011
// rule 5 says refuse rather than let a simulation free-run across days on a stale
// estimate.
var ErrSocGap = errors.New("missing measured SoC at a day boundary")

// computeW2 simulates the "dumb rule" battery (charge from surplus only, discharge to
// house load only, never from grid) across slots, re-anchoring the simulated SoC to
// the measured SoC (BatterySocFrac) at the first valid slot of each calendar day. A
// free-running month-long simulation is not evidence (ADR-011 rule 5); resetting daily
// bounds how far the simulated and real batteries can have diverged before they're
// realigned.
func computeW2(slots []slotData, phys batteryPhysics) ([]worldFlow, error) {
	out := make([]worldFlow, len(slots))

	var socKWh float64
	var haveSoc bool
	var day string

	for i, s := range slots {
		if s.BatterySocFrac == nil {
			return nil, ErrSocGap
		}

		today := s.Start.Local().Format("2006-01-02")
		if today != day {
			// re-anchor: a new calendar day always resets to the measured SoC,
			// even if the previous day also had one - drift must not accumulate
			socKWh = *s.BatterySocFrac * phys.CapacityKWh
			haveSoc = true
			day = today
		}
		if !haveSoc {
			return nil, ErrSocGap
		}

		newSoc, flow, _, _ := simulateSlotStep(batteryModeNormal, s.modelledLoadKWh(), s.PVKWh, socKWh, phys)
		socKWh = newSoc
		out[i] = flow
	}

	return out, nil
}

// WorldCost is one world's cost, priced both ways.
type WorldCost struct {
	Label   string  `json:"label"`
	Settled Settled `json:"settled"`
}

// Contribution is one measure's share of the total savings: Cost(previous world) -
// Cost(this world). ADR-011's central defence: summing every Contribution in a Chain
// must equal Cost(W0) - Cost(actual) - see TestChainContributionsSumToWhole.
type Contribution struct {
	Label   string  `json:"label"`
	Settled Settled `json:"settled"`
}

// ControlSplit divides the W2->W3 contribution (the software's contribution, on top
// of the hardware) into routing and timing, per ADR-011 item 2's requirement that the
// two be distinguishable on a site where both are non-zero.
//
// Full is the headline: Cost(W2) - Cost(actual) at per-slot settlement.
// Routing is the same difference at period-average settlement - flattening every
// slot to the period's mean price removes timing entirely, isolating the value that
// comes purely from which sink (battery/export/grid) the energy went to.
// Timing is what's left: Full - Routing. Positive means the real controller bought
// and sold at better moments than the dumb rule's incidental timing did.
//
// Timing only reflects real money under per-slot settlement (see the Settled doc
// comment) - it is a genuine diagnostic under period-average settlement, not a
// currency figure that period-average billing actually pays, and callers must label
// it as such (ADR-011 rule 7).
type ControlSplit struct {
	Full    float64 `json:"full"`
	Routing float64 `json:"routing"`
	Timing  float64 `json:"timing"`
}

// Chain is the full W0..W3 chain for a period, priced both ways, plus the physics
// assumptions behind W2 (nil if the site has no battery, in which case W2 collapses
// to W1 and the battery contribution is honestly zero rather than omitted).
type Chain struct {
	Worlds         []WorldCost     `json:"worlds"`
	Contributions  []Contribution  `json:"contributions"`
	Coverage       Coverage        `json:"coverage"`
	BatteryPhysics *batteryPhysics `json:"batteryPhysics,omitempty"`
	Control        *ControlSplit   `json:"control,omitempty"`
}

// ComputeChain runs the full ADR-011 world chain for [from,to). See buildLedgerSlots
// for what counts as a valid slot and ErrBeforeTariffStart/ErrSocGap for the two ways
// this refuses rather than fabricates.
func ComputeChain(ctx context.Context, from, to time.Time) (*Chain, error) {
	set, err := buildLedgerSlots(ctx, from, to, true, true)
	if err != nil {
		return nil, err
	}

	w0 := computeW0(set.Slots)
	w1 := computeW1(set.Slots)
	w3 := actualFlows(set.Slots)

	var w2 []worldFlow
	var phys *batteryPhysics
	var control *ControlSplit

	if set.HasBattery {
		p, err := deriveBatteryPhysics(ctx)
		if err != nil {
			return nil, err
		}
		phys = &p

		w2, err = computeW2(set.Slots, p)
		if err != nil {
			return nil, err
		}

		w2Settled := settleFlows(set.Slots, w2)
		w3Settled := settleFlows(set.Slots, w3)
		full := w2Settled.PerSlot - w3Settled.PerSlot
		routing := w2Settled.PeriodAverage - w3Settled.PeriodAverage
		control = &ControlSplit{Full: full, Routing: routing, Timing: full - routing}
	} else {
		// no battery configured: W2 has nothing to add over W1, so it collapses to
		// W1 rather than being fabricated - the resulting "Battery" contribution
		// below is honestly zero, not hidden.
		w2 = w1
	}

	worlds := []WorldCost{
		{Label: "W0", Settled: settleFlows(set.Slots, w0)},
		{Label: "W1", Settled: settleFlows(set.Slots, w1)},
		{Label: "W2", Settled: settleFlows(set.Slots, w2)},
		{Label: "W3", Settled: settleFlows(set.Slots, w3)},
	}

	contributions := []Contribution{
		{Label: "PV", Settled: diffSettled(worlds[0].Settled, worlds[1].Settled)},
		{Label: "Battery", Settled: diffSettled(worlds[1].Settled, worlds[2].Settled)},
		{Label: "Control", Settled: diffSettled(worlds[2].Settled, worlds[3].Settled)},
	}

	return &Chain{
		Worlds:         worlds,
		Contributions:  contributions,
		Coverage:       set.coverage(),
		BatteryPhysics: phys,
		Control:        control,
	}, nil
}
