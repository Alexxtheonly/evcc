// Pure transforms for the ADR-011 per-slot decision replay (core/metrics/ledger_decisions.go's
// DecisionRow). Kept framework-free for unit testing.

import type { LedgerDecisionRow } from "./savingsLedger.types";
import { ZERO_EPSILON_EUR } from "./savingsLedgerChain";

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
  return MODE_LABEL_KEYS[normalizeMode(mode)];
}

// the four modes in the order the legend lists them
const MODES = Object.keys(MODE_LABEL_KEYS);

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
 * What happened in one slot, as the ribbon colours it.
 *  - "unrecorded": no optimizer run produced a suggestion for this slot, so there is
 *    nothing to compare against. Absent, never the string "unknown" - folding that into
 *    "followed" would claim the optimizer agreed with what was applied.
 *  - "followed": the applied mode matches the suggestion - the battery did as asked.
 *  - "diverged": the applied mode overrode the suggestion (slotFlowDeltaEur prices that
 *    difference, when it is computable).
 */
export type SlotState = "followed" | "diverged" | "unrecorded";

/** Whether a row recorded a suggestion at all. The backend stores nil, but rows written
 * before the column became nullable spell the same absence as api.BatteryUnknown's
 * "unknown" - and that token is never a decision, so an older backend must not be able to
 * make this card report an override of a suggestion nobody made. */
export function hasSuggestion(row: LedgerDecisionRow): boolean {
  return row.suggestedMode != null && row.suggestedMode !== "" && row.suggestedMode !== "unknown";
}

export function slotState(row: LedgerDecisionRow): SlotState {
  if (!hasSuggestion(row)) return "unrecorded";
  return normalizeMode(row.appliedMode) === normalizeMode(row.suggestedMode)
    ? "followed"
    : "diverged";
}

/** Which way a diverged slot's slot-local delta went. Two of these are not outcomes:
 * "unknown" is absence (ADR-011 rule 3 - never folded into "saved"), and "neutral" is a
 * computed wash. An override that changed the bill by nothing must not be coloured, or
 * captioned, as one that saved money. */
export type DeltaDirection = "cost" | "saved" | "neutral" | "unknown";

export function deltaDirection(row: LedgerDecisionRow): DeltaDirection {
  return moneyDirection(row.slotFlowDeltaEur);
}

export function moneyDirection(eur: number | null | undefined): DeltaDirection {
  if (eur == null) return "unknown";
  if (Math.abs(eur) <= ZERO_EPSILON_EUR) return "neutral";
  return eur > 0 ? "cost" : "saved";
}

export interface DecisionSlot {
  row: LedgerDecisionRow;
  state: SlotState;
  /** epoch ms of the slot start */
  tsMs: number;
}

/** Rows sorted ascending by ts, tagged with their state and epoch ms - what the
 * timeline and the table both iterate. */
export function decisionSlots(rows: LedgerDecisionRow[]): DecisionSlot[] {
  return rows
    .map((row) => ({ row, state: slotState(row), tsMs: new Date(row.ts).getTime() }))
    .sort((a, b) => a.tsMs - b.tsMs);
}

/** The counts the caption under the timeline is composed from. netDeltaEur is null when
 * no diverged slot has a computable figure - distinct from 0.00, which would claim the
 * overrides were free. */
export interface DecisionSummary {
  slots: number;
  suggested: number;
  followed: number;
  diverged: number;
  divergedPriced: number;
  netDeltaEur: number | null;
  unhealthy: number;
}

export function decisionSummary(slots: DecisionSlot[]): DecisionSummary {
  const summary: DecisionSummary = {
    slots: slots.length,
    suggested: 0,
    followed: 0,
    diverged: 0,
    divergedPriced: 0,
    netDeltaEur: null,
    unhealthy: 0,
  };

  for (const { row, state } of slots) {
    if (state !== "unrecorded") summary.suggested++;
    if (state === "followed") summary.followed++;
    if (state !== "diverged") continue;

    summary.diverged++;
    if (!row.healthOk) summary.unhealthy++;
    if (row.slotFlowDeltaEur == null) continue;
    summary.divergedPriced++;
    summary.netDeltaEur = (summary.netDeltaEur ?? 0) + row.slotFlowDeltaEur;
  }

  return summary;
}

/** The distinct modes drawn in either lane, in MODES order - so the legend lists what
 * is on screen rather than all four every time. */
export function modesPresent(slots: DecisionSlot[]): string[] {
  const seen = new Set<string>();
  for (const { row } of slots) {
    seen.add(normalizeMode(row.appliedMode));
    if (hasSuggestion(row)) seen.add(normalizeMode(row.suggestedMode));
  }
  return MODES.filter((m) => seen.has(m));
}

/** Milliseconds in one control slot (tariff.SlotDuration). */
export const SLOT_MS = 15 * 60 * 1000;

/** Hour spacing for the timeline's x-axis labels: dense enough to orient on a one-day
 * window, sparse enough that a week's worth doesn't collapse into a smear. */
export function axisStepHours(spanMs: number): number {
  const days = spanMs / (24 * 3600 * 1000);
  // a handful of slots would otherwise get an axis with no label on it at all
  if (days <= 0.5) return 1;
  if (days <= 1.5) return 3;
  if (days <= 4) return 6;
  return 12;
}

/** One contiguous block of the ribbon: adjacent slots that share a value, merged. */
export interface LaneRun<T> {
  /** epoch ms, inclusive */
  start: number;
  /** epoch ms, exclusive */
  end: number;
  value: T;
}

/**
 * Merge adjacent slots that map to the same value into runs. Two slots are adjacent
 * only when the second starts exactly where the first ends, so a period with missing
 * rows keeps its gap instead of being bridged by a block that claims data it doesn't
 * have. value() returning null drops the slot from the ribbon entirely.
 */
export function laneRuns<T>(
  slots: DecisionSlot[],
  value: (slot: DecisionSlot) => T | null
): LaneRun<T>[] {
  const runs: LaneRun<T>[] = [];
  for (const slot of slots) {
    const v = value(slot);
    if (v === null) continue;
    const last = runs[runs.length - 1];
    if (last && last.end === slot.tsMs && last.value === v) {
      last.end = slot.tsMs + SLOT_MS;
      continue;
    }
    runs.push({ start: slot.tsMs, end: slot.tsMs + SLOT_MS, value: v });
  }
  return runs;
}

/** A block of consecutive slots in which the applied mode overrode the suggestion.
 * deltaEur is null when not one slot in the run had a computable figure - ADR-011
 * rule 3 again: a run of unpriceable overrides is not a EUR 0.00 run. */
export interface OverrideRun {
  start: number;
  end: number;
  slots: number;
  /** how many of those slots carried a computable figure - a partial sum must never be
   * badged as the run's total */
  pricedSlots: number;
  deltaEur: number | null;
}

export function overrideRuns(slots: DecisionSlot[]): OverrideRun[] {
  return laneRuns(slots, (slot) => (slot.state === "diverged" ? "diverged" : null)).map((run) => {
    let deltaEur: number | null = null;
    let count = 0;
    let priced = 0;
    for (const slot of slots) {
      if (slot.tsMs < run.start || slot.tsMs >= run.end || slot.state !== "diverged") continue;
      count++;
      if (slot.row.slotFlowDeltaEur == null) continue;
      priced++;
      deltaEur = (deltaEur ?? 0) + slot.row.slotFlowDeltaEur;
    }
    return { start: run.start, end: run.end, slots: count, pricedSlots: priced, deltaEur };
  });
}

/**
 * Which override runs get a value badge on the chart: the biggest by absolute effect,
 * capped, and never two closer together than minGapMs, which the caller derives from
 * the badge's PIXEL width and the chart's own width - a fraction of the time span is
 * the wrong test, because a badge is the same size on a phone and on a desktop.
 *
 * Only fully-priced runs qualify. Badging a run whose figure covers 3 of its 12 slots
 * would put a partial sum over the full width of the run, and a wash (a run whose
 * priced slots cancel out) gets no badge rather than a green "saved EUR 0.00".
 */
export function badgedRuns(runs: OverrideRun[], minGapMs: number, max = 3): OverrideRun[] {
  const mid = (run: OverrideRun) => (run.start + run.end) / 2;
  const picked: OverrideRun[] = [];
  for (const run of runs
    .filter(
      (r) =>
        r.deltaEur !== null && r.pricedSlots === r.slots && moneyDirection(r.deltaEur) !== "neutral"
    )
    .sort((a, b) => Math.abs(b.deltaEur!) - Math.abs(a.deltaEur!))) {
    if (picked.length >= max) break;
    if (picked.some((p) => Math.abs(mid(p) - mid(run)) < minGapMs)) continue;
    picked.push(run);
  }
  return picked.sort((a, b) => a.start - b.start);
}
