// Pure transforms for the ADR-011 per-slot decision replay (core/metrics/ledger_decisions.go's
// DecisionRow). Kept framework-free for unit testing.

import type { LedgerDecisionRow } from "./savingsLedger.types";

// AppliedMode/SuggestedMode mirror api.BatteryMode.String() as plain lowercase strings
// (control_slots.go's own doc comment), the same vocabulary BATTERY_MODE already uses -
// reuse PriceChart.vue's existing forecast.optimizer.mode* i18n keys rather than adding
// a second set of English strings for the same four modes.
const MODE_LABEL_KEYS: Record<string, string> = {
  normal: "forecast.optimizer.modeNormal",
  hold: "forecast.optimizer.modeHold",
  charge: "forecast.optimizer.modeCharge",
  holdcharge: "forecast.optimizer.modeHoldcharge",
};

/**
 * api.BatteryUnknown is what evcc records for a slot in which it held no override, and on
 * a site with a battery that means exactly what api.BatteryNormal means. The backend now
 * folds the two at persistence time, but every row written before it did still carries
 * "unknown", and the rest of this UI has always treated them as one thing anyway -
 * Battery/BatteryStatusCard.vue's statusState(), Energyflow.vue, Bar.vue and
 * BatteryBoostButton.vue all match only hold/holdcharge/charge and let normal and unknown
 * alike fall through. Folded here, once, before anything compares or labels a mode, so a
 * legacy row cannot read as a veto that never happened - and so the wire word "unknown"
 * never reaches the screen.
 */
export function normalizeMode(mode?: string): string {
  return !mode || mode === "unknown" ? "normal" : mode;
}

export function modeLabelKey(mode: string): string | undefined {
  return MODE_LABEL_KEYS[normalizeMode(mode)];
}

// control_slots.go's VetoReason is optimizerVetoReason's wire value (core/site_optimizer.go) -
// the same enum OPTIMIZER_VETO_REASON already covers for the live slot-0 annotation.
const REASON_LABEL_KEYS: Record<string, string> = {
  payback: "forecast.optimizer.reasonPayback",
  forcedIdle: "forecast.optimizer.reasonForcedIdle",
  damping: "forecast.optimizer.reasonDamping",
  liveRate: "forecast.optimizer.reasonLiveRate",
};

export function reasonLabelKey(reason?: string): string | undefined {
  if (!reason) return undefined;
  return REASON_LABEL_KEYS[reason];
}

/**
 * A decision row's outcome, for colouring the timeline tick and the table row.
 *  - "steady": AppliedMode == SuggestedMode (normalizeMode applied to both) - nothing was
 *    vetoed, nothing to report.
 *  - "vetoed-cost": a veto happened and its slot-local delta is positive (the applied
 *    mode cost more than the rejected suggestion would have, in that slot).
 *  - "vetoed-saved": a veto happened and its slot-local delta is negative or zero.
 *  - "vetoed-unknown": a veto happened but slotFlowDeltaEur isn't computable (no
 *    battery physics, or the slot fell outside the ledger's valid slot set) - ADR-011
 *    rule 3: absence is never a sentinel, so this is a distinct state, not "saved".
 */
export type DecisionOutcome =
  | "no-suggestion"
  | "steady"
  | "vetoed-cost"
  | "vetoed-saved"
  | "vetoed-unknown";

export function decisionOutcome(row: LedgerDecisionRow): DecisionOutcome {
  // absent, not "unknown": the backend now records nil when no optimizer run produced a
  // suggestion for the slot (core/metrics/control_slots.go). Folding that into "steady"
  // via normalizeMode would claim the optimizer agreed with what was applied - on this
  // site's own database that was 62 % of the rows. Rows written before the column was
  // nullable still carry the literal "unknown" and are still folded, below.
  if (row.suggestedMode == null) return "no-suggestion";
  if (normalizeMode(row.appliedMode) === normalizeMode(row.suggestedMode)) return "steady";
  if (row.slotFlowDeltaEur == null) return "vetoed-unknown";
  return row.slotFlowDeltaEur > 0 ? "vetoed-cost" : "vetoed-saved";
}

/** True for the outcomes where the controller actually overrode a recorded suggestion -
 * the only ones with a rejected alternative to name or to price. */
export function isVeto(outcome: DecisionOutcome): boolean {
  return outcome !== "steady" && outcome !== "no-suggestion";
}

export interface DecisionSlot {
  row: LedgerDecisionRow;
  outcome: DecisionOutcome;
  tsMs: number;
}

/** Rows sorted ascending by ts, tagged with their outcome and epoch ms - what the
 * timeline strip and the table both iterate. */
export function decisionSlots(rows: LedgerDecisionRow[]): DecisionSlot[] {
  return rows
    .map((row) => ({ row, outcome: decisionOutcome(row), tsMs: new Date(row.ts).getTime() }))
    .sort((a, b) => a.tsMs - b.tsMs);
}

/** Count of vetoed rows whose health flag was false at the time - a decision made under
 * a condition the site itself flagged as unhealthy, worth surfacing distinctly. */
export function unhealthyVetoCount(rows: LedgerDecisionRow[]): number {
  return rows.filter((r) => isVeto(decisionOutcome(r)) && !r.healthOk).length;
}
