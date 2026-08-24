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
	"math"
	"slices"
	"sync"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"gorm.io/gorm"
)

// batteryModeNormal etc. mirror api.BatteryMode.String() as plain strings, the same
// choice control_slots.go already made for AppliedMode/SuggestedMode - importing the
// api package here just to re-derive four constants isn't worth the coupling.
const (
	batteryModeUnknown    = "unknown"
	batteryModeNormal     = "normal"
	batteryModeHold       = "hold"
	batteryModeCharge     = "charge"
	batteryModeHoldCharge = "holdcharge"
)

// batteryEta is the round-trip-efficiency building block this package falls back to
// when the data can't defensibly support a derived one (see deriveBatteryPhysics).
// Used one-way (charge and discharge each apply it once), matching how
// core/site_optimizer.go's own eta applies eta*eta for a round trip.
const batteryEta = 0.9

// BatteryEta is batteryEta, exported so core/site_optimizer.go's `eta` constant can be
// defined as metrics.BatteryEta instead of an independently-typed 0.9 that could drift
// from this one with nothing to catch it - core already imports core/metrics (the
// reverse import would cycle), so this is a same-value const, not a duplicate.
const BatteryEta = batteryEta

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
// one happened. Fields are camelCase-tagged to match every other payload type in this
// package (Settled, Coverage, ...) - without tags this serialised as PascalCase amid
// lowerCamel siblings, the one field in the whole /api/savingsledger response that
// looked like it came from a different API.
type batteryPhysics struct {
	CapacityKWh    float64 `json:"capacityKWh"`
	CapacitySource string  `json:"capacitySource"`

	EtaC      float64 `json:"etaC"`
	EtaD      float64 `json:"etaD"`
	EtaSource string  `json:"etaSource"`

	FloorFrac   float64 `json:"floorFrac"`
	FloorSource string  `json:"floorSource"`

	// MaxChargeKWh/MaxDischargeKWh are the rateLimitPercentile (p99) single-slot
	// charge/discharge energy observed for this battery - an empirical, data-derived
	// stand-in for an inverter rate limit that isn't persisted anywhere the ledger
	// can read. Deliberately NOT the raw maximum: a single glitched slot (a meter
	// spike, or a one-off grid-forced test charge) would otherwise become the
	// ceiling for the battery's entire future, letting the model "charge" from empty
	// to full in one 15-minute slot - unphysical, and it always overstates what a
	// rejected alternative could have done (in the flattering direction: bigger
	// swings make the model's counterfactual look better than the real one). Only
	// binds api.BatteryCharge's grid-forced branch (see simulateSlotStep).
	MaxChargeKWh    float64 `json:"maxChargeKWh"`
	MaxDischargeKWh float64 `json:"maxDischargeKWh"`

	// HasChargeEvidence/HasDischargeEvidence are false when the corresponding sample
	// list handed to percentile() was empty - percentile() returns 0 for an empty
	// slice, which is indistinguishable from "observed and genuinely tiny" once it's
	// sitting in MaxChargeKWh/MaxDischargeKWh. A site the controller has been holding
	// (or one that has simply never discharged) has an empty discharge sample list -
	// computeChainFromSlots uses these to refuse (ErrBatteryRateCeilingUnavailable)
	// rather than let computeW2 silently model a battery that can't move in that
	// direction at all, collapsing towards W1 and booking the battery's entire real
	// value to Control with nothing to say so.
	HasChargeEvidence    bool `json:"-"`
	HasDischargeEvidence bool `json:"-"`
}

// rateCeilingNote renders MaxChargeKWh/MaxDischargeKWh and their provenance into the
// chain's Notes (ADR-011 rule 7) - the ceiling silently throttles or unlocks the W2
// counterfactual battery, so a reader comparing two periods needs to see it, not just
// the number it produced.
func (p batteryPhysics) rateCeilingNote() string {
	return fmt.Sprintf("counterfactual battery rate ceiling: %.3fkWh/slot charge, %.3fkWh/slot discharge - the %.0fth percentile of observed single-slot energy in this battery's history (not a device spec)",
		p.MaxChargeKWh, p.MaxDischargeKWh, rateLimitPercentile*100)
}

// floorNote renders FloorFrac and its provenance into the chain's Notes (ADR-011
// rule 7). The floor directly throttles how much the W2 counterfactual battery is
// allowed to discharge, and moving it is enough to flip the sign of Control (see
// TestFloorFracSensitivityIsLabelled) - so which of the two sources produced it belongs
// in Notes, the one field every caller already renders unconditionally, rather than
// sitting unused inside batteryPhysics. The fallback wording is the sharper of the two
// deliberately: an observed minimum is a statistic of the audited controller's own
// behaviour (see resolveBatteryFloor).
func (p batteryPhysics) floorNote() string {
	if p.FloorSource == floorSourceConfigured {
		return fmt.Sprintf("counterfactual battery floor: %.1f%% SoC - the installation's configured minimum (%s)", p.FloorFrac*100, p.FloorSource)
	}
	return fmt.Sprintf("counterfactual battery floor: %.1f%% SoC (%s) - no configured minimum is on record for this battery, so this is the lowest SoC ever observed, which is a behaviour of the controller being measured; a single extra low reading can move it and therefore the control contribution materially",
		p.FloorFrac*100, p.FloorSource)
}

// floorSourceConfigured is the FloorSource value resolveBatteryFloor sets when the
// installation's own configured minimum SoC was available - a named constant rather than
// a repeated literal because floorNote branches on it.
const floorSourceConfigured = "configured minimum SoC, device-reported"

// rateLimitPercentile is the percentile used to establish MaxChargeKWh/MaxDischargeKWh
// from history, instead of the single largest slot ever observed - see those fields'
// doc comment for why a raw max() is the wrong statistic here.
const rateLimitPercentile = 0.99

// percentile returns the value at percentile p (0..1) of values, nearest-rank method.
// Returns 0 for an empty slice. Does not mutate values.
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	rank = max(0, min(rank, len(sorted)-1))
	return sorted[rank]
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
		haveSoc                         bool
		chargeSamples, dischargeSamples []float64
	)
	for _, r := range rows {
		if r.SocFrac != nil {
			haveSoc = true
			minSocFrac = min(minSocFrac, *r.SocFrac)
		}
		if r.ChargeKWh > 0 {
			chargeSamples = append(chargeSamples, r.ChargeKWh)
		}
		if r.DischargeKWh > 0 {
			dischargeSamples = append(dischargeSamples, r.DischargeKWh)
		}
	}
	maxChargeSlot := percentile(chargeSamples, rateLimitPercentile)
	maxDischargeSlot := percentile(dischargeSamples, rateLimitPercentile)

	capacityKWh, capacitySource, err := resolveBatteryCapacity(ctx, ids, rows)
	if err != nil {
		return batteryPhysics{}, err
	}

	floorFrac, floorSource := resolveBatteryFloor(ctx, ids, minSocFrac, haveSoc)

	return batteryPhysics{
		CapacityKWh:          capacityKWh,
		CapacitySource:       capacitySource,
		EtaC:                 batteryEta,
		EtaD:                 batteryEta,
		EtaSource:            "constant (0.9), not derived - shared with core/site_optimizer.go's eta, see BatteryEta",
		FloorFrac:            floorFrac,
		FloorSource:          floorSource,
		MaxChargeKWh:         maxChargeSlot,
		MaxDischargeKWh:      maxDischargeSlot,
		HasChargeEvidence:    len(chargeSamples) > 0,
		HasDischargeEvidence: len(dischargeSamples) > 0,
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

// persistedBatteryFloorFrac is the capacity-weighted mean of every battery entity's
// persisted min_soc_frac (Collector.SetMinSoc, from api.BatterySocLimiter) - i.e. the
// floor of the single aggregate pack the counterfactual models, Sigma(min_i * cap_i) /
// Sigma(cap_i). ok is true only when EVERY entity has BOTH a persisted floor and a
// persisted capacity: without each battery's own capacity there is no defensible weight
// to combine two different floors with, and a site where only one battery reports a
// limit has no honest aggregate at all. Same all-or-nothing rule
// persistedBatteryCapacityKWh already applies, for the same reason.
func persistedBatteryFloorFrac(ctx context.Context, ids []int) (frac float64, ok bool, err error) {
	// explicit column tags: gorm's default naming strategy turns CapacityKWh into
	// "capacity_k_wh", which SQLite happily scans as NULL for every row - the same trap
	// queryTariffSlots' FeedIn field documents. entity's own field carries the tag;
	// a scan struct without one silently reports "no persisted capacity" forever.
	var rows []struct {
		MinSocFrac  sql.NullFloat64 `gorm:"column:min_soc_frac"`
		CapacityKWh sql.NullFloat64 `gorm:"column:capacity_kwh"`
	}
	if err := db.Instance.WithContext(ctx).Model(new(entity)).
		Select("min_soc_frac, capacity_kwh").Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		return 0, false, err
	}
	if len(rows) != len(ids) {
		return 0, false, nil
	}

	var weighted, capacity float64
	for _, r := range rows {
		if !r.MinSocFrac.Valid || !r.CapacityKWh.Valid {
			return 0, false, nil
		}
		weighted += r.MinSocFrac.Float64 * r.CapacityKWh.Float64
		capacity += r.CapacityKWh.Float64
	}
	if capacity <= 0 {
		return 0, false, nil
	}
	return weighted / capacity, true, nil
}

// resolveBatteryFloor establishes the counterfactual battery's discharge floor,
// preferring the installation's own configured minimum SoC over the lowest SoC ever
// observed.
//
// The observed minimum is what this used to use unconditionally, and it is unsound in
// two ways. It is RETROACTIVE: it is taken over the whole retained history, so one new
// low reading silently restates every figure the ledger has ever reported, and today's
// low rows ageing past MaxLedgerRangeDays restates them again. And it is CIRCULAR: "how
// deep has this pack ever been run" is a behaviour of the very controller the ledger
// audits, so a controller that never discharges deeply gives its own counterfactual a
// high floor, which makes the counterfactual expensive, which flatters the controller.
// On this site's own database the difference was not academic - the observed 4.1% floor
// against the configured 5% moved the Control contribution by EUR 0.17 on a window whose
// entire realised grid cost was EUR 3.97, and a 10% floor flipped its sign.
//
// The observed minimum stays as the fallback for a battery that reports no limit, but it
// is labelled as one: FloorSource is the provenance string floorNote renders, and it must
// always say which of the two produced the number.
func resolveBatteryFloor(ctx context.Context, ids []int, observedMin float64, haveSoc bool) (float64, string) {
	if frac, ok, err := persistedBatteryFloorFrac(ctx, ids); err == nil && ok {
		return frac, floorSourceConfigured
	}
	if haveSoc {
		return observedMin, "lowest observed SoC in history (fallback: no configured minimum recorded)"
	}
	return 0, "no SoC history and no configured minimum, defaulted to 0%"
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

// ErrSocGap means a slot in the requested period has no measured SoC, so the
// counterfactual battery has no state to start from. In practice this is currently
// unreachable through ComputeChain, since buildLedgerSlots already drops any slot
// missing BatterySocFrac before computeW2 ever sees it - kept as a defensive check (a
// future caller building slots another way must not silently start from zero instead)
// rather than something the existing test suite can exercise end-to-end.
var ErrSocGap = errors.New("missing measured SoC")

// ErrBatteryRateCeilingUnavailable means the battery's history has no observed
// charge or discharge samples in one direction - see batteryPhysics'
// HasChargeEvidence/HasDischargeEvidence doc comment for why a 0 rate ceiling from an
// empty sample list must not be handed to computeW2 as if it were physical fact.
var ErrBatteryRateCeilingUnavailable = errors.New("no observed charge or discharge history to establish the counterfactual battery's rate ceiling")

// W2Drift is the counterfactual battery's energy bookkeeping, published beside
// MeterResidual for the same reason: it is a known gap under W2's euro figure, and the
// only honest thing to do with it is print it.
//
// The counterfactual battery is anchored to the measured SoC once, at the period's
// first valid slot, and simulated from there. It is NOT re-anchored afterwards - see
// computeW2 for why a re-anchor is an unpriced energy injection rather than a
// correction. What it does do at a gap is CARRY the measured pack's own state change
// across the unmeasured stretch (CarriedKWh), because the excluded slots are excluded
// from W3 too and W3 still receives that energy implicitly, through the meter: its
// post-gap import is lower because the real pack was filled during hours nobody priced.
// Giving W2 the same movement, and nothing else, is what keeps the two worlds
// comparable across a hole in the record.
type W2Drift struct {
	// Gaps is the number of breaks in the otherwise-15-minute-contiguous slot series.
	Gaps int `json:"gaps"`
	// CarriedKWh is the net energy handed to the counterfactual battery across those
	// gaps - the measured pack's own movement while the ledger was not looking,
	// bounded by the same floor/capacity simulateSlotStep enforces every slot. It is
	// unpriced in W2 exactly as it is unpriced in W3.
	CarriedKWh float64 `json:"carriedKWh"`
	// FinalKWh is (simulated - measured) stored energy at the end of the period. The
	// dumb rule is a different strategy, so it ends somewhere else; that divergence is
	// the counterfactual doing its job, not an error, but it is also energy one world
	// holds and the other does not, and the cost comparison does not value it. A large
	// negative figure means W2 ended emptier than reality and so under-bought against a
	// stock-neutral comparison, i.e. the Control contribution beside it is a lower bound.
	FinalKWh float64 `json:"finalKWh"`
}

// note renders W2Drift into the chain's Notes (ADR-011 rule 7) - the numbers are
// in the payload either way, but the note is the field every caller already renders.
func (d W2Drift) note() string {
	return fmt.Sprintf("counterfactual battery: anchored to the measured charge once, at the period's first slot, then simulated - across %d gap(s) in the record it was handed the %+.2fkWh the real pack itself moved while unmeasured (unpriced in this world exactly as it is in what you paid), and it ends the period %+.2fkWh from the real pack, energy neither cost figure values",
		d.Gaps, d.CarriedKWh, d.FinalKWh)
}

// computeW2 simulates the "dumb rule" battery (charge from surplus only, discharge to
// house load only, never from grid) across slots.
//
// The simulated SoC is anchored to the measured SoC exactly once, at the first slot,
// and free-runs from there. It used to be re-anchored at every calendar-day boundary
// and at every gap in the otherwise-15-minute-contiguous run, on the argument that a
// free-running simulation is not evidence (ADR-011 rule 5). That argument is real but
// the cure was worse: each re-anchor is an unpriced energy injection. Whatever the real
// battery had accumulated while the ledger was not looking - and, worse, whatever the
// audited controller had achieved that the dumb rule had not - was credited to the
// counterfactual for free, and every free kWh is a kWh W2 never has to buy. On this
// site's own database, over 166 slots (2026-08-21 12:45 to 2026-08-24 10:45), the
// re-anchors injected 9.685kWh net - 24% of the period's whole load - and moved the
// Control contribution from -EUR 0.28 to -EUR 0.62. A calendar-day reset in particular
// has no defence at all: midnight is not a measurement event, and those two resets
// alone were worth EUR 0.036.
//
// The half of the old behaviour that WAS defensible is kept, in isolation: at a gap,
// the measured pack's own state change across the unmeasured stretch is carried onto
// the simulated SoC. Those slots are excluded from every world, including W3 - but W3
// is a meter reading, so it receives that energy anyway: its post-gap import is lower
// because the real pack was charged during hours nobody priced. Free-running W2 would
// receive none of it, which penalises the counterfactual and flatters the controller by
// exactly that amount (on the same 166 slots: 6.03kWh, moving Control to +EUR 0.48).
// Carrying the delta - rather than resetting to the measured level - gives W2 the same
// unmeasured movement W3 got while preserving the simulation's own divergence, which is
// the counterfactual's entire point.
//
// soc_temp is recorded at SLOT START (see meter.SocTemp), so the pack's measured state
// at the END of the last slot before a gap is that slot's start SoC plus its own
// measured charge/discharge - applied with the same one-way efficiencies
// simulateSlotStep uses, so this is the simulation's own physics rather than a new
// assumption. Bounding the carried state to [floor, capacity] is the same physical
// bound simulateSlotStep applies every slot, not a clamp on a reported figure, and
// whatever the bound absorbs is reflected in W2Drift.CarriedKWh rather than hidden.
func computeW2(slots []slotData, phys batteryPhysics) ([]worldFlow, W2Drift, error) {
	out := make([]worldFlow, len(slots))
	var drift W2Drift

	var socKWh, prevEndMeasured float64
	var started bool
	var prevStart time.Time

	for i, s := range slots {
		if s.BatterySocFrac == nil {
			return nil, W2Drift{}, ErrSocGap
		}
		measured := *s.BatterySocFrac * phys.CapacityKWh

		switch {
		case !started:
			socKWh, started = measured, true
		case !s.Start.Equal(prevStart.Add(tariff.SlotDuration)):
			drift.Gaps++
			carried := min(max(socKWh+measured-prevEndMeasured, phys.FloorFrac*phys.CapacityKWh), phys.CapacityKWh)
			drift.CarriedKWh += carried - socKWh
			socKWh = carried
		}

		newSoc, flow, _, _ := simulateSlotStep(batteryModeNormal, s.modelledLoadKWh(), s.PVKWh, socKWh, phys)
		socKWh = newSoc
		out[i] = flow
		prevStart = s.Start
		prevEndMeasured = measured + s.BatteryChargeKWh*phys.EtaC - s.BatteryDischargeKWh/phys.EtaD
	}

	if started {
		drift.FinalKWh = socKWh - prevEndMeasured
	}

	return out, drift, nil
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
	// MeterResidual is the A1 diagnostic (ledger_slots.go): the noise floor under
	// every euro figure above, published rather than left implicit - see its own doc
	// comment for why it is not expected to be zero.
	MeterResidual MeterResidual `json:"meterResidual"`
	// W2Drift is the counterfactual battery's energy bookkeeping - nil when the site
	// has no battery and W2 collapses to W1, where there is no simulation to account
	// for. See its own doc comment.
	W2Drift *W2Drift `json:"w2Drift,omitempty"`
	// Notes are caveats ADR-011 rule 7 says must be labelled in the payload, not left
	// to a code comment or an unwritten UI convention - which settlement figures a
	// PeriodAverage price actually reflects, what Routing/Timing do and don't include,
	// and what the euro figures do and don't cover against a real invoice.
	Notes []string `json:"notes,omitempty"`
}

// noteInvoiceComparability, always present: every figure in this payload prices only
// the grid/feed-in rate from the tariffs table. A standing charge, meter fee or VAT
// isn't included unless the site's tariff configuration bakes it in - and the whole
// point of this ledger is comparing against a real invoice.
const noteInvoiceComparability = "prices only the grid tariff rate (kWh); standing charges, meter fees and VAT are not included unless baked into the tariff configuration"

// noteRoutingIncludesLosses, present whenever Control is (a battery is configured):
// Routing isolates "which sink got the energy" by pricing both worlds at the flat
// period-average, which removes timing - but arbitrage (charging then discharging)
// changes total import/export volume by the round-trip conversion loss, so that loss
// is reported under Routing's volume effect even though it isn't a routing decision.
const noteRoutingIncludesLosses = "control.routing includes round-trip battery conversion losses (charge-then-discharge isn't lossless), not only which sink the energy went to"

// noteTimingSettlement, present whenever Control is: Timing is a genuine diagnostic
// under period-average settlement, but reflects money actually paid only where the
// site settles per-slot (Settled's own doc comment).
const noteTimingSettlement = "control.timing reflects real money only under per-slot settlement; under period-average billing it is a diagnostic, not a figure actually paid"

// noteSlotFlowDeltaIsSlotLocal, present whenever Control is: every decision's
// slotFlowDeltaEur (in the sibling decisions array) prices only the vetoed slot
// itself against the same starting SoC, not any later slot the decision would have
// affected - see DecisionRow.SlotFlowDeltaEUR's doc comment for a worked example
// where this reports the opposite sign from a true hindsight figure.
const noteSlotFlowDeltaIsSlotLocal = "decisions[].slotFlowDeltaEur prices only the vetoed slot itself, at its own starting SoC - a decision whose cost or benefit only materialises in a later slot can show the wrong sign here"

// noteEVTimingUnattributed, present whenever the site has a loadpoint: W0/W1/W2 all
// take slotData.modelledLoadKWh() - household plus every loadpoint's EV charging -
// at its REALISED timestamp, so shifting when a car charges (a smartCostLimit plan,
// a cheap-overnight window) produces exactly EUR 0 of attributed value in every
// contribution, no matter how much the timing actually saved. Arithmetically correct
// (ADR-011 rule 7 doesn't ask this package to invent a measure it can't honestly
// attribute) but silent about it without this note - see
// TestChainNotesEVTimingUnattributed.
const noteEVTimingUnattributed = "EV charge timing is not attributed to any measure - PV/Battery/Control all price a loadpoint's energy at when it was actually drawn, so shifting a charge to a cheaper slot shows EUR 0 of value here even when it saved money"

// noteFeedInZero, present whenever every slot in the period has PriceFeedIn == 0:
// every export line in this payload is then EUR 0.00 by construction, indistinguishable
// from a computation that silently lost every export unless this is said plainly.
const noteFeedInZero = "feed-in price is EUR 0.00 for every slot in this period, so every export credit in this payload is EUR 0.00 - that reflects the configured/observed feed-in rate, not a computation error"

// noteFeedInStaticFallback, present whenever at least one included slot's feed-in
// price came from the site's configured static tariff instead of the tariffs table:
// those slots would otherwise have been excluded entirely, so the coverage figure
// beside it depends on the substitution and a reader has to be able to see it. See
// feedInFallback for the guard that has to hold before this is allowed at all.
//
// slots/price must come from the SAME slot set as the figure the note is attached to.
// The chain and the realised cost build different sets (ComputeRealisedCost excludes
// the battery and loadpoint requirements, so it keeps slots the chain drops) and so
// have different fallback counts - on this site's own database, 416 imputed slots
// behind the realised figure against 86 behind the chain. Quoting one set's count
// beside the other's euros understates the imputation by 5x.
func noteFeedInStaticFallback(slots int, price float64) string {
	return fmt.Sprintf("no feed-in price was recorded for %d of the slots behind this figure; they were priced at the site's currently configured static feed-in rate of EUR %.4f/kWh - accepted only because that tariff declares its price time-invariant AND every feed-in price ever recorded by this site equals it, and those slots would have been excluded outright had any recorded price differed", slots, price)
}

// noteMeterResidual, always present: points a reader at meterResidual rather than
// leaving it to be found only by knowing the field exists.
const noteMeterResidual = "meterResidual is the measured gap between this period's sources and sinks - see its own doc comment for why it is not expected to be zero; its eurBand is that gap priced at the period's mean grid rate, and any figure above smaller than it is inside the noise, not a direction"

// allFeedInZero reports whether every slot's feed-in price is exactly 0 - see
// noteFeedInZero.
func allFeedInZero(slots []slotData) bool {
	if len(slots) == 0 {
		return false
	}
	for _, s := range slots {
		if s.PriceFeedIn != 0 {
			return false
		}
	}
	return true
}

// notePeriodAverageCoverage, present whenever coverage is incomplete: the period-
// average price is the mean over VALID slots only, and dropped slots are not assumed
// to distribute evenly across the day - if they cluster (e.g. an evening-peak outage),
// the period-average figures shift with coverage, not just with what actually happened.
func notePeriodAverageCoverage(c Coverage) string {
	return fmt.Sprintf("periodAverage prices are the mean over the %d valid slots only (%.1f%% coverage) - excluded slots are not assumed to average out evenly", c.ValidSlots, c.Fraction*100)
}

// ComputeChain runs the full ADR-011 world chain for [from,to). See buildLedgerSlots
// for what counts as a valid slot and ErrBeforeTariffStart/ErrSocGap for the two ways
// this refuses rather than fabricates.
func ComputeChain(ctx context.Context, from, to time.Time, feedInStatic *float64) (*Chain, error) {
	set, err := buildLedgerSlots(ctx, from, to, true, true, feedInStatic)
	if err != nil {
		return nil, err
	}

	return computeChainFromSlots(ctx, set)
}

// computeChainFromSlots is ComputeChain's body, taking an already-built slot set so
// ComputeLedger can share one buildLedgerSlots pass with the decision replay instead
// of paying for it twice (see that function's doc comment).
func computeChainFromSlots(ctx context.Context, set *ledgerSlotSet) (*Chain, error) {
	w0 := computeW0(set.Slots)
	w1 := computeW1(set.Slots)
	w3 := actualFlows(set.Slots)

	var w2 []worldFlow
	var phys *batteryPhysics
	var control *ControlSplit
	var drift *W2Drift

	if set.HasBattery {
		p, err := deriveBatteryPhysics(ctx)
		if err != nil {
			return nil, err
		}
		// a 0 rate ceiling from an empty sample list (see HasChargeEvidence's doc
		// comment) is not the same as a genuinely observed low rate - handing it to
		// computeW2 would silently make that direction inert, collapsing W2 towards
		// W1 and booking the battery's real value to Control with no refusal and no
		// note (see ErrBatteryRateCeilingUnavailable).
		if !p.HasChargeEvidence || !p.HasDischargeEvidence {
			return nil, fmt.Errorf("%w: charge evidence=%v, discharge evidence=%v", ErrBatteryRateCeilingUnavailable, p.HasChargeEvidence, p.HasDischargeEvidence)
		}
		phys = &p

		var d W2Drift
		w2, d, err = computeW2(set.Slots, p)
		if err != nil {
			return nil, err
		}
		drift = &d

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

	coverage := set.coverage()
	notes := []string{noteInvoiceComparability, noteMeterResidual}
	if control != nil {
		notes = append(notes, noteRoutingIncludesLosses, noteTimingSettlement, noteSlotFlowDeltaIsSlotLocal, phys.rateCeilingNote(), phys.floorNote(), drift.note())
	}
	if coverage.TotalSlots > 0 && coverage.ValidSlots < coverage.TotalSlots {
		notes = append(notes, notePeriodAverageCoverage(coverage))
	}
	if set.HasLoadpoint {
		notes = append(notes, noteEVTimingUnattributed)
	}
	if allFeedInZero(set.Slots) {
		notes = append(notes, noteFeedInZero)
	}
	if set.FeedInFallbackSlots > 0 {
		notes = append(notes, noteFeedInStaticFallback(set.FeedInFallbackSlots, set.FeedInFallbackPrice))
	}

	return &Chain{
		Worlds:         worlds,
		Contributions:  contributions,
		Coverage:       coverage,
		BatteryPhysics: phys,
		Control:        control,
		MeterResidual:  computeMeterResidual(set.Slots),
		W2Drift:        drift,
		Notes:          notes,
	}, nil
}
