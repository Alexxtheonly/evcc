package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/types"
	"github.com/evcc-io/evcc/hems/hems"
	"github.com/evcc-io/evcc/messenger"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util/config"
	"github.com/evcc-io/evcc/util/request"
	"github.com/evcc-io/evcc/util/sponsor"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/jinzhu/now"
	"github.com/samber/lo"
	"golang.org/x/exp/constraints"
)

const (
	// eta is the efficiency of the battery charging/discharging. Defined as
	// metrics.BatteryEta (core/metrics/ledger_worlds.go) rather than its own 0.9 so
	// the savings ledger's counterfactual battery physics and the live optimizer
	// can't silently drift onto different efficiency assumptions - a change here
	// now changes both, or fails to compile if metrics.BatteryEta goes away.
	eta = metrics.BatteryEta

	// batteryPower is the fallback charge/discharge power of a battery without a
	// api.BatteryPowerLimiter and without enough history for batteryPowerLimits to derive a
	// plausible limit from, in W.
	batteryPower = 6000

	// batteryPowerLookback is the trailing window of energy history batteryPowerLimits draws
	// its observed maximum from.
	batteryPowerLookback = 30 * 24 * time.Hour

	// batteryPowerMinSamples is the minimum number of qualifying (non-zero, non-recovered,
	// non-incomplete) 15min slots required before batteryPowerLimits trusts the observed
	// history over the batteryPower fallback. A newly added battery, or one that has barely
	// charged or discharged yet, stays on the fallback until it has a real track record.
	batteryPowerMinSamples = 20

	// batteryMaxCRate is the C-rate a sample must stay under to be believed: a slot
	// implying the battery moved more than this many times its own capacity in an hour did
	// not happen. Nothing between the meters table and here bounds a sample's magnitude -
	// BatteryPowerSamples filters recovered and incomplete rows, not implausible ones - so
	// without this a counter rollover, a briefly double-reporting meter or a downtime
	// backfill the recovered flag missed sets CMax/DMax for the whole batteryPowerLookback
	// window, at whatever magnitude the corruption happened to have.
	//
	// Deliberately generous: home batteries run 0.5-1C continuous and peak around 1-2C, and
	// a 15min slot average understates the peak further (see batteryPowerLimits), so this
	// bound never excludes a sample real hardware could have produced. It is a plausibility
	// filter, not a tuning knob - the only alternative discriminator would be the sample's
	// rank among its peers, and rank cannot tell a battery that hit 12kW once from a meter
	// that reported 120kW once, because those two histories have identical order statistics.
	batteryMaxCRate = 2
)

// optimizerChargingStrategies are the valid grid charging strategies; the first
// entry is the default and preserves the previous hard-coded behavior.
var optimizerChargingStrategies = []string{
	string(optimizer.OptimizerStrategyChargingStrategyChargeBeforeExport),
	string(optimizer.OptimizerStrategyChargingStrategyAttenuateDemandPeaks),
	string(optimizer.OptimizerStrategyChargingStrategyAttenuateFeedinPeaks),
	string(optimizer.OptimizerStrategyChargingStrategyAttenuateGridPeaks),
	string(optimizer.OptimizerStrategyChargingStrategyNone),
}

const defaultOptimizerChargingStrategy = string(optimizer.OptimizerStrategyChargingStrategyChargeBeforeExport)

// optimizerDecaySlots is the number of slots over which measured values decay into the forecast
const optimizerDecaySlots = 4

// optimizerResult wraps the optimizer publish payload to implement BytesMarshaler.
// This ensures publishComplex serializes it as a single JSON message instead of
// recursively decomposing each struct field and array element into individual MQTT
// topics (~1,500 messages per optimizer run).
type optimizerResult struct {
	Updated time.Time                    `json:"updated"`
	Req     optimizer.OptimizationInput  `json:"req"`
	Res     optimizer.OptimizationResult `json:"res"`
	Details requestDetails               `json:"details"`
}

var _ api.BytesMarshaler = (*optimizerResult)(nil)

func (r optimizerResult) MarshalBytes() ([]byte, error) {
	return json.Marshal(r)
}

type batteryType string

const (
	OPTIMIZER_URI = "https://optimizer.evcc.io"

	batteryTypeLoadpoint batteryType = "loadpoint"
	batteryTypeVehicle   batteryType = "vehicle"
	batteryTypeBattery   batteryType = "battery"
)

type batteryDetail struct {
	Type     batteryType `json:"type"`
	Title    string      `json:"title,omitempty"`
	Name     string      `json:"name,omitempty"`
	Capacity float64     `json:"capacity,omitempty"`

	loadpoint    *int // originating loadpoint id for loadpoint/vehicle entries
	controllable bool // device can act on suggestions
}

// batteryKey and loadpointKey build the canonical device keys used for
// suggestion routing and notifications
func batteryKey(name string) string { return "battery:" + name }
func loadpointKey(id int) string    { return fmt.Sprintf("loadpoint:%d", id) }

// key identifies the device across optimizer runs; an empty key means the
// device can't act on a suggestion.
func (d batteryDetail) key() string {
	switch {
	case d.Type == batteryTypeBattery:
		return batteryKey(d.Name)
	case d.loadpoint != nil:
		return loadpointKey(*d.loadpoint)
	default:
		return ""
	}
}

// currentAction returns the device's current operating mode for suggestion
// comparison. Must only be called for devices with a non-empty key.
func (d batteryDetail) currentAction(site *Site) string {
	if d.Type == batteryTypeBattery {
		return site.GetBatteryMode().String()
	}
	return loadpointCurrentAction(site.loadpoints[*d.loadpoint])
}

type batteryResult struct {
	batteryDetail
	Full  time.Time `json:"full,omitzero"`
	Empty time.Time `json:"empty,omitzero"`
}

// suggestionThreshold ignores numerical noise in power comparisons (W)
const suggestionThreshold = 50

// advisory actions for a loadpoint/vehicle slot; battery actions use api.BatteryMode
const (
	actionStop   = "stop"
	actionCharge = "charge"
)

// actionDischarge is the battery-to-grid discharge advisory. It has no matching
// api.BatteryMode, so it always reads as actionable.
const actionDischarge = "discharge"

// surplusCharge reports whether s is an active recommendation to charge by following pv
// surplus, as opposed to a forced grid-fed charge (full power, by definition, is always
// grid-fed - see optimizerCharging) or no charge suggestion at all. s.Grid close to zero
// means the plan expects the charge to be covered by surplus rather than drawing extra
// import, the same condition optimizerCharging uses to decide whether to defer to the pv
// control loop instead of driving the setpoint directly.
func surplusCharge(s *types.Suggestion, maxPower float64) bool {
	if s == nil || s.Action != actionCharge {
		return false
	}
	full := s.Charge >= maxPower-suggestionThreshold
	return !full && math.Abs(s.Grid) <= suggestionThreshold
}

// evSuggestion notifies when the optimizer's advisory action for a device changes
const evSuggestion = "suggestion"

// pendingSuggestion pairs a device's current-run suggestion with the
// notification event to emit if it represents an actionable change.
type pendingSuggestion struct {
	suggestion types.Suggestion
	event      messenger.Event
}

// suggestionEvent builds the notification event for a device suggestion
func suggestionEvent(detail batteryDetail, s types.Suggestion) messenger.Event {
	ev := messenger.Event{Event: evSuggestion, Attributes: map[string]any{
		"suggestionAction": s.Action,
		"suggestionTitle":  detail.Title,
	}}

	switch {
	case detail.Type == batteryTypeBattery:
		ev.Attributes["suggestionName"] = detail.Name
	case detail.loadpoint != nil:
		id := *detail.loadpoint
		ev.Loadpoint = &id
	}

	return ev
}

// socBoundEpsilon is the tolerance for treating a battery as sitting on one of
// its SoC bounds, in Wh relative to its capacity.
func socBoundEpsilon(cfg optimizer.BatteryConfig) float32 {
	return max(cfg.SCapacity*0.01, 10)
}

// effectivePriorityToCPriority maps a loadpoint's EffectivePriority - 0..10 in the UI
// (config.loadpoint.priorityLabel), unbounded if set directly in config - onto the optimizer's
// CPriority scale (0..2, "2 = highest priority"). CPriority only breaks ties between
// cost-equivalent solver choices (see the optimizer's preference objective), so a coarse
// three-way split is enough: the low third, including the common default of 0, keeps the
// previous unweighted behavior at 0; the top third (8..10, "highest" in the UI) gets the
// solver's top preference; everything in between lands in the middle.
func effectivePriorityToCPriority(priority int) int {
	return min(max(priority, 0), 10) * 3 / 11
}

// safeCPriority returns priority unchanged unless minImportPrice (the horizon's minimum
// import price, matching the solver's own min_import_price = np.min(time_series.p_N)) is
// negative, in which case it returns 0. The solver's CPriority preference term
// (optimizer.py:472-475) multiplies charge/discharge decisions by min_import_price *
// c_priority: with a non-negative minimum that is a reward that scales with priority as
// intended, but a single negatively-priced slot anywhere in the horizon flips the sign of
// the whole term, turning "prefer this battery" into "avoid this battery". The solver
// already works around the identical pitfall for the adjacent peak-leveling term by using
// penalty_base instead of min_import_price for exactly this reason (see the comment at
// optimizer.py:459-460); the priority term never got the same treatment because nothing ever
// set a non-zero CPriority before this.
func safeCPriority(priority int, minImportPrice float32) int {
	if minImportPrice < 0 {
		return 0
	}
	return priority
}

// terminalValueSafetyMargin keeps terminalStorageValue strictly below the horizon's
// cheapest price, so rounding between here and the solver can never make banking the
// terminal bonus as good as spending the energy in the horizon itself.
const terminalValueSafetyMargin = 0.99

// terminalStorageValue is the Wh value the solver assigns to energy still in a battery at
// the end of the horizon - BatteryConfig.PA, optimizer.py's bat.p_a. The solver adds
// s[-1]*p_a to the objective alongside real import cost and export revenue
// (optimizer.py:396-398), so p_a has to be a genuine currency/Wh estimate of what that
// energy goes on to be worth, not an incentive of its own.
//
// s is internal storage, not AC-side energy: the state transition (optimizer.py:617-631)
// applies eta_c on the way in and eta_d on the way out, so one internal Wh realizes eta_d
// AC-side Wh when it is eventually discharged. Its value is therefore eta*p, a
// multiplication, not p/eta - dividing prices replacement cost (what it took to put the Wh
// there), not realizable value (what it is worth coming back out). Capping the multiplier
// at the horizon's cheapest price, minImportPrice, values leftover energy at what it would
// have cost to top up right now, with a small safety margin so the solver never prefers
// banking the terminal bonus over spending the energy inside the horizon it can already
// see priced out slot by slot.
//
// Floored at zero: a negative minImportPrice (the grid paying to import) would otherwise
// send the value negative, turning stored energy into a liability the solver dumps to
// raise the objective. Zero makes leftover energy worth nothing in that case instead - the
// uninformative but safe answer when the horizon itself gives no evidence about what
// happens after it. This also matters for sites configured with s_min = 0: without the
// floor a negative terminal value would push the solver to drain the battery to empty
// purely to escape the penalty, which the optimizer's own openapi contract already rules
// out by declaring p_a's minimum as 0.
func terminalStorageValue(minImportPrice float32) float32 {
	return max(0, minImportPrice*eta*terminalValueSafetyMargin)
}

// currentSlotSuggestion maps the optimizer's first-slot corner result onto an advisory action.
// Because the optimization is linear, the first slot is at an operating-range extreme, so it
// maps cleanly onto the discrete battery mode / loadpoint intent that control would later apply.
// An idle battery is interpreted from the grid flow: importing means discharge is withheld
// (hold), exporting means charging is withheld (holdcharge). A battery idling on one of its
// SoC bounds is forced there by physics, not by choice — at SMin it cannot discharge and at
// SMax it cannot charge — so no hold/holdcharge intent is derived from it.
func currentSlotSuggestion(detail batteryDetail, cfg optimizer.BatteryConfig, res optimizer.BatteryResult, gridImport, gridExport float32, slotHours float64) types.Suggestion {
	if slotHours <= 0 || len(res.ChargingPower) == 0 || len(res.DischargingPower) == 0 {
		return types.Suggestion{}
	}

	charge := float64(res.ChargingPower[0]) / slotHours
	discharge := float64(res.DischargingPower[0]) / slotHours
	gridImporting := gridImport > 0
	gridExporting := gridExport > 0

	s := types.Suggestion{
		Charge:    charge,
		Discharge: discharge,
		Grid:      float64(gridImport-gridExport) / slotHours,
	}

	if detail.Type == batteryTypeBattery {
		idle := charge <= suggestionThreshold && discharge <= suggestionThreshold
		atMin := cfg.SInitial <= cfg.SMin+socBoundEpsilon(cfg)
		atMax := cfg.SInitial >= cfg.SMax-socBoundEpsilon(cfg)
		switch {
		case charge > suggestionThreshold && gridImporting:
			// charging while importing means grid charging
			s.Action = api.BatteryCharge.String()
		case idle && gridImporting && !atMin:
			// idle while importing: discharge is deliberately withheld
			s.Action = api.BatteryHold.String()
		case idle && gridExporting && !atMax:
			// idle while exporting: surplus is exported instead of charged
			s.Action = api.BatteryHoldCharge.String()
		case discharge > suggestionThreshold && gridExporting:
			// discharging while exporting means battery-to-grid discharge
			s.Action = actionDischarge
		default:
			s.Action = api.BatteryNormal.String()
		}
	} else if charge > suggestionThreshold {
		s.Action = actionCharge
	} else {
		s.Action = actionStop
	}

	return s
}

// optimizerBatteryModeConfirmDelay is the minimum age of a pending mode
// candidate before a later run may confirm it. Automatic mode runs the
// optimizer on every control-loop cycle (~30s, see core/site.go:1478's
// documented minimum interval) rather than once per tariff slot, so
// consecutive runs are not independent observations by themselves — a
// degenerate LP optimum on a flat price plateau can tip either way run to
// run. Two runs 5 min apart is now ~10 agreeing cycles, not the "a forced
// re-run seconds later" scenario the slot-cadence version guarded against.
const optimizerBatteryModeConfirmDelay = 5 * time.Minute

// optimizerBatteryModeValidity bounds how long a pending candidate may wait
// for a confirming run before being treated as a fresh observation, and
// doubles as the staleness bound for the applied mode once runs stop landing
// altogether (the optimizer HTTP client alone times out after 90s, see
// optimizerUpdateAsync). It must exceed optimizerBatteryModeConfirmDelay to
// leave a confirmation window at all; one minute (~2 cycles) is enough
// headroom since a run is attempted every cycle.
const optimizerBatteryModeValidity = optimizerBatteryModeConfirmDelay + time.Minute

// optimizerBatteryModeDisagreementLimit bounds how long an applied active mode
// may stay unconfirmed while runs keep deriving something else before it is
// de-escalated to normal instead of being frozen by the confirmation logic.
// At slot cadence this was 40 min (2x a 20 min validity); at cycle cadence
// that would tolerate ~24 disagreeing runs for over ten minutes on a mode that
// may be actively charging or holding the battery, so it scales down with the
// new (much tighter) validity instead of keeping the old absolute value.
const optimizerBatteryModeDisagreementLimit = 2 * optimizerBatteryModeValidity

// optimizerChargePriceTolerance (currency/kWh) absorbs float jitter when
// comparing the live rate against the price a charge decision was based on;
// it is not an economic margin.
const optimizerChargePriceTolerance = 0.001

// chargePaybackBuffer (currency/kWh) is the forecast-risk margin a planned
// grid charge must clear beyond the round-trip losses. Prices are exact
// day-ahead; the risk priced here is the load/PV forecast error that strands
// the energy.
const chargePaybackBuffer = 0.03

// optimizerVetoReason explains why the applied optimizer battery mode is not
// (fully) in effect. It is UI annotation only and never feeds back into
// control decisions — those are already made by the time a reason is derived.
type optimizerVetoReason string

const (
	vetoReasonNone       optimizerVetoReason = ""
	vetoReasonPayback    optimizerVetoReason = "payback"    // a grid charge would not pay back its round-trip losses plus margin
	vetoReasonForcedIdle optimizerVetoReason = "forcedIdle" // controllable batteries derived conflicting actions, so none was applied
	vetoReasonDamping    optimizerVetoReason = "damping"    // a mode change is pending confirmation by a later run
	vetoReasonLiveRate   optimizerVetoReason = "liveRate"   // the live rate has moved past the price the decision was based on
)

// optimizerDecision is the vetted outcome of one optimizer run.
type optimizerDecision struct {
	// mode is the applyable candidate that setOptimizerBatteryMode's state
	// machine acts on - api.BatteryUnknown when vetoed (vetoReasonPayback) or
	// batteries disagree (vetoReasonForcedIdle), so a rejected suggestion is
	// never actually applied.
	mode api.BatteryMode

	// suggestedMode is what the optimizer derived this run, independent of
	// any veto - equal to mode except for vetoReasonPayback/vetoReasonForcedIdle,
	// where mode is forced to api.BatteryUnknown but suggestedMode still
	// carries the real candidate (F4). This is what control_slots.SuggestedMode
	// records: showing "charge" alongside veto_reason "payback" is the whole
	// point of persisting both the suggestion and why it was rejected.
	suggestedMode api.BatteryMode

	chargeVetoed bool                // a grid charge suggestion was declined by a gate
	vetoReason   optimizerVetoReason // reason for chargeVetoed, or for mode staying at api.BatteryUnknown
	price        float64             // price (currency/kWh) underlying an accepted charge decision (mode == api.BatteryCharge with no veto)
}

// gridChargeJustified checks that grid-charging the battery now is worth it: a
// later slot must feed-in (or avoid import) at a price that covers the
// round-trip loss of charging now and discharging later.
func gridChargeJustified(pn []float32) bool {
	if len(pn) < 2 {
		return false
	}

	// pn is currency/Wh
	price := float64(pn[0]) * 1e3

	// energy at zero or negative cost is always worth storing; the spread test
	// below would divide a negative price and lower the bar instead of raising it
	if price <= 0 {
		return true
	}

	return float64(lo.Max(pn[1:]))*1e3 >= price/(eta*eta)
}

// chargePaybackJustified checks that the plan pays a slot-0 grid charge back at
// a price covering the round-trip losses plus a risk margin. Charging/discharging
// energies and SoC are AC-side plan values from the optimizer result; the
// discharge-weighted price up to the first return to the initial SoC is the
// price at which the plan actually gives the energy charged now back.
func chargePaybackJustified(pn, soc, discharge []float32, sInitial float32) bool {
	if len(pn) < 2 {
		return false
	}

	// pn is currency/Wh
	price := float64(pn[0]) * 1e3
	if price <= 0 {
		return true
	}

	n := min(len(pn), len(soc), len(discharge))

	var value, energy float64
	for t := 1; t < n; t++ {
		value += float64(discharge[t]) * float64(pn[t]) * 1e3
		energy += float64(discharge[t])

		if soc[t] <= sInitial {
			// the energy charged now has been given back
			if energy == 0 {
				return false
			}
			return value/energy >= (price+chargePaybackBuffer)/(eta*eta)
		}
	}

	// the plan never returns to the initial SoC within the horizon: the charged
	// energy's value rests on the terminal value alone, which is always below the
	// buy price - not worth paying for now. This depends on terminalStorageValue
	// staying a fraction of minImportPrice (a multiplication, not a division by eta);
	// see its doc comment for why the inverse formula breaks this invariant.
	return false
}

// batteryModeCandidate maps the suggestions from the current optimizer run
// onto the raw (undamped) battery-mode decision across all controllable home
// batteries: they must agree (details is index-aligned with req.Batteries/
// res.Batteries, see applyOptimizerResult), and a Charge suggestion is
// additionally gated on the plan being worth it (gridChargeJustified) and
// every controllable battery's plan paying itself back (chargePaybackJustified).
func batteryModeCandidate(suggestions map[string]types.Suggestion, req optimizer.OptimizationInput, res optimizer.OptimizationResult, details []batteryDetail) optimizerDecision {
	mode := api.BatteryUnknown

	for _, detail := range details {
		if detail.Type != batteryTypeBattery || !detail.controllable {
			continue
		}

		s, ok := suggestions[detail.key()]
		if !ok {
			continue
		}

		m, err := api.BatteryModeString(s.Action)
		if err != nil {
			// discharging to grid has no matching battery mode
			m = api.BatteryNormal
		}

		if mode != api.BatteryUnknown && m != mode {
			// batteries disagree: don't act, but keep the first-encountered
			// candidate as suggestedMode (F4) - not authoritative since they
			// disagreed, but still a real, reachable value instead of always
			// "unknown"
			return optimizerDecision{suggestedMode: mode, vetoReason: vetoReasonForcedIdle}
		}
		mode = m
	}

	if mode != api.BatteryCharge {
		return optimizerDecision{mode: mode, suggestedMode: mode}
	}

	pn := req.TimeSeries.PN

	if !gridChargeJustified(pn) {
		return optimizerDecision{suggestedMode: mode, chargeVetoed: true, vetoReason: vetoReasonPayback}
	}

	// every controllable home battery's plan must pay the charge back
	for i, detail := range details {
		if detail.Type != batteryTypeBattery || !detail.controllable {
			continue
		}
		if i >= len(req.Batteries) || i >= len(res.Batteries) {
			return optimizerDecision{suggestedMode: mode, chargeVetoed: true, vetoReason: vetoReasonPayback}
		}
		if !chargePaybackJustified(pn, res.Batteries[i].StateOfCharge, res.Batteries[i].DischargingPower, req.Batteries[i].SInitial) {
			return optimizerDecision{suggestedMode: mode, chargeVetoed: true, vetoReason: vetoReasonPayback}
		}
	}

	// pn is currency/Wh
	return optimizerDecision{mode: mode, suggestedMode: mode, price: float64(pn[0]) * 1e3}
}

// setOptimizerBatteryMode stores the damped battery-mode decision derived
// from the latest optimizer run. A new active mode (including a switch
// between two active modes) only takes effect once two runs at least
// optimizerBatteryModeConfirmDelay apart agree, so a degenerate LP optimum on
// a flat price plateau cannot flap the battery every cycle. Reverting to
// Unknown — automatic disabled, no suggestion, or disagreeing batteries — is
// never delayed. Automatic disabled only ever affects the applyable mode:
// the vetted suggestion behind it is still recorded (control_slots, ADR-011)
// so the gap between what the optimizer would have done and what was
// actually applied is auditable from before automatic mode was ever turned on.
//
// Must be called at most once per optimizer run (from applyOptimizerResult),
// not at control-loop cadence: batterySuggestionMode reads the result far
// more often than that and must not re-trigger this logic, or every read
// would look like a fresh, independent observation and the confirmation
// delay would never bind.
//
// d.price (currency/kWh) is the price the candidate was based on when
// d.mode is api.BatteryCharge; batterySuggestionMode re-validates it against
// the live rate on every read, since a stale charge decision costs money in a
// way a stale hold does not and must not wait out the generic staleness window.
func (site *Site) setOptimizerBatteryMode(d optimizerDecision) {
	site.Lock()
	defer site.Unlock()

	candidate := d.mode
	now := time.Now()

	apply := func(mode api.BatteryMode) {
		site.optimizerBatteryMode = mode
		site.optimizerBatteryModeConfirmedAt = now
		site.optimizerBatteryModePending = api.BatteryUnknown
		site.optimizerChargePrice = d.price
	}

	site.optimizerBatteryModeUpdated = now
	site.optimizerChargeVetoed = d.chargeVetoed
	site.optimizerVetoReason = d.vetoReason
	site.optimizerSuggestedMode = d.suggestedMode

	switch {
	case !site.Automatic():
		// automatic mode may have been disabled between deriving and storing
		// the candidate; re-check the flag inside the critical section. Only
		// the applyable mode is forced to Unknown here - chargeVetoed,
		// vetoReason and suggestedMode were already set above from d and are
		// left alone: they are UI/ledger annotation only (see their doc
		// comments), the vetted candidate was genuinely derived this run
		// whether or not automatic mode is on to act on it, and
		// persistControlSlot needs it to compare what the optimizer would
		// have done against what was actually applied even while advisory.
		apply(api.BatteryUnknown)
	case candidate == api.BatteryUnknown:
		apply(api.BatteryUnknown)
	case candidate == site.optimizerBatteryMode:
		apply(candidate)
	case candidate == site.optimizerBatteryModePending:
		if age := now.Sub(site.optimizerBatteryModePendingSince); age >= optimizerBatteryModeConfirmDelay && age <= optimizerBatteryModeValidity {
			apply(candidate)
		} else if age > optimizerBatteryModeValidity {
			// candidate went stale before confirming: treat as a fresh observation
			site.optimizerBatteryModePendingSince = now
			if candidate == api.BatteryCharge {
				site.optimizerVetoReason = vetoReasonDamping
			}
		} else if candidate == api.BatteryCharge {
			site.optimizerVetoReason = vetoReasonDamping
		}
	default:
		site.optimizerBatteryModePending = candidate
		site.optimizerBatteryModePendingSince = now
		if candidate == api.BatteryCharge {
			site.optimizerVetoReason = vetoReasonDamping
		}
	}

	// persistent disagreement: runs keep deriving something else than the
	// applied active mode. De-escalate instead of freezing — a frozen charge
	// mode would keep buying energy on the strength of an arbitrarily old
	// observation.
	if site.optimizerBatteryMode != api.BatteryUnknown && site.optimizerBatteryMode != api.BatteryNormal &&
		!site.optimizerBatteryModeConfirmedAt.IsZero() &&
		now.Sub(site.optimizerBatteryModeConfirmedAt) > optimizerBatteryModeDisagreementLimit {
		apply(api.BatteryNormal)
	}

	site.publishOptimizerDecisionLocked()
}

// optimizerDecisionPublish is the wire format of the vetted optimizer decision,
// published for the forecast view's slot-0 annotation. Kept separate from the
// internal optimizerDecision so the payload shape does not follow it.
type optimizerDecisionPublish struct {
	Mode         api.BatteryMode     `json:"mode"`
	ChargeVetoed bool                `json:"chargeVetoed"`
	VetoReason   optimizerVetoReason `json:"vetoReason,omitempty"`
	Price        float64             `json:"price,omitempty"`
	Updated      time.Time           `json:"updated"`
	ValidFor     int64               `json:"validFor"` // ms Updated stays current, mirrors optimizerBatteryModeValidity
}

// publishOptimizerDecisionLocked publishes the current optimizer decision.
// Caller must already hold site.Lock or site.RLock.
func (site *Site) publishOptimizerDecisionLocked() {
	site.publish(keys.OptimizerDecision, optimizerDecisionPublish{
		Mode:         site.optimizerBatteryMode,
		ChargeVetoed: site.optimizerChargeVetoed,
		VetoReason:   site.optimizerVetoReason,
		Price:        site.optimizerChargePrice,
		Updated:      site.optimizerBatteryModeUpdated,
		ValidFor:     optimizerBatteryModeValidity.Milliseconds(),
	})
}

// appliedBatteryMode is site.GetBatteryMode() translated into the mode the
// battery was actually running in, for the ledger only. The two differ for
// api.BatteryUnknown, which at site level does not mean "we don't know": it
// is the control loop's word for "evcc holds no override", the state a site
// stays in from boot until something first calls SetBatteryMode.
// requiredBatteryMode returns it to mean "no change required" and
// batteryModeModified() already classifies it together with
// api.BatteryNormal; the UI (BatteryStatusCard.statusState) likewise never
// renders it, falling through to the raw power reading exactly as it does
// for normal, and simulateSlotStep's default branch already replays it with
// normal physics. Recording the string "unknown" made every row of a site
// that simply never needs an override - advisory mode, no grid-charge limit,
// no smart-cost limit - read as if the ledger had failed to observe
// something, and made AppliedMode differ from an identical SuggestedMode,
// which DecisionDeltas then priced as a veto worth 0 EUR.
//
// Not a fabrication: with a battery present, "no override in effect" IS
// normal operation, which is what every other consumer in the codebase
// already takes it to be. With no battery configured at all there is no
// mode to report and api.BatteryUnknown is kept - requiredBatteryMode's own
// !batteryConfigured() branch returns exactly that. Deliberately confined to
// this persistence path: site.batteryMode itself keeps meaning what the
// control loop needs it to mean.
func (site *Site) appliedBatteryMode() api.BatteryMode {
	if mode := site.GetBatteryMode(); mode != api.BatteryUnknown || !site.batteryConfigured() {
		return mode
	}
	return api.BatteryNormal
}

// persistControlSlot stores one completed 15min control decision (ADR-011):
// the optimizer's vetted suggestion versus the battery mode actually applied,
// and why they differ. Driven by the update loop like persistTariffs, with
// the same slot-boundary gate - automatic mode runs the optimizer far more
// often than once per slot (site.go's update loop calls this every cycle,
// ~30s), so the gate is what bounds the table to ~96 rows/day rather than
// the control loop's own frequency.
//
// AppliedMode is a point sample taken on the first tick to observe the new
// slot - a few seconds into it, not an end-of-slot summary - the same
// forward-looking convention persistTariffs uses (Timestamp is the slot's
// start). The applied mode can still change again before the slot ends
// (confirm delay is 5min; HEMS dimming and external control can also
// intervene), so every later tick within the same slot compares
// GetBatteryMode() against that first sample and, the first time it has
// diverged, flags the row via MarkControlSlotModeChanged. A per-change row
// was considered and rejected: mode changes are rare by design (the
// damping/confirm-delay machinery exists specifically to prevent flapping),
// so the boolean marker costs nothing in the common case, while a
// per-change row would break the 1-row-per-slot join the rest of the
// ledger design assumes, for a case that barely occurs.
//
// Called after updateBatteryMode so GetBatteryMode() reflects this cycle's
// applied decision. GetBatteryMode() takes site.RLock() itself, so it is
// read before this function's own RLock section rather than inside it -
// site.RWMutex is not reentrant, and nesting the two would deadlock against
// a writer arriving between them.
func (site *Site) persistControlSlot() {
	slot := time.Now().Truncate(tariff.SlotDuration)
	applied := site.appliedBatteryMode()

	if !slot.After(site.controlSlot) {
		// still the already-recorded slot: only watch for AppliedMode having
		// diverged from the row's first sample, nothing new to insert
		if !site.controlSlot.IsZero() && !site.controlSlotChanged && applied != site.controlSlotBaseline {
			site.controlSlotChanged = true
			if err := metrics.MarkControlSlotModeChanged(site.controlSlot); err != nil {
				site.log.ERROR.Printf("mark control slot mode change: %v", err)
			}
		}
		return
	}

	// entering a new slot: skip the partial boot slot, which the process
	// didn't observe from the start and so has no meaningful baseline
	first := site.controlSlot.IsZero()
	site.controlSlot = slot
	site.controlSlotBaseline = applied
	site.controlSlotChanged = false
	if first {
		return
	}

	site.RLock()
	suggested := site.optimizerSuggestedMode
	veto := site.optimizerVetoReason
	healthOk := site.optimizerHealthOk
	price := site.optimizerChargePrice
	site.RUnlock()

	// price only means something alongside an actually accepted charge
	// decision - see optimizerChargePrice's own doc comment. suggested can
	// now be api.BatteryCharge while vetoed (F4), and optimizerChargePrice
	// is not meaningfully updated for a vetoed run, so the veto must be
	// checked here too or a payback-vetoed slot would show a stale/zero
	// price as if it had been the accepted decision's basis.
	var p *float64
	if suggested == api.BatteryCharge && veto == vetoReasonNone {
		p = &price
	}

	// api.BatteryUnknown is not a suggestion - it is what clearSuggestions() leaves
	// behind after a failed run, and what a site with no controllable battery
	// produces. Recording its "unknown" spelling made those indistinguishable from
	// a deliberate decision; nil says "no run produced one" (see
	// metrics.controlSlot.SuggestedMode).
	var sm *string
	if suggested != api.BatteryUnknown {
		s := suggested.String()
		sm = &s
	}

	if err := metrics.PersistControlSlot(slot, applied.String(), sm, string(veto), healthOk, p); err != nil {
		site.log.ERROR.Printf("persist control slot: %v", err)
	}
}

// optimizerHealthReason explains why the optimizer is not currently producing
// results. It is independent of optimizerVetoReason, which explains why a
// result it did produce is not (fully) applied.
type optimizerHealthReason string

const (
	optimizerHealthReasonNone          optimizerHealthReason = ""
	optimizerHealthReasonNotSponsored  optimizerHealthReason = "notSponsored"  // sponsorship required to use the optimizer
	optimizerHealthReasonDisabled      optimizerHealthReason = "disabled"      // experimental or optimizer setting is off
	optimizerHealthReasonNotConfigured optimizerHealthReason = "notConfigured" // no battery, vehicle or loadpoint for it to act on
	optimizerHealthReasonNoTariff      optimizerHealthReason = "noTariff"      // not enough forecast data to plan a horizon
	optimizerHealthReasonError         optimizerHealthReason = "error"         // the run itself failed, e.g. the solver rejected the request
)

// optimizerHealthPublish is the wire format of the optimizer's operational
// status - whether it is producing results at all, independent of what it
// decided. Requested so a strategy relying on the optimizer can detect a
// silently dead optimizer (e.g. after prices stop arriving) and fall back
// instead of continuing to act on a stale plan.
type optimizerHealthPublish struct {
	Ok      bool                  `json:"ok"`
	Reason  optimizerHealthReason `json:"reason,omitempty"`
	Updated time.Time             `json:"updated,omitzero"`
}

// publishOptimizerHealth publishes the outcome of a completed run attempt and
// reports whether it actually changed the published state. For the one
// steady-state reason a repeat of the same {ok, reason}
// is a no-op - Updated does not advance and nothing is published, mirroring
// publishOptimizerHealthGate. For every other reason the call always publishes
// and Updated always advances to now. Called from optimizerUpdateAsync's
// deferred handler for every outcome except errOptimizerNotReady, which leaves
// the previous status in place for a silent retry on the next cycle.
func (site *Site) publishOptimizerHealth(ok bool, reason optimizerHealthReason) bool {
	site.Lock()
	changed := site.optimizerHealthOk != ok || site.optimizerHealthReason != reason

	// notConfigured describes a persistent configuration state rather than a genuine
	// result of this particular run - the same category publishOptimizerHealthGate
	// already dedupes for notSponsored/disabled. In automatic mode optimizerUpdateAsync
	// runs on every loadpoint cycle, so without dedup a site with no
	// battery/vehicle/loadpoint configured yet republishes "not configured" - and
	// re-clears suggestions - forever. Genuine run results (success, solver errors,
	// missing tariff) are deliberately not deduped, so Updated keeps advancing on every
	// real attempt.
	if reason == optimizerHealthReasonNotConfigured && !changed {
		site.Unlock()
		return false
	}

	site.optimizerHealthOk = ok
	site.optimizerHealthReason = reason
	site.optimizerHealthUpdated = time.Now()
	updated := site.optimizerHealthUpdated
	site.Unlock()

	site.publish(keys.OptimizerHealth, optimizerHealthPublish{Ok: ok, Reason: reason, Updated: updated})
	return changed
}

// publishOptimizerHealthGate publishes why the optimizer isn't even attempting
// to run (not sponsored, disabled) without touching the last-run timestamp -
// no run was attempted. These gates are re-checked on every control cycle, so
// the publish is skipped once the reason stops changing to avoid republishing
// identical, information-free state continuously for the common case of an
// optimizer that is simply not in use.
func (site *Site) publishOptimizerHealthGate(reason optimizerHealthReason) {
	site.Lock()
	changed := site.optimizerHealthOk || site.optimizerHealthReason != reason
	site.optimizerHealthOk = false
	site.optimizerHealthReason = reason
	updated := site.optimizerHealthUpdated
	site.Unlock()

	if !changed {
		return
	}

	site.publish(keys.OptimizerHealth, optimizerHealthPublish{Ok: false, Reason: reason, Updated: updated})
}

// liveRateVetoLocked reports whether rate has moved past the price the active
// charge decision was based on. Caller must already hold site.RLock or
// site.Lock. Unlike the fork this ports from, there is no grid-charge-limit
// check: GetBatteryGridChargeLimit() is structurally nil while Automatic() is
// true (see core/site_api.go), so under automatic mode that check is dead.
func (site *Site) liveRateVetoLocked(rate api.Rate) bool {
	fresh := time.Since(site.optimizerBatteryModeUpdated) <= optimizerBatteryModeValidity
	active := site.optimizerBatteryMode == api.BatteryCharge && fresh

	return active && rate.Value > site.optimizerChargePrice+optimizerChargePriceTolerance
}

// updateOptimizerLiveRateVeto refreshes the live-rate-guard annotation for the
// forecast view. batterySuggestionMode already drops a stale charge decision
// from control when the live rate diverges from the price it was based on;
// this only makes that fact visible, it never changes what is applied.
//
// Called every control cycle, where most of the time there is no optimizer, no
// change of mind, or automatic mode is off — all a no-op. An RLock fast path
// avoids taking the write lock for those; the write path re-checks under
// site.Lock since state may have changed between the two.
func (site *Site) updateOptimizerLiveRateVeto(rate api.Rate) {
	site.RLock()
	veto := site.liveRateVetoLocked(rate)
	noop := (veto && site.optimizerVetoReason == vetoReasonLiveRate) ||
		(!veto && site.optimizerVetoReason != vetoReasonLiveRate)
	site.RUnlock()

	if noop {
		return
	}

	site.Lock()
	defer site.Unlock()

	veto = site.liveRateVetoLocked(rate)

	switch {
	case veto && site.optimizerVetoReason != vetoReasonLiveRate:
		site.optimizerVetoReason = vetoReasonLiveRate
	case !veto && site.optimizerVetoReason == vetoReasonLiveRate:
		site.optimizerVetoReason = vetoReasonNone
	default:
		return
	}

	site.publishOptimizerDecisionLocked()
}

// ResetOptimizerBatteryMode clears the damped battery-mode decision and any
// pending candidate. Call when automatic mode is toggled so a stale
// pre-toggle observation cannot confirm a mode once the setting flips back.
func (site *Site) ResetOptimizerBatteryMode() {
	site.Lock()
	defer site.Unlock()

	site.optimizerBatteryMode = api.BatteryUnknown
	site.optimizerBatteryModeUpdated = time.Time{}
	site.optimizerBatteryModeConfirmedAt = time.Time{}
	site.optimizerBatteryModePending = api.BatteryUnknown
	site.optimizerBatteryModePendingSince = time.Time{}
	site.optimizerChargePrice = 0
	site.optimizerChargeVetoed = false
	site.optimizerVetoReason = vetoReasonNone
	site.optimizerSuggestedMode = api.BatteryUnknown

	site.publishOptimizerDecisionLocked()
}

// loadpointCurrentAction returns the loadpoint's current operating mode for
// suggestion comparison, reusing chargeGoalReached so a loadpoint left
// enabled while idle (e.g. vehicle finished at its limit) is treated as
// stopped instead of triggering a spurious pause suggestion.
func loadpointCurrentAction(lp *Loadpoint) string {
	lp.RLock()
	enabled := lp.enabled
	lp.RUnlock()

	if enabled && !lp.chargeGoalReached(enabled) {
		return actionCharge
	}
	return actionStop
}

// suggestionMaxAge invalidates suggestions of a stalled optimizer. Runs happen
// once per loadpoint update cycle, so two slots without a result mean the
// optimizer is no longer keeping up.
const suggestionMaxAge = 2 * tariff.SlotDuration

// setSuggestions replaces the suggestions applied on each publish
func (site *Site) setSuggestions(suggestions map[string]types.Suggestion) {
	site.Lock()
	defer site.Unlock()

	site.suggestions = suggestions
	site.suggestionsUpdated = time.Now()
}

// setBatteryForecast replaces the battery forecast of the cached state
func (site *Site) setBatteryForecast(forecast *types.BatteryForecast) {
	site.Lock()
	defer site.Unlock()

	site.battery.Forecast = forecast
}

// suggestion returns the optimizer suggestion for the given device key.
// The actionable flag is evaluated on read against the device's current
// action since that changes between optimizer runs.
func (site *Site) suggestion(key, currentAction string) *types.Suggestion {
	site.RLock()
	s, ok := site.suggestions[key]
	stale := time.Since(site.suggestionsUpdated) > suggestionMaxAge
	site.RUnlock()

	if !ok || stale {
		return nil
	}

	s.Actionable = s.Action != currentAction

	return &s
}

// publishSuggestions publishes the loadpoints' suggestions and hands them to the
// loadpoints, where they act as start/stop gate while the optimizer is in control
func (site *Site) publishSuggestions() {
	for id, lp := range site.loadpoints {
		if lp == nil {
			continue
		}

		s := site.suggestion(loadpointKey(id), loadpointCurrentAction(lp))

		var val any
		if s != nil {
			val = *s
		}
		site.publishLoadpoint(id, keys.Suggestion, val)

		lp.setSuggestion(s)
	}
}

// clearSuggestions removes all suggestions and the battery forecast when the
// optimizer result is stale
func (site *Site) clearSuggestions() {
	site.setSuggestions(nil)
	site.setBatteryForecast(nil)
	site.setOptimizerBatteryMode(optimizerDecision{})

	// F6: the diagnostics of the last successful run must not linger with no
	// staleness marker once a run fails - newOptimizerDiagnosticsPublish is
	// only ever published from applyOptimizerResult (i.e. on Optimal/Feasible),
	// so nothing else clears it. Publishing nil, the same way an absent
	// suggestion already reads on the wire (see publishSuggestions), is
	// unambiguous: no current diagnostics, rather than stale numbers with no
	// indication they are stale.
	site.publish(keys.OptimizerDiagnostics, nil)

	site.publishBattery()
	site.publishSuggestions()

	site.Lock()
	site.suggestionActions = nil
	site.Unlock()
}

// pendingSuggestions collects the stored suggestions with their actionable flag
// evaluated against the devices' current operating mode
func (site *Site) pendingSuggestions(details []batteryDetail) map[string]pendingSuggestion {
	pending := make(map[string]pendingSuggestion, len(details))

	for _, detail := range details {
		key := detail.key()
		if key == "" {
			continue
		}

		s := site.suggestion(key, detail.currentAction(site))
		if s == nil {
			continue
		}

		pending[key] = pendingSuggestion{suggestion: *s, event: suggestionEvent(detail, *s)}
	}

	return pending
}

// diffSuggestions updates the tracked actionable optimizer suggestions and
// returns the events to send for devices whose actionable action changed since
// the last run. Non-actionable or vanished devices are pruned so a later
// actionable change re-notifies.
func (site *Site) diffSuggestions(pending map[string]pendingSuggestion) []messenger.Event {
	site.Lock()
	defer site.Unlock()

	if site.suggestionActions == nil {
		site.suggestionActions = make(map[string]string)
	}

	// prune devices that are gone or no longer actionable
	for key := range site.suggestionActions {
		if p, ok := pending[key]; !ok || !p.suggestion.Actionable {
			delete(site.suggestionActions, key)
		}
	}

	var events []messenger.Event
	for key, p := range pending {
		if !p.suggestion.Actionable || site.suggestionActions[key] == p.suggestion.Action {
			continue
		}
		site.suggestionActions[key] = p.suggestion.Action
		events = append(events, p.event)
	}
	return events
}

type requestDetails struct {
	Timestamps     []time.Time     `json:"timestamp"`
	BatteryDetails []batteryDetail `json:"batteryDetails"`
}

// optimizerBattery pairs a battery request entry with its device detail
type optimizerBattery struct {
	cfg    optimizer.BatteryConfig
	detail batteryDetail
}

func optimizerURI() string {
	return cmp.Or(os.Getenv("OPTIMIZER_URI"), OPTIMIZER_URI)
}

const slotsPerHour = float64(time.Hour / tariff.SlotDuration)

// errOptimizerNotReady means battery measurements aren't available yet (e.g. at
// startup); the slot gate is left open so the next cycle retries.
var errOptimizerNotReady = errors.New("battery measurements not ready")

// errOptimizerNotConfigured means there is no battery, vehicle or loadpoint
// for the optimizer to act on - a legitimate idle state, not a failure.
var errOptimizerNotConfigured = errors.New("no batteries configured for optimization")

// errOptimizerNoTariff means too few forecast slots are available to plan a
// meaningful horizon, typically because a required tariff is missing or its
// data hasn't arrived yet.
var errOptimizerNoTariff = errors.New("not enough forecast slots for meaningful optimization")

// optimizerUpdateAsync runs the optimizer. In automatic mode it runs on every
// loadpoint cycle since the loadpoint gate needs a fresh result, while advisory
// suggestions only need one run per slot. Pass force to run regardless, e.g.
// when a changed setting should take effect immediately. It is a no-op when the
// optimizer is not active or a run is already in progress; the running update
// reflects the change on its next run.
func (site *Site) optimizerUpdateAsync(force bool) {
	if !sponsor.IsAuthorized() {
		site.publishOptimizerHealthGate(optimizerHealthReasonNotSponsored)
		return
	}

	if !optimizerEnabled() {
		site.publishOptimizerHealthGate(optimizerHealthReasonDisabled)
		return
	}

	if !site.optimizerMu.TryLock() {
		return
	}
	defer site.optimizerMu.Unlock()

	if force {
		// keep the gate open so a not-ready run is retried on the next cycle
		site.optimizerUpdated = time.Time{}
	} else if !site.Automatic() && time.Since(site.optimizerUpdated) < tariff.SlotDuration {
		return
	}

	var err error

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic %v", r)
		}

		// not ready yet: keep the gate open for an immediate retry next cycle
		if errors.Is(err, errOptimizerNotReady) {
			return
		}

		site.optimizerUpdated = time.Now()

		switch {
		case err == nil:
			site.publishOptimizerHealth(true, optimizerHealthReasonNone)
		case errors.Is(err, errOptimizerNotConfigured):
			// only clear on the transition into "not configured" - repeat runs
			// while nothing is configured must not keep re-clearing and
			// re-publishing suggestions/battery/mode that are already cleared
			if site.publishOptimizerHealth(false, optimizerHealthReasonNotConfigured) {
				site.clearSuggestions()
			}
		default:
			site.log.ERROR.Println("optimizer:", err)

			// stale advice must not linger
			site.clearSuggestions()

			reason := optimizerHealthReasonError
			if errors.Is(err, errOptimizerNoTariff) {
				reason = optimizerHealthReasonNoTariff
			}
			site.publishOptimizerHealth(false, reason)
		}
	}()

	err = site.optimizerUpdate(site.state().battery.Devices)
}

// optimizerRequest assembles the optimizer request and the matching device
// details from tariffs, home profile, loadpoints and battery meters
func (site *Site) optimizerRequest(battery []types.Measurement) (optimizer.OptimizationInput, requestDetails, error) {
	var req optimizer.OptimizationInput
	var details requestDetails

	solarTariff := site.GetTariff(api.TariffUsageSolar)
	solar := currentRates(solarTariff)

	grid := currentRates(site.GetTariff(api.TariffUsageGrid))
	feedIn := currentRates(site.GetTariff(api.TariffUsageFeedIn))

	minLen := lo.Min([]int{len(grid), len(feedIn)})
	// exclude empty solar forecast from minLen
	if solarTariff != nil && len(solar) > 0 {
		minLen = min(minLen, len(solar))
	}

	if optimizerURI() == OPTIMIZER_URI {
		minLen = slotsUntil(grid, optimizerHorizon(time.Now()), minLen)
	}

	if expectedSlots := 8; minLen < expectedSlots {
		if solarTariff != nil {
			return req, details, fmt.Errorf("%w: %d < %d (grid=%d, feedIn=%d, solar=%d)", errOptimizerNoTariff, minLen, expectedSlots, len(grid), len(feedIn), len(solar))
		}
		return req, details, fmt.Errorf("%w: %d < %d (grid=%d, feedIn=%d)", errOptimizerNoTariff, minLen, expectedSlots, len(grid), len(feedIn))
	}

	now := time.Now()
	dt := timeSteps(minLen, now)
	firstSlotDuration := time.Duration(dt[0]) * time.Second

	site.log.DEBUG.Printf("optimizer: optimizing %d slots until %v: grid=%d, feedIn=%d, solar=%d, first slot: %v",
		minLen,
		grid[minLen-1].End.Local(),
		len(grid), len(feedIn), len(solar),
		firstSlotDuration,
	)

	gt, err := site.homeProfile(minLen)
	if err != nil {
		return req, details, err
	}

	// blend measured energy of the last metrics slot into the first slots
	if v := site.measuredSlotEnergy(metrics.Home); v > 0 {
		orig := slices.Clone(gt[:min(optimizerDecaySlots, len(gt))])
		blendMeasured(gt, v, optimizerDecaySlots)
		site.log.DEBUG.Printf("optimizer: home slots updated with measured %.0fWh: %.0f -> %.0f", v, orig, gt[:len(orig)])
	}

	// allow empty solar forecast
	ft := lo.RepeatBy(minLen, func(i int) float32 { return float32(0) })
	if solarTariff != nil && len(solar) > 0 {
		solarEnergy, err := solarRatesToEnergy(solar)
		if err != nil {
			return req, details, err
		}

		scale := site.effectiveSolarScale()
		scaleAt := site.effectiveSolarScaleAt(scale)
		ftSlots := scaleAndPruneByLead(solarEnergy, now, scaleAt, minLen)

		// decay the scale derived from measured vs forecasted energy of the last
		// completed slot. fcstRaw is deliberately NOT pre-multiplied by scale here -
		// blendScaleByLead below evaluates the per-slot ratio pv/(fcstRaw*scaleAt(lead))
		// using each target slot's OWN lead, the same scale ftSlots[i] was built with
		// (see B31: a single flat ratio computed with one scale, applied to slots
		// built with a per-lead scale, silently mixes the two for every slot but the
		// one whose lead happens to match).
		if pv, fcstRaw := site.measuredSlotEnergy(site.Meters.PVMetersRef...), site.measuredSlotEnergy(metrics.Forecast); pv > 0 && fcstRaw > 0 {
			orig := slices.Clone(ftSlots[:min(optimizerDecaySlots, len(ftSlots))])
			blendScaleByLead(ftSlots, solarEnergy, now, func(lead time.Duration) float64 {
				s := scaleAt(lead)
				if s == 0 {
					return 1 // degenerate scale, no correction rather than a division by zero
				}
				return pv / (fcstRaw * s)
			}, optimizerDecaySlots)
			site.log.DEBUG.Printf("optimizer: pv slots updated with measured %.0fWh vs forecast %.0fWh: %.0f -> %.0f", pv, fcstRaw, orig, ftSlots[:len(orig)])
		}
		ft = prorate(ftSlots, firstSlotDuration)
	}

	req = optimizer.OptimizationInput{
		Strategy: optimizer.OptimizerStrategy{
			ChargingStrategy:    optimizer.OptimizerStrategyChargingStrategy(site.GetOptimizerChargingStrategy()),
			DischargingStrategy: optimizer.OptimizerStrategyDischargingStrategyDischargeBeforeImport,
		},
		EtaC: eta,
		EtaD: eta,
		TimeSeries: optimizer.TimeSeries{
			Dt: dt,
			Gt: prorate(gt, firstSlotDuration),
			Ft: ft,
			PN: scaleAndPrune(grid, 0.001, minLen),
			PE: scaleAndPrune(feedIn, 0.001, minLen),
		},
	}

	// minImportPrice is the same value the solver itself derives as min_import_price
	// (optimizer.py: self.min_import_price = np.min(self.time_series.p_N)) - used below both
	// for the end-of-horizon Wh value and to guard CPriority against inverting on a negative
	// price (see safeCPriority).
	minImportPrice := lo.Min(req.TimeSeries.PN)

	// end of horizon Wh value
	pa := terminalStorageValue(minImportPrice)

	details = requestDetails{
		Timestamps: asTimestamps(dt, now),
	}

	if site.circuit != nil {
		if pMaxImp := site.circuit.GetMaxPower(); pMaxImp > 0 {
			// hard grid import limit if no price penalty is set by PrcPExcImp.
			//
			// evcc never sets Grid.PrcPExcImp today (B4) - do not start without
			// restoring the guard below first. Setting it DISABLES the solver's normal
			// per-Wh overshoot penalty (optimizer.py's prc_e_grid_imp_pen, gated off at
			// optimizer.py:431 whenever prc_p_exc_imp is set), so import overshoot gets
			// CHEAPER, not more expensive - measured 22468.3Wh of overshoot with
			// PrcPExcImp set vs 7485.5Wh without it, on the identical request. If a
			// future change sets PrcPExcImp (e.g. a real demand-charge tariff), it must
			// also keep prc_e_grid_imp_pen active, not silently swap one penalty out
			// for a weaker one.
			req.Grid.PMaxImp = float32(pMaxImp)
		}
	}

	// static grid export limit configured in the UI: export is capped at this
	// power, excess PV is curtailed instead of exported
	if limit := site.GetGridExportLimit(); limit > 0 {
		req.Grid.PMaxExp = float32(limit)
	}

	// soft grid feed-in cap from active HEMS curtailment (e.g. German 70% rule)
	// wins over the static limit while active
	if curtailed := hems.Curtailed(site.hems); curtailed != nil && *curtailed {
		if pMaxExp := site.hems.MaxProductionPower(); pMaxExp != nil {
			req.Grid.PMaxExp = float32(*pMaxExp)
		}
	}

	var batteries []optimizerBattery

	// uncontrollable power of loadpoints that cannot be modelled as storage
	var unmodelled float64

	for id, lp := range site.ActiveLoadpoints() {
		// ignore disconnected loadpoints, including StatusNone
		if s := lp.GetStatus(); s != api.StatusB && s != api.StatusC {
			continue
		}

		v := lp.GetVehicle()

		// no vehicle capacity and no session energy limit to model against:
		// account for the consumption as uncontrollable load
		if v == nil || (v.Capacity() == 0 && lp.GetLimitEnergy() == 0) {
			unmodelled += unmodelledPower(lp)
			continue
		}

		// skip disabled loadpoints
		if cfg, detail := site.loadpointRequest(lp, minLen, firstSlotDuration, grid, minImportPrice); cfg.CMax > 0 {
			detail.loadpoint = &id
			batteries = append(batteries, optimizerBattery{cfg, detail})
		}
	}

	// home profile subtracts all loadpoint power, so unmodelled loadpoints would
	// leave the optimizer planning against surplus that is already consumed. Their
	// forecast is zero, so the measured power only decays into the near slots -
	// without a capacity there is no fill point to assert it any further.
	if unmodelled > 0 {
		load := make([]float64, minLen)
		blendMeasured(load, unmodelled/slotsPerHour, optimizerDecaySlots)

		site.log.DEBUG.Printf("optimizer: home slots updated with unmodelled %.0fW loadpoint load: %.0f", unmodelled, load[:min(optimizerDecaySlots, len(load))])

		for i, v := range prorate(load, firstSlotDuration) {
			req.TimeSeries.Gt[i] += v
		}
	}

	for i, dev := range site.batteryMeters {
		// measurements may lag the configured meters on an off-cycle trigger
		if i >= len(battery) {
			break
		}
		b := battery[i]

		if b.Capacity == nil || *b.Capacity == 0 || b.Soc == nil {
			continue
		}

		cfg, detail := site.batteryRequest(dev, b, grid, minLen, firstSlotDuration)
		batteries = append(batteries, optimizerBattery{cfg, detail})
	}

	for _, b := range batteries {
		b.cfg.PA = pa
		req.Batteries = append(req.Batteries, b.cfg)
		details.BatteryDetails = append(details.BatteryDetails, b.detail)
	}

	return req, details, nil
}

func (site *Site) optimizerUpdate(battery []types.Measurement) error {
	req, details, err := site.optimizerRequest(battery)
	if err != nil {
		return err
	}

	if len(req.Batteries) == 0 {
		// meters configured but measurements not in yet: retry instead of
		// consuming the slot gate
		if len(site.batteryMeters) > 0 {
			return errOptimizerNotReady
		}
		return errOptimizerNotConfigured
	}

	// create the api client once and reuse it across runs to keep connections alive
	if site.optimizerClient == nil {
		httpClient := request.NewClient(site.log)
		httpClient.Timeout = 90 * time.Second

		apiClient, err := optimizer.NewClientWithResponses(optimizerURI(), optimizer.WithHTTPClient(httpClient))
		if err != nil {
			return err
		}

		site.optimizerClient = apiClient
	}

	resp, err := site.optimizerClient.PostOptimizeChargeScheduleWithResponse(context.TODO(), req, func(_ context.Context, req *http.Request) error {
		if sponsor.IsAuthorized() {
			req.Header.Set("Authorization", "Bearer "+sponsor.Token)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if resp.StatusCode() != http.StatusOK {
		return apiError(resp)
	}

	// publish before the status check so the optimizer page stays available
	// for diagnosing non-optimal results
	site.publish("evopt", optimizerResult{
		Updated: time.Now(),
		Req:     req,
		Res:     *resp.JSON200,
		Details: details,
	})

	// diagnostic record of the run itself (ADR-011), independent of whether
	// the result was usable - an Infeasible run is exactly the kind of thing
	// this table exists to make visible after the fact
	site.persistOptimizerRun(string(resp.JSON200.Status), *resp.JSON200)

	// feasible results are usable, they are just not proven optimal
	if status := resp.JSON200.Status; status != optimizer.Optimal && status != optimizer.Feasible {
		return errors.New(string(status))
	}

	site.applyOptimizerResult(req, details.BatteryDetails, *resp.JSON200)

	return nil
}

// applyOptimizerResult maps the optimizer response onto suggestions, battery
// forecast and notifications
func (site *Site) applyOptimizerResult(req optimizer.OptimizationInput, details []batteryDetail, res optimizer.OptimizationResult) {
	slotHours := (time.Duration(req.TimeSeries.Dt[0]) * time.Second).Hours()
	var gridImport, gridExport float32
	if len(res.GridImport) > 0 {
		gridImport = res.GridImport[0]
	}
	if len(res.GridExport) > 0 {
		gridExport = res.GridExport[0]
	}

	var batteries []batteryResult
	suggestions := make(map[string]types.Suggestion, len(req.Batteries))

	for i, batReq := range req.Batteries {
		batRes := res.Batteries[i]
		detail := details[i]

		batteries = append(batteries, batteryResult{
			batteryDetail: detail,
			Full: matchSoc(batRes.StateOfCharge, func(soc float32) bool {
				return soc >= batReq.SMax
			}),
			// empty means the battery is actually drained, not merely at its configured
			// minSoc floor - SMin is a soft target the solver can plan below (see
			// site_optimizer.go's loadpointRequest), so a vehicle sitting under its minSoc
			// while plugged in and not yet charged back up is not "empty"
			Empty: matchSoc(batRes.StateOfCharge, func(soc float32) bool {
				return soc <= 0
			}),
		})

		suggestion := currentSlotSuggestion(detail, batReq, batRes, gridImport, gridExport, slotHours)
		if suggestion.Action == "" {
			continue
		}

		// uncontrollable devices can't act on a suggestion
		if key := detail.key(); key != "" && detail.controllable {
			suggestions[key] = suggestion
		}
	}

	site.publish("evopt-batteries", batteries)
	site.publish(keys.OptimizerDiagnostics, newOptimizerDiagnosticsPublish(res))

	site.setSuggestions(suggestions)
	site.setBatteryForecast(site.addBatteryForecastTotals(req.Batteries, res.Batteries))

	// derive and damp the battery mode to apply from the home battery
	// suggestions; setOptimizerBatteryMode re-checks Automatic() under lock
	site.setOptimizerBatteryMode(batteryModeCandidate(suggestions, req, res, details))

	site.publishBattery()

	// publish for all loadpoints so suggestions of dropped-out loadpoints clear
	site.publishSuggestions()

	// notify on actionable suggestion changes (advisory only, see #31903)
	for _, ev := range site.diffSuggestions(site.pendingSuggestions(details)) {
		site.pushEvent(ev)
	}
}

// optimizerLimitViolations is the wire format of optimizer.LimitViolationResult, renamed to
// the site's camelCase publish convention (see optimizerDecisionPublish).
type optimizerLimitViolations struct {
	GridImportLimitExceeded bool `json:"gridImportLimitExceeded"`
	GridExportLimitHit      bool `json:"gridExportLimitHit"`
}

// optimizerDiagnosticsPublish is the wire format of the last optimizer run's economic and
// constraint diagnostics - the cheapest input to a "is the optimizer earning anything" ledger,
// and the place a GridConfig.PMaxImp overshoot becomes visible: the solver prices it as a
// per-slot penalty rather than a hard constraint, so it shows up here instead of silently
// vanishing into a discarded Infeasible result. Kept separate from the raw evopt payload (see
// optimizerResult) so consumers don't need the optimizer's snake_case wire format.
type optimizerDiagnosticsPublish struct {
	// ObjectiveValue is the run's economic benefit in the site's configured currency.
	ObjectiveValue float64 `json:"objectiveValue"`
	// GridImportOvershoot is the total energy imported above PMaxImp across the horizon (Wh).
	GridImportOvershoot float64 `json:"gridImportOvershoot"`
	// GridExportOvershoot is the total energy curtailed above PMaxExp across the horizon (Wh).
	GridExportOvershoot float64                  `json:"gridExportOvershoot"`
	LimitViolations     optimizerLimitViolations `json:"limitViolations"`
}

func newOptimizerDiagnosticsPublish(res optimizer.OptimizationResult) optimizerDiagnosticsPublish {
	return optimizerDiagnosticsPublish{
		ObjectiveValue:      float64(res.ObjectiveValue),
		GridImportOvershoot: float64(lo.Sum(res.GridImportOvershoot)),
		GridExportOvershoot: float64(lo.Sum(res.GridExportOvershoot)),
		LimitViolations: optimizerLimitViolations{
			GridImportLimitExceeded: res.LimitViolations.GridImportLimitExceeded,
			GridExportLimitHit:      res.LimitViolations.GridExportLimitHit,
		},
	}
}

// persistOptimizerRun stores the diagnostic outcome of one optimizer run for
// ADR-011. Automatic mode calls the optimizer roughly once per control-loop
// cycle (~30s, ~30 runs per 15min slot), so a sampled Optimal/Feasible run is
// gated to the same 15min slot boundary as persistTariffs and
// persistControlSlot - that is the happy path, and one representative sample
// per slot is enough. Every other status (Infeasible, solver error, timeout)
// bypasses the gate entirely and is always recorded at its own real
// timestamp: those are exactly the failures this table exists to catch, and
// sampling them the same way as the happy path would mean a solver going
// Infeasible for ten minutes leaves no trace whenever the slot's first run
// happened to succeed.
//
// Only ever called from optimizerUpdate, itself only reachable while the
// caller (optimizerUpdateAsync) already holds site.optimizerMu for the whole
// run - so optimizerRunSlot, like optimizerUpdated beside it, is read and
// written here without taking the lock again; optimizerMu is a plain
// sync.Mutex and re-locking it on the same goroutine would deadlock. Only the
// sampled branch touches optimizerRunSlot, so interleaved failure rows never
// disturb the slot gate for the next sampled success.
func (site *Site) persistOptimizerRun(status string, res optimizer.OptimizationResult) {
	now := time.Now()
	// Optimal and Feasible are the statuses that produced an actual schedule. The
	// optimizer client's wire format zero-values ObjectiveValue and the overshoot slices
	// for any other status - see optimizer.OptimizationResult.ObjectiveValue's doc
	// comment ("present for Optimal and Feasible, null otherwise") - so for any other
	// status those fields would read as a false "no overshoot" instead of "no schedule
	// to measure" if persisted as-is.
	st := optimizer.OptimizationResultStatus(status)
	sampled := st == optimizer.Optimal || st == optimizer.Feasible

	ts := now

	if sampled {
		slot := now.Truncate(tariff.SlotDuration)

		last := site.optimizerRunSlot
		site.optimizerRunSlot = slot

		// skip repeat ticks within the slot and the partial boot slot
		if last.IsZero() || !slot.After(last) {
			return
		}

		ts = slot
	}

	var objective, importOvershoot, exportOvershoot *float64
	var importLimitExceeded, exportLimitHit *bool

	if sampled {
		objective = lo.ToPtr(float64(res.ObjectiveValue))
		importOvershoot = lo.ToPtr(float64(lo.Sum(res.GridImportOvershoot)))
		exportOvershoot = lo.ToPtr(float64(lo.Sum(res.GridExportOvershoot)))
		importLimitExceeded = lo.ToPtr(res.LimitViolations.GridImportLimitExceeded)
		exportLimitHit = lo.ToPtr(res.LimitViolations.GridExportLimitHit)
	}

	if err := metrics.PersistOptimizerRun(ts, status, objective, importOvershoot, exportOvershoot, importLimitExceeded, exportLimitHit, sampled); err != nil {
		site.log.ERROR.Printf("persist optimizer run: %v", err)
	}
}

func (site *Site) addBatteryForecastTotals(req []optimizer.BatteryConfig, resp []optimizer.BatteryResult) *types.BatteryForecast {
	if len(resp) == 0 || len(resp[0].StateOfCharge) == 0 {
		return nil
	}

	high, low := batteryForecastSocExtremes(req, resp)
	if high == nil && low == nil {
		return nil
	}

	cutoff := time.Now()
	now := cutoff.Round(tariff.SlotDuration)
	point := func(p *batteryForecastSlot) *types.BatteryForecastPoint {
		if p == nil {
			return nil
		}
		ts := now.Add(time.Duration(p.slot) * tariff.SlotDuration)
		if !ts.After(cutoff) {
			return nil
		}
		return &types.BatteryForecastPoint{Soc: p.soc, Time: ts, Limit: p.limit}
	}

	res := types.BatteryForecast{
		Highest: point(high),
		Lowest:  point(low),
	}
	if res.Highest == nil && res.Lowest == nil {
		return nil
	}
	return &res
}

type batteryForecastSlot struct {
	slot  int
	soc   float64 // percent
	limit bool    // true when SMax (highest) or SMin (lowest) boundary reached
}

// batteryForecastSocExtremes returns the highest and lowest aggregate SOC
// points across home batteries (SCapacity > 0) over the forecast horizon.
// The Limit flag indicates whether the SOC reached the configured SMax (for
// the highest point) or SMin (for the lowest point) boundary - in which case
// the battery is forecasted to become fully charged or empty.
// Returns nil for either point when no home battery is present or when the
// battery already is at the respective limit.
func batteryForecastSocExtremes(req []optimizer.BatteryConfig, resp []optimizer.BatteryResult) (*batteryForecastSlot, *batteryForecastSlot) {
	homeIndices := lo.FilterMap(req, func(b optimizer.BatteryConfig, i int) (int, bool) {
		return i, b.SCapacity > 0
	})
	if len(homeIndices) == 0 || len(resp) == 0 {
		return nil, nil
	}

	totalCapacity := lo.SumBy(homeIndices, func(i int) float32 { return req[i].SCapacity })
	totalSMax := lo.SumBy(homeIndices, func(i int) float32 { return req[i].SMax })
	totalSMin := lo.SumBy(homeIndices, func(i int) float32 { return req[i].SMin })

	var high, low *batteryForecastSlot
	for i := range resp[homeIndices[0]].StateOfCharge {
		sum := lo.SumBy(homeIndices, func(idx int) float32 { return resp[idx].StateOfCharge[i] })
		soc := float64(sum/totalCapacity) * 100
		fullReached := totalSMax > 0 && sum >= totalSMax
		emptyReached := sum <= totalSMin

		// first slot at SMax wins for highest
		if high == nil || (!high.limit && (soc > high.soc || fullReached)) {
			high = &batteryForecastSlot{slot: i, soc: soc, limit: fullReached}
		}
		// first slot at SMin wins for lowest
		if low == nil || (!low.limit && (soc < low.soc || emptyReached)) {
			low = &batteryForecastSlot{slot: i, soc: soc, limit: emptyReached}
		}
	}

	// battery is already at the limit - announcing it will become full/empty is pointless
	if high != nil && high.limit && high.slot == 0 {
		high = nil
	}
	if low != nil && low.limit && low.slot == 0 {
		low = nil
	}

	return high, low
}

// batteryWillRefillToday reports whether the optimizer's own forecast (see
// batteryForecastSocExtremes, addBatteryForecastTotals) confidently shows the home
// battery reaching its configured SMax (fully charged) at or before the end of the
// day containing asOf. "Confidently" means the forecast actually reached the
// limit, not just trended upward - Highest.Limit is only set once the simulated
// SOC hits SMax, so a forecast that stays below it (e.g. an overcast day) reports
// false here even though Highest is still populated.
//
// A nil forecast - no optimizer run yet, not sponsored, or the forecast was
// cleared as stale (see clearSuggestions) - reports false, which is exactly the
// "fall back to configured static behaviour" case callers need.
//
// Highest.Time must still be ahead of asOf: the forecast is computed once per
// optimizer run and can be stale by up to a run interval, so a peak that
// already passed is a disproven prediction, not a pending one - without this
// check, a relaxation granted while the peak was still ahead would keep
// applying after the battery demonstrably did not refill as forecast.
func batteryWillRefillToday(forecast *types.BatteryForecast, asOf time.Time) bool {
	if forecast == nil || forecast.Highest == nil || !forecast.Highest.Limit {
		return false
	}
	return forecast.Highest.Time.After(asOf) && !forecast.Highest.Time.After(now.With(asOf).EndOfDay())
}

func (site *Site) loadpointRequest(lp loadpoint.API, minLen int, firstSlotDuration time.Duration, grid api.Rates, minImportPrice float32) (optimizer.BatteryConfig, batteryDetail) {
	bat := optimizer.BatteryConfig{
		ChargeFromGrid: true,
		CMin:           float32(lp.EffectiveMinPower()),
		CMax:           float32(lp.EffectiveMaxPower()),
		DMax:           0,
		CPriority:      safeCPriority(effectivePriorityToCPriority(lp.EffectivePriority()), minImportPrice),
		// PA:             pa,
	}

	if profile := loadpointProfile(lp, minLen); profile != nil {
		bat.PDemand = prorate(profile, firstSlotDuration)
	}

	detail := batteryDetail{
		Type:         batteryTypeLoadpoint,
		Title:        lp.GetTitle(),
		controllable: true,
	}

	// vehicle
	v := lp.GetVehicle()

	capacity := v.Capacity() // kWh
	soc := lp.GetSoc()       // percent

	// without capacity or soc there is no battery state to model, but a session energy
	// limit still bounds the charge- use charged energy as state (see remainingLimitEnergy)
	if limit := lp.GetLimitEnergy(); limit > 0 && (capacity == 0 || soc == 0) {
		bat.SInitial = float32(lp.GetChargedEnergy())    // Wh
		bat.SMax = max(bat.SInitial, float32(limit*1e3)) // prevent infeasible if limit already exceeded
	} else {
		minSoc := capacity * float64(lp.EffectiveMinSoc()) * 10   // Wh
		maxSoc := capacity * float64(lp.EffectiveLimitSoc()) * 10 // Wh
		bat.SInitial = float32(capacity * soc * 10)               // Wh
		// unclamped: s_min is a soft penalty (optimizer.py s_min_pen), not a hard bound, so a
		// current soc below minSoc is feasible - the solver forces charging toward minSoc the
		// same way loadpoint.minSocNotReached does, instead of the clamp silently zeroing the
		// penalty and letting the model ignore the floor entirely (see 587a3bdc6)
		bat.SMin = float32(minSoc)
		bat.SMax = max(bat.SInitial, float32(maxSoc)) // prevent infeasible if current soc above maximum
	}

	detail.Type = batteryTypeVehicle
	detail.Capacity = capacity

	if vt := v.GetTitle(); vt != "" {
		if detail.Title != "" {
			detail.Title += " (" + vt + ")"
		} else {
			detail.Title = vt
		}
	}

	// find vehicle name/id
	for _, dev := range config.Vehicles().Devices() {
		if dev.Instance() == v {
			detail.Name = dev.Config().Name
		}
	}

	var demand []float32

	switch lp.GetMode() {
	case api.ModeOff:
		// disable charging
		bat.CMax = 0

	case api.ModeNow:
		// forced max charging
		demand = continuousDemand(lp, minLen)

	case api.ModeMinPV:
		// forced min charging
		demand = continuousDemand(lp, minLen)
		// add smartcost limit, precondition and plan goal, if configured
		demand = applySmartCostLimit(lp, demand, grid, minLen)
		demand = applyPrecondition(lp, demand, minLen)
		site.applyPlanGoal(lp, &bat, minLen)

	case api.ModePV:
		// add smartcost limit, precondition and plan goal, if configured
		demand = applySmartCostLimit(lp, nil, grid, minLen)
		demand = applyPrecondition(lp, demand, minLen)
		site.applyPlanGoal(lp, &bat, minLen)
	}

	if demand != nil {
		// after prorate, so the shortened first slot counts with the energy it really carries
		bat.PDemand = clearDemandWhenFull(prorate(demand, firstSlotDuration), bat.SMax-bat.SInitial)
	}

	return bat, detail
}

// clearDemandWhenFull zeroes the charge demand from the slot the accumulated energy fills the
// vehicle. The optimizer drops the demand at s_max anyway, but pays two binaries per slot to
// detect it, so slots that cannot bind are worth not asking about. Losses are accounted for.
//
// The cut assumes the demand is met every slot. A grid import limit can throttle charging below
// it, moving the real fill point later than the estimate - the next request corrects that from
// the measured soc, and the near slots are never affected because the cut sits a full charge away.
func clearDemandWhenFull(demand []float32, headroom float32) []float32 {
	res := slices.Clone(demand)

	var acc float32
	for i, d := range res {
		if acc >= headroom {
			res[i] = 0
			continue
		}
		acc += d * eta
	}

	return res
}

// batteryPowerLimits derives plausible charge/discharge power limits (W) for a battery meter
// that does not implement api.BatteryPowerLimiter, from the meter's own observed energy
// history instead of the flat batteryPower constant every such battery was otherwise stuck
// with regardless of its real size - every "can the battery absorb this cheap hour" answer
// scales linearly with this value.
//
// Each sample is one 15min slot's average power (kWh observed in the slot / slot duration),
// which systematically understates true sustained capability: a battery doing 6kW for 4
// minutes then idling for the remaining 11 contributes a sample of only 1.6kW, because the
// slot boundary, not the battery, decides how long the burst gets averaged over. Deriving a
// limit below the fallback from this data is worse than not deriving one at all - a
// too-low limit makes the optimizer schedule fewer, shorter hard charges/discharges, which
// then keeps producing exactly the low-power samples that justify the low limit next time
// (self-reinforcing). So history here can only ever raise the fallback, never lower it: take a
// high percentile (batteryPowerPercentile) of the observed sustained slot power per direction,
// once there is enough of it (batteryPowerMinSamples), and use batteryPower as a floor under
// that, not a starting point to average around. A battery that has demonstrably sustained more
// than the default gets credit for it; one that has only ever trickled keeps the default
// instead of being pinned below it.
//
// Samples are screened for physical plausibility against the battery's own capacity before
// the maximum is taken (batteryMaxCRate), so one corrupt meters row cannot set the limit for
// a month. What that does NOT cover is a corruption that lands inside the plausible range: a
// meter double-reporting at 2x a 0.5C battery's real power reads as 1C and is believed, for
// as long as it lasts. Screening by rank instead - a high percentile, or dropping the top
// few samples - does not fix that case either and costs real capability, since a battery
// that genuinely reaches its peak in only a few slots per month has those slots discarded.
//
// The failure this bounds is a wrong plan, not wrong hardware behaviour: CMax/DMax shape
// what the solver schedules and what the savings ledger counterfactual assumes, but the
// control path to a home battery is api.BatteryController, i.e. SetBatteryMode with a mode
// enum. There is no power setpoint here to over-drive an inverter with. A battery that does
// know its own limits reports them through api.BatteryPowerLimiter, which overrides this
// derivation entirely (see batteryRequest).
func (site *Site) batteryPowerLimits(name string, capacity float64) (chargeLimit, dischargeLimit float64) {
	chargeLimit, dischargeLimit = batteryPower, batteryPower

	c, ok := site.collectors[name]
	if !ok {
		return
	}

	charge, discharge, err := c.BatteryPowerSamples(time.Now().Add(-batteryPowerLookback))
	if err != nil {
		site.log.ERROR.Printf("battery power limits: %v", err)
		return
	}

	// kWh observed in one 15min slot -> average W sustained over that slot
	if len(charge) >= batteryPowerMinSamples {
		chargeLimit = max(chargeLimit, plausibleMax(charge, capacity)*1e3*slotsPerHour)
	}
	if len(discharge) >= batteryPowerMinSamples {
		dischargeLimit = max(dischargeLimit, plausibleMax(discharge, capacity)*1e3*slotsPerHour)
	}

	return chargeLimit, dischargeLimit
}

// plausibleMax returns the largest per-slot energy (kWh) in samples that a battery of
// capacity (kWh) could actually have moved, or 0 when none of them could. An unknown
// capacity (<= 0) leaves nothing to judge against, so the plain maximum is returned.
func plausibleMax(samples []float64, capacity float64) float64 {
	ceiling := capacity * batteryMaxCRate / slotsPerHour // kWh movable in one slot at the C-rate bound

	var res float64
	for _, v := range samples {
		if capacity > 0 && v > ceiling {
			continue
		}
		res = max(res, v)
	}

	return res
}

func (site *Site) batteryRequest(dev config.Device[api.Meter], b types.Measurement, grid api.Rates, minLen int, firstSlotDuration time.Duration) (optimizer.BatteryConfig, batteryDetail) {
	chargeLimit, dischargeLimit := site.batteryPowerLimits(dev.Config().Name, *b.Capacity)

	bat := optimizer.BatteryConfig{
		CMax:      float32(chargeLimit),
		DMax:      float32(dischargeLimit),
		SCapacity: float32(*b.Capacity * 1e3),         // Wh
		SInitial:  float32(*b.Capacity * *b.Soc * 10), // Wh
		// CPriority stays at its zero value. The solver's preference term
		// (optimizer.py:472-475) rewards both charging and discharging a battery, so a
		// positive CPriority means "cycle this battery more, and sooner" - for an EV
		// (DMax = 0) the discharge half is inert and it does read as "fill this one
		// first", but for a home battery it would buy cost-neutral extra cycling, i.e.
		// wear with no benefit.
		// PA:       pa,
	}

	instance := dev.Instance()

	controllable := api.HasCap[api.BatteryController](instance)
	if controllable {
		bat.ChargeFromGrid = true
		bat.DischargeToGrid = site.GetBatteryGridDischarge()
	}

	if m, ok := api.Cap[api.BatteryPowerLimiter](instance); ok {
		charge, discharge := m.GetPowerLimits()
		bat.CMax = float32(charge)
		bat.DMax = float32(discharge)
	}

	if m, ok := api.Cap[api.BatterySocLimiter](instance); ok {
		minSoc, maxSoc := m.GetSocLimits()
		if maxSoc == 0 {
			maxSoc = 100 // empty/unset maxsoc means no upper limit
		}
		// clamp against current soc to prevent infeasible if it is outside the configured limits
		bat.SMin = min(bat.SInitial, float32(*b.Capacity*minSoc*10)) // Wh
		bat.SMax = max(bat.SInitial, float32(*b.Capacity*maxSoc*10)) // Wh
	}

	detail := batteryDetail{
		Type:         batteryTypeBattery,
		Name:         dev.Config().Name,
		Title:        deviceProperties(dev).Title,
		Capacity:     *b.Capacity,
		controllable: controllable,
	}

	// tariff forecast-based grid charging demand
	if bat.ChargeFromGrid {
		if demand := site.applyBatteryGridChargeLimit(bat.CMax, grid, minLen); demand != nil {
			bat.PDemand = prorate(demand, firstSlotDuration)
		}
	}

	return bat, detail
}

func matchSoc(ts []float32, fun func(float32) bool) time.Time {
	for i, soc := range ts {
		if fun(soc) {
			// TODO first slot
			return time.Now().Add(time.Duration(i+1) * tariff.SlotDuration).Round(time.Second)
		}
	}

	return time.Time{}
}

// continuousDemand creates a slice of power demands depending on loadpoint mode
func continuousDemand(lp loadpoint.API, minLen int) []float32 {
	if lp.GetStatus() != api.StatusC {
		return nil
	}

	pwr := lp.EffectiveMaxPower()
	if lp.GetMode() == api.ModeMinPV {
		pwr = lp.EffectiveMinPower()
	}

	return lo.RepeatBy(minLen, func(i int) float32 {
		return float32(pwr / slotsPerHour)
	})
}

// loadpointProfile returns the loadpoint's charging profile in Wh
// TODO consider charging efficiency
func loadpointProfile(lp loadpoint.API, minLen int) []float64 {
	mode := lp.GetMode()
	status := lp.GetStatus()

	if status != api.StatusC || (mode != api.ModeMinPV && mode != api.ModeNow) {
		return nil
	}

	power := lp.GetChargePower()
	if minP := lp.EffectiveMinPower(); mode == api.ModeMinPV && minP < power {
		power = minP
	}

	energy := lp.GetRemainingEnergy() * 1e3 // Wh
	energyKnown := energy > 0

	res := make([]float64, 0, minLen)
	for range minLen {
		deltaEnergy := power * float64(tariff.SlotDuration) / float64(time.Hour) // Wh
		if energyKnown && deltaEnergy >= energy {
			deltaEnergy = energy
		}
		energy -= deltaEnergy

		res = append(res, deltaEnergy)
	}

	return res
}

// unmodelledPower returns the uncontrollable power of a connected loadpoint that
// cannot be modelled as storage because the vehicle capacity is unknown
func unmodelledPower(lp loadpoint.API) float64 {
	power := lp.GetChargePower()

	// minpv keeps drawing at least min power while the vehicle is connected,
	// even before the charge meter has caught up
	if lp.GetMode() == api.ModeMinPV && lp.GetStatus() == api.StatusC {
		power = max(power, lp.EffectiveMinPower())
	}

	return max(0, power)
}

// homeProfile returns the home base load in Wh
func (site *Site) homeProfile(minLen int) ([]float64, error) {
	// kWh over last 30 days
	profile, err := site.collectors[metrics.Home].EnergyProfile(now.BeginningOfDay().AddDate(0, 0, -30))
	if err != nil {
		return nil, err
	}

	// max 4 days
	slots := make([]float64, 0, minLen+1)
	for len(slots) <= minLen+24*4 { // allow for prorating first day
		slots = append(slots, profile[:]...)
	}

	res := profileSlotsFromNow(slots)
	if len(res) < minLen {
		return nil, fmt.Errorf("minimum home profile length %d is less than required %d", len(res), minLen)
	}
	if len(res) > minLen {
		res = res[:minLen]
	}

	res = site.applyHeatingDegree(res)

	// convert to Wh
	return lo.Map(res, func(v float64, i int) float64 {
		return v * 1e3
	}), nil
}

// profileSlotsFromNow strips away any slots before "now".
// The profile contains 48 15min slots (00:00-23:45) that repeat for multiple days.
func profileSlotsFromNow(profile []float64) []float64 {
	firstSlot := int(time.Now().Truncate(tariff.SlotDuration).Sub(now.BeginningOfDay()) / tariff.SlotDuration)
	return profile[firstSlot:]
}

// measuredSlotEnergy returns the summed energy in Wh of the last completed
// metrics slot for the given collector refs, 0 when not available
func (site *Site) measuredSlotEnergy(refs ...string) float64 {
	var sum float64
	for _, ref := range refs {
		c, ok := site.collectors[ref]
		if !ok {
			return 0
		}

		v, ok := c.LastSlotEnergy()
		if !ok {
			return 0
		}
		sum += v
	}

	return sum * 1e3
}

// blendMeasured decays the first slots from the measured value into the
// forecast. Slot 0 uses the measured value, the forecast takes over from
// slot decaySlots on.
func blendMeasured[T constraints.Float](slots []T, measured T, decaySlots int) {
	for i := range min(decaySlots, len(slots)) {
		w := T(decaySlots-i) / T(decaySlots)
		slots[i] = w*measured + (1-w)*slots[i]
	}
}

// blendScaleByLead applies a per-lead scale that decays towards 1 over the first slots
// (B31). It replaced a flat variant that took one scale for the whole window: a single flat scale
// applied to every slot in the decay window silently mixes two different scales for
// every slot but the first. slots[i] was built by scaleAndPruneByLead using
// scaleAt(lead of leadSlots[i]) - a flat ratio computed with, say, the nowcast scale
// only cancels that baked-in per-lead scale correctly at i=0, where lead is 0 and the
// two scales happen to be the same value; for i>0 the mismatch shows up as a
// systematic over- or under-correction proportional to how much scaleAt(lead) differs
// from whatever scale the flat ratio used. ratioAt is evaluated per slot with that
// slot's own lead, so the correction is internally consistent at every index, not
// just the first - see TestBlendScaleByLead.
func blendScaleByLead[T constraints.Float](slots []T, leadSlots api.Rates, now time.Time, ratioAt func(lead time.Duration) float64, decaySlots int) {
	for i := range min(decaySlots, len(slots), len(leadSlots)) {
		w := float64(decaySlots-i) / float64(decaySlots)
		ratio := ratioAt(leadSlots[i].Start.Sub(now))
		slots[i] = T(float64(slots[i]) * (w*ratio + (1 - w)))
	}
}

// prorate adjusts the first slot's energy amount according to remaining duration
func prorate[T constraints.Float](slots []T, firstSlotDuration time.Duration) []float32 {
	// return empty slice instead of nil to make api happy
	if len(slots) == 0 {
		return []float32{}
	}

	res := slices.Clone(slots)
	res[0] = res[0] * T(firstSlotDuration) / T(tariff.SlotDuration)
	return lo.Map(res, func(f T, _ int) float32 {
		return float32(f)
	})
}

func solarRatesToEnergy(rr api.Rates) (api.Rates, error) {
	res := make(api.Rates, 0, len(rr))

	for _, r := range rr {
		energy := solarEnergy(rr, r.Start, r.End)
		if energy < 0 {
			return nil, fmt.Errorf("negative solar energy from %v to %v: %.3f", r.Start, r.End, energy)
		}

		res = append(res, api.Rate{
			Start: r.Start,
			End:   r.End,
			Value: energy,
		})
	}

	return res, nil
}

func currentRates(tariff api.Tariff) api.Rates {
	if tariff == nil {
		return nil
	}

	rates, err := tariff.Rates()
	if err != nil {
		return nil
	}

	// filter past slots
	now := time.Now()
	return lo.Filter(rates, func(slot api.Rate, _ int) bool {
		return slot.End.After(now)
	})
}

// optimizerHorizon is the timeframe the hosted optimizer is limited to for sake
// of performance: 48 hours, extended to the end of that day. In the early hours
// the extension would add almost a full day, hence it only applies past 6:00.
func optimizerHorizon(t time.Time) time.Time {
	horizon := t.Add(48 * time.Hour)
	if t.Hour() < 6 {
		return horizon
	}
	return now.With(horizon).EndOfDay()
}

// slotsUntil limits maxLen to the slots starting before the given horizon
func slotsUntil(rates api.Rates, horizon time.Time, maxLen int) int {
	if i := slices.IndexFunc(rates[:min(maxLen, len(rates))], func(slot api.Rate) bool {
		return slot.Start.After(horizon)
	}); i >= 0 {
		return i
	}
	return maxLen
}

func timeSteps(minLen int, now time.Time) []int {
	res := make([]int, 0, minLen)

	eos := now.Truncate(tariff.SlotDuration).Add(tariff.SlotDuration)
	if d := eos.Sub(now); d > time.Second && d < tariff.SlotDuration {
		res = append(res, int(d.Seconds()))
	}

	for i := len(res); i < minLen; i++ {
		res = append(res, int(tariff.SlotDuration.Seconds())) // 15min slots
	}

	return res
}

func asTimestamps(dt []int, now time.Time) []time.Time {
	res := make([]time.Time, 0, len(dt))

	eos := now.Truncate(tariff.SlotDuration).Add(tariff.SlotDuration)
	res = append(res, eos.Add(-time.Duration(dt[0])*time.Second))

	for i := range len(dt) - 1 {
		res = append(res, res[i].Add(time.Duration(dt[i])*time.Second))
	}

	return res
}

func scaleAndPrune(rates api.Rates, scale float64, maxLen int) []float32 {
	res := make([]float32, 0, maxLen)

	for _, slot := range rates {
		res = append(res, float32(slot.Value*scale))
		if len(res) >= maxLen {
			break
		}
	}

	return res
}

// scaleAndPruneByLead is scaleAndPrune's lead-time-aware counterpart: instead
// of one flat scale for the whole horizon, scaleAt is evaluated per slot with
// that slot's distance from now, so a 24h-ahead slot is corrected with 24h-ahead
// bias and a slot starting now with nowcast bias (see solarScaleAt).
func scaleAndPruneByLead(rates api.Rates, now time.Time, scaleAt func(lead time.Duration) float64, maxLen int) []float32 {
	res := make([]float32, 0, maxLen)

	for _, slot := range rates {
		res = append(res, float32(slot.Value*scaleAt(slot.Start.Sub(now))))
		if len(res) >= maxLen {
			break
		}
	}

	return res
}

func (site *Site) applyPlanGoal(lp loadpoint.API, bat *optimizer.BatteryConfig, minLen int) {
	goal, socBased := lp.GetPlanGoal()
	if goal <= 0 {
		return
	}

	// Convert to Wh
	if vehicle := lp.GetVehicle(); socBased && vehicle != nil {
		goal *= vehicle.Capacity() * 10
	} else {
		goal *= 1000 // Wh
	}

	ts := lp.EffectivePlanTime()
	if ts.IsZero() {
		return
	}

	// TODO precise slot placement
	slot := int(time.Until(ts) / tariff.SlotDuration)
	if slot >= 0 && slot < minLen {
		bat.SGoal = make([]float32, minLen)
		bat.SGoal[slot] = float32(goal)
		bat.SMax = max(bat.SMax, float32(goal))
	} else {
		site.log.DEBUG.Printf("plan beyond forecast range or overrun: %.1f at %v slot %d", goal, ts.Round(time.Minute), slot)
	}
}

// TODO remove once smart cost limit usage becomes obsolete
func applySmartCostLimit(lp loadpoint.API, demand []float32, grid api.Rates, minLen int) []float32 {
	costLimit := lp.GetSmartCostLimit()
	if costLimit == nil {
		return demand
	}

	maxLen := min(minLen, len(grid))

	// Check if any slots meet the cost limit
	if hasAffordableSlots := slices.ContainsFunc(grid[:maxLen], func(r api.Rate) bool {
		return r.Value <= *costLimit
	}); !hasAffordableSlots {
		return demand
	}

	maxPower := lp.EffectiveMaxPower()

	if demand == nil {
		demand = make([]float32, minLen)
	}

	for i := range maxLen {
		if grid[i].Value <= *costLimit {
			demand[i] = float32(maxPower / slotsPerHour)
		}
		// else: keep existing demand (either 0 or minPower from ModeMinPV)
	}

	return demand
}

// applyPrecondition forces max charging power during the planner's precondition window
// ("late charging"), i.e. the last precondition duration before the plan time
func applyPrecondition(lp loadpoint.API, demand []float32, minLen int) []float32 {
	precondition := lp.EffectivePlanStrategy().Precondition
	if precondition <= 0 {
		return demand
	}

	ts := lp.EffectivePlanTime()
	if ts.IsZero() {
		return demand
	}

	// TODO precise slot placement
	end := time.Until(ts)
	start := end - precondition
	if end <= 0 {
		return demand
	}

	first := max(int(start/tariff.SlotDuration), 0)
	if first >= minLen {
		return demand
	}

	if demand == nil {
		demand = make([]float32, minLen)
	}

	energy := float32(lp.EffectiveMaxPower() / slotsPerHour)

	for i := first; i < minLen; i++ {
		slotStart := time.Duration(i) * tariff.SlotDuration
		overlap := min(end, slotStart+tariff.SlotDuration) - max(start, slotStart)
		if overlap <= 0 {
			break
		}

		demand[i] = max(demand[i], energy*float32(overlap)/float32(tariff.SlotDuration))
	}

	return demand
}

func (site *Site) applyBatteryGridChargeLimit(cMax float32, grid api.Rates, minLen int) []float32 {
	limit := site.GetBatteryGridChargeLimit()
	if limit == nil {
		return nil
	}

	maxLen := min(minLen, len(grid))

	if hasAffordableSlots := slices.ContainsFunc(grid[:maxLen], func(r api.Rate) bool {
		return r.Value <= *limit
	}); !hasAffordableSlots {
		return nil
	}

	demand := make([]float32, minLen)
	for i := range maxLen {
		if grid[i].Value <= *limit {
			demand[i] = float32(float64(cMax) / slotsPerHour)
		}
	}

	return demand
}

// apiError extracts error message from optimizer API response
func apiError(resp *optimizer.PostOptimizeChargeScheduleResponse) error {
	var errObj *optimizer.Error
	switch resp.StatusCode() {
	case http.StatusBadRequest:
		errObj = resp.JSON400
	case http.StatusInternalServerError:
		errObj = resp.JSON500
	}

	if errObj == nil {
		return fmt.Errorf("invalid status: %d: %s", resp.StatusCode(), resp.Body)
	}

	if len(errObj.Details) > 0 {
		var details []string
		for field, msg := range errObj.Details {
			details = append(details, fmt.Sprintf("%s: %s", field, msg))
		}
		slices.Sort(details)
		return fmt.Errorf("%s (%s)", errObj.Message, strings.Join(details, ", "))
	}

	return errors.New(errObj.Message)
}
