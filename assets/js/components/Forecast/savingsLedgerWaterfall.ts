// Pure geometry for the ADR-011 savings-ledger waterfall (bridge) chart. Framework-free
// for unit testing, mirroring savingsLedgerChain.ts / optimizerOverlay.ts.
//
// Grounded in core/metrics/ledger_worlds.go - see savingsLedger.types.ts's field-level
// comments for the Go source each type mirrors.

import type { LedgerChain } from "./savingsLedger.types";
import { pickSettled, ZERO_EPSILON_EUR, type SettlementHeadline } from "./savingsLedgerChain";

/** The five columns, left to right. Always all five, even when a contribution is absent
 * (a site without a battery draws battery/control as zero-magnitude columns rather than
 * silently dropping them - the reader must be able to see that the measure existed and
 * moved nothing). Control is NEVER split into routing/timing here: routing+timing ==
 * control.full, and splitting the column costs more readability than the extra detail
 * buys. The split is surfaced in the Control column's tooltip instead, and ONLY at the
 * perSlot headline, because control.routing/control.timing are not a per-lens pair like
 * every other figure here - Routing is always the period-average-lens diff and Timing is
 * always Full (the perSlot-lens diff) minus Routing (ledger_worlds.go's ControlSplit doc
 * comment). They sum to Full, i.e. to the perSlot Control contribution; showing them
 * beside a periodAverage Control figure would render two numbers that don't sum to the
 * figure above them, breaking the "parts sum to the whole" guarantee this chart exists to
 * demonstrate. */
export type WaterfallKey = "baseline" | "pv" | "battery" | "control" | "paid";

export const WATERFALL_KEYS: WaterfallKey[] = ["baseline", "pv", "battery", "control", "paid"];

export interface WaterfallColumn {
  key: WaterfallKey;
  /** For the two end columns (`total`): that world's cost, a level. For the three middle
   * columns: the contribution, SIGNED per ledger_worlds.go - POSITIVE means the measure
   * SAVED money (the bar descends), NEGATIVE means it COST money (the bar rises). */
  eur: number;
  /** Bottom edge of the bar, in euro. */
  base: number;
  /** Height of the bar, in euro; always >= 0. */
  span: number;
  /** Running cost level after this column. */
  level: number;
  /** One of the two end columns (full-height bar from zero), not a contribution. */
  total: boolean;
  /** ADR-011 rule 7: this column's figure rests on the W2 counterfactual battery and
   * must carry that in its axis label, not in a footnote. PV is measured arithmetic over
   * directly measured PV/grid energy and realised prices, no derived battery assumption
   * involved; Battery and Control are estimated whenever the site has a battery
   * (chain.batteryPhysics is set) because W2 depends on it. deriveBatteryPhysics's Source
   * strings say whether that's a device-reported fact or a fallback derived from history;
   * this module never fabricates a numeric error bar the API doesn't provide. */
  estimated: boolean;
  /** The measure cost money rather than saving it - drawn rising, in the danger colour.
   * Never clamped to zero (ADR-011 rule 1). */
  overspend: boolean;
  /** |eur| <= ZERO_EPSILON_EUR: drawn as a zero-height bar carrying its own label. */
  zero: boolean;
}

export interface WaterfallLayout {
  columns: WaterfallColumn[];
  /** chain.worlds[0], the "without solar, battery or control" cost. */
  w0: number;
  /** chain.worlds[3], what was actually paid. */
  paid: number;
  /** w0 - paid. NEGATIVE when the period cost more than the baseline - shown, never
   * clamped (ADR-011 rule 1). */
  saved: number;
  /** saved / w0, or null when w0 is not a usable denominator. */
  savedFraction: number | null;
  /** Where the telescoping chain actually ends (w0 - pv - battery - control). Equals
   * `paid` by construction; exposed so a test can assert the identity rather than the
   * UI silently papering over a mismatch. */
  endLevel: number;
  /** Zero of the plotting coordinate system, in euro: min(0, lowest bar bottom). Zero in
   * every normal period. Negative only when some level goes below zero (a net-credit
   * period), which is exactly when a chart pinned at y=0 would clip a bar. */
  origin: number;
}

const MIDDLE: { key: WaterfallKey; label: "PV" | "Battery" | "Control" }[] = [
  { key: "pv", label: "PV" },
  { key: "battery", label: "Battery" },
  { key: "control", label: "Control" },
];

function worldCost(chain: LedgerChain, index: number, headline: SettlementHeadline): number {
  const world = chain.worlds[index];
  return world ? pickSettled(world.settled, headline) : 0;
}

/**
 * Five columns: the W0 baseline, one floating bar per measure, and the W3 bill.
 *
 * The running cost level telescopes exactly - L0 = W0, L1 = L0 - PV, L2 = L1 - Battery,
 * L3 = L2 - Control - because Contribution = Cost(previous world) - Cost(this world)
 * (ledger_worlds.go), so every intermediate level IS the corresponding world's cost and
 * L3 is W3. Each middle bar spans [min(before, after), max(before, after)] with height
 * |eur|, so a measure that cost money draws upward from the previous level instead of
 * being hidden or clamped.
 */
export function waterfallLayout(chain: LedgerChain, headline: SettlementHeadline): WaterfallLayout {
  const w0 = worldCost(chain, 0, headline);
  const paid = worldCost(chain, 3, headline);
  const hasPhysics = !!chain.batteryPhysics;

  const columns: WaterfallColumn[] = [
    {
      key: "baseline",
      eur: w0,
      base: Math.min(0, w0),
      span: Math.abs(w0),
      level: w0,
      total: true,
      estimated: false,
      overspend: false,
      zero: false,
    },
  ];

  let level = w0;
  for (const { key, label } of MIDDLE) {
    const contribution = chain.contributions.find((c) => c.label === label);
    // absent contribution (e.g. a site with no battery) is a zero-magnitude column, not
    // a missing one - the reader still sees that the measure was considered.
    const eur = contribution ? pickSettled(contribution.settled, headline) : 0;
    const before = level;
    const after = before - eur;
    columns.push({
      key,
      eur,
      base: Math.min(before, after),
      span: Math.abs(eur),
      level: after,
      total: false,
      estimated: key === "pv" ? false : hasPhysics,
      overspend: eur < -ZERO_EPSILON_EUR,
      zero: Math.abs(eur) <= ZERO_EPSILON_EUR,
    });
    level = after;
  }

  columns.push({
    key: "paid",
    eur: paid,
    base: Math.min(0, paid),
    span: Math.abs(paid),
    level: paid,
    total: true,
    estimated: false,
    overspend: false,
    zero: false,
  });

  const origin = Math.min(0, ...columns.map((c) => c.base));
  const saved = w0 - paid;

  return {
    columns,
    w0,
    paid,
    saved,
    savedFraction: w0 > 0 ? saved / w0 : null,
    endLevel: level,
    origin,
  };
}

/** Bar bottom in plotting coordinates. The chart is drawn in a system translated by
 * `origin` (zero in every normal period) so that a net-credit period - where a level
 * goes below zero - is neither clipped by an axis pinned at 0 nor broken by ECharts'
 * stacking, which accumulates positive and negative values into separate stacks and so
 * cannot express a floating bar whose base is negative and whose height is positive.
 * Always >= 0 by construction. */
export function plotBase(column: WaterfallColumn, layout: WaterfallLayout): number {
  return column.base - layout.origin;
}

/** The running level after each column, in plotting coordinates - the y of the thin
 * connector that links one bar to the next. Same origin shift as plotBase, so a caller
 * can feed both to the same axis. */
export function plotLevels(layout: WaterfallLayout): number[] {
  return layout.columns.map((c) => c.level - layout.origin);
}

// --- axis scale ---------------------------------------------------------------------

/** Headroom above the tallest bar so its value label has somewhere to sit. Small on
 * purpose: rounding up to the next whole-euro tick below usually adds a good deal more. */
const AXIS_HEADROOM = 1.05;
/** At most this many ticks, so a tall axis doesn't turn into a ladder. */
const AXIS_MAX_SPLITS = 5;
/** Tick sizes, WHOLE units only (1, 2, 5, 10, 20, 50, ...). The axis labels are rendered
 * without decimals, so a 2.50 tick would print two ticks both reading "3". */
const AXIS_STEP_MANTISSAS = [1, 2, 5];

export interface WaterfallAxis {
  /** Top of the axis, in plotting coordinates (add layout.origin for real euros). */
  max: number;
  /** Distance between ticks, in the same coordinates. Passed to ECharts as an explicit
   * `interval` rather than a splitNumber hint, so the ticks land exactly here. */
  interval: number;
}

/**
 * A whole-euro tick scale that covers every bar with a little headroom.
 *
 * Picks the smallest whole-unit step that gets the whole chart inside AXIS_MAX_SPLITS
 * ticks, then pins the axis at an exact multiple of it - so the labels read "0, 2, 4, 6,
 * 8" rather than ECharts' auto choice of "14.81". In the (rare) shifted case where
 * layout.origin is negative the ticks are still evenly spaced whole units apart, but the
 * labels are offset by origin and so are not themselves round - the honest figure wins
 * over the round one.
 */
export function waterfallAxis(layout: WaterfallLayout): WaterfallAxis {
  const top = Math.max(0, ...layout.columns.map((c) => c.base + c.span - layout.origin));
  const needed = top * AXIS_HEADROOM;
  if (!(needed > 0)) return { max: 1, interval: 1 };
  for (let exp = 0; exp <= 12; exp++) {
    for (const mantissa of AXIS_STEP_MANTISSAS) {
      const interval = mantissa * Math.pow(10, exp);
      const splits = Math.ceil(needed / interval);
      if (splits <= AXIS_MAX_SPLITS) return { max: interval * splits, interval };
    }
  }
  // unreachable for any real euro figure (1e12 ticks cover everything) - never fabricate
  // a scale that hides a bar.
  return { max: needed, interval: needed / AXIS_MAX_SPLITS };
}

// --- minimum rendered bar height ------------------------------------------------------

/** A bar smaller than this reads as a rule, not a bar - and once the "estimated" dashed
 * outline is drawn on it, as a dotted hairline with no fill at all. */
export const BAR_MIN_PX = 4;

/** BAR_MIN_PX expressed in the chart's own value units, given the axis top and the
 * plotting area's height in pixels. */
export function minSpan(axisMax: number, plotHeightPx: number): number {
  if (!(axisMax > 0) || !(plotHeightPx > 0)) return 0;
  return (BAR_MIN_PX / plotHeightPx) * axisMax;
}

/**
 * Bar height in plotting coordinates, floored at `floor` so a small-but-nonzero
 * contribution still reads as a bar.
 *
 * A `zero: true` column is returned untouched: a measure that moved nothing must never be
 * drawn as though it had. The floor is deliberately applied to the height only, leaving
 * the bar's base where the geometry put it - the bar's top can therefore overshoot its
 * true level by up to BAR_MIN_PX. That is a rendering minimum, not a restatement of the
 * figure: the connectors and every printed number still come from the true levels.
 */
export function plotSpan(column: WaterfallColumn, floor: number): number {
  if (column.zero) return column.span;
  return Math.max(column.span, floor);
}
