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

export function modeLabelKey(mode: string): string | undefined {
  return MODE_LABEL_KEYS[mode];
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
 *  - "steady": AppliedMode == SuggestedMode - nothing was vetoed, nothing to report.
 *  - "vetoed-cost": a veto happened and its slot-local delta is positive (the applied
 *    mode cost more than the rejected suggestion would have, in that slot).
 *  - "vetoed-saved": a veto happened and its slot-local delta is negative or zero.
 *  - "vetoed-unknown": a veto happened but slotFlowDeltaEur isn't computable (no
 *    battery physics, or the slot fell outside the ledger's valid slot set) - ADR-011
 *    rule 3: absence is never a sentinel, so this is a distinct state, not "saved".
 */
export type DecisionOutcome = "steady" | "vetoed-cost" | "vetoed-saved" | "vetoed-unknown";

export function decisionOutcome(row: LedgerDecisionRow): DecisionOutcome {
  if (row.appliedMode === row.suggestedMode) return "steady";
  if (row.slotFlowDeltaEur == null) return "vetoed-unknown";
  return row.slotFlowDeltaEur > 0 ? "vetoed-cost" : "vetoed-saved";
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
  return rows.filter((r) => r.appliedMode !== r.suggestedMode && !r.healthOk).length;
}
