// Pure transforms for the per-slot decision replay (core/metrics/ledger_decisions.go's
// DecisionRow). Kept framework-free for unit testing.

import type { LedgerDecisionRow } from "./savingsLedger.types";

// AppliedMode/SuggestedMode mirror api.BatteryMode.String() as plain lowercase strings, the
// same vocabulary BATTERY_MODE uses, so the existing forecast.optimizer.mode* keys apply.
const MODE_LABEL_KEYS: Record<string, string> = {
  normal: "forecast.optimizer.modeNormal",
  hold: "forecast.optimizer.modeHold",
  charge: "forecast.optimizer.modeCharge",
  holdcharge: "forecast.optimizer.modeHoldcharge",
};

/**
 * api.BatteryUnknown records a slot in which evcc held no override, which on a site with a
 * battery means exactly what api.BatteryNormal means. The backend folds the two at
 * persistence time, but rows written before it did still carry "unknown". Folded here once,
 * before anything compares or labels a mode, so a legacy row cannot read as a veto that
 * never happened and the wire word never reaches the screen.
 */
export function normalizeMode(mode?: string): string {
  return !mode || mode === "unknown" ? "normal" : mode;
}

export function modeLabelKey(mode: string): string | undefined {
  return MODE_LABEL_KEYS[normalizeMode(mode)];
}

// control_slots.go's VetoReason is optimizerVetoReason's wire value, the same enum
// OPTIMIZER_VETO_REASON covers for the live slot-0 annotation.
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
 *  - "steady": AppliedMode == SuggestedMode after normalizeMode, nothing was vetoed.
 *  - "vetoed-cost": a veto whose slot-local delta is positive (the applied mode cost more
 *    than the rejected suggestion would have, in that slot).
 *  - "vetoed-saved": a veto whose slot-local delta is negative or zero.
 *  - "vetoed-unknown": a veto whose slotFlowDeltaEur is not computable (no battery physics,
 *    or the slot fell outside the valid slot set). Absence is never a sentinel, so this is
 *    its own state, not "saved".
 */
export type DecisionOutcome =
  | "no-suggestion"
  | "steady"
  | "vetoed-cost"
  | "vetoed-saved"
  | "vetoed-unknown";

export function decisionOutcome(row: LedgerDecisionRow): DecisionOutcome {
  // absent, not "unknown": the backend records nil when no optimizer run produced a
  // suggestion. Folding that into "steady" via normalizeMode would claim the optimizer
  // agreed with what was applied. Legacy rows spelling the same absence as "unknown" are
  // decoded to null on the Go read path, so they arrive here already absent.
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

/** Rows sorted ascending by ts, tagged with their outcome and epoch ms. */
export function decisionSlots(rows: LedgerDecisionRow[]): DecisionSlot[] {
  return rows
    .map((row) => ({ row, outcome: decisionOutcome(row), tsMs: new Date(row.ts).getTime() }))
    .sort((a, b) => a.tsMs - b.tsMs);
}

/** Vetoed rows whose health flag was false: a decision made under a condition the site
 * itself flagged as unhealthy. */
export function unhealthyVetoCount(rows: LedgerDecisionRow[]): number {
  return rows.filter((r) => isVeto(decisionOutcome(r)) && !r.healthOk).length;
}
