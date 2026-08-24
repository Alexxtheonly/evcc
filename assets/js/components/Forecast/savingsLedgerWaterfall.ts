// Pure geometry for the savings-ledger waterfall (bridge) chart. Kept framework-free for
// unit testing; sign conventions mirror core/metrics/ledger_worlds.go.

import type { LedgerChain } from "./savingsLedger.types";
import {
  contributionBand,
  pickSettled,
  ZERO_EPSILON_EUR,
  type SettlementHeadline,
} from "./savingsLedgerChain";

/** The five columns, left to right. Always all five: an absent contribution (a site with no
 * battery) is a zero-magnitude column, never a dropped one. Control is never split into
 * routing/timing here - Routing is the period-average-lens diff and Timing is Full minus
 * Routing, so beside a periodAverage Control figure the parts would not sum to the whole
 * this chart exists to demonstrate. The split is in the Control tooltip, at perSlot only. */
export type WaterfallKey = "baseline" | "pv" | "battery" | "control" | "paid";

export interface WaterfallColumn {
  key: WaterfallKey;
  /** End columns (`total`): that world's cost, a level. Middle columns: the contribution,
   * POSITIVE when the measure SAVED money (bar descends), NEGATIVE when it cost money. */
  eur: number;
  base: number;
  span: number;
  level: number;
  /** One of the two end columns (full-height bar from zero), not a contribution. */
  total: boolean;
  /** Rests on the W2 counterfactual battery, so the axis label has to say so rather than a
   * footnote. PV is measured arithmetic; Battery and Control are estimated whenever the
   * site has a battery. Never fabricate a numeric error bar: the API provides none. */
  estimated: boolean;
  /** Cost money by more than the period's own measurement noise (contributionBand), drawn
   * rising in the danger colour. Never clamped to zero: a period that came out worse than
   * the baseline says so. */
  overspend: boolean;
  /** |eur| <= ZERO_EPSILON_EUR: drawn as a zero-height bar carrying its own label. */
  zero: boolean;
  /** |eur| <= contributionBand: smaller than the period's measured noise floor, so its
   * DIRECTION is not evidence. Still drawn and printed, never hidden or rounded away, but
   * never coloured or worded as a saving or a loss either. */
  insideNoise: boolean;
}

export interface WaterfallLayout {
  columns: WaterfallColumn[];
  /** chain.worlds[0], the "without solar, battery or control" cost. */
  w0: number;
  paid: number;
  /** w0 - paid. NEGATIVE when the period cost more than the baseline; shown, never clamped. */
  saved: number;
  savedFraction: number | null;
  /** Zero of the plotting coordinate system, in euro: min(0, lowest bar bottom). Negative
   * only when a level goes below zero, which is exactly when a chart pinned at y=0 clips. */
  origin: number;
  /** The period's own measurement-noise floor, in euro (contributionBand). Carried here so
   * the bar's colour and the sentence printed under it cannot use two different thresholds. */
  band: number;
  /** The Control column, or null when the chain carries no Control contribution at all.
   * The only thing that tells absent from zero: |0| <= band, so reading the column alone
   * would report a site that never ran a controller as "too small to call". */
  control: WaterfallColumn | null;
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
 * Five columns: the W0 baseline, one floating bar per measure, and the W3 bill. The
 * running cost level telescopes exactly (L0 = W0 ... L3 = W3) because Contribution =
 * Cost(previous world) - Cost(this world). Each middle bar spans [min(before, after),
 * max(before, after)] with height |eur|, so a measure that cost money draws upward from
 * the previous level instead of being hidden or clamped.
 */
export function waterfallLayout(chain: LedgerChain, headline: SettlementHeadline): WaterfallLayout {
  const w0 = worldCost(chain, 0, headline);
  const paid = worldCost(chain, 3, headline);
  const hasPhysics = !!chain.batteryPhysics;
  const band = contributionBand(chain);

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
      insideNoise: false,
    },
  ];

  let level = w0;
  for (const { key, label } of MIDDLE) {
    const contribution = chain.contributions.find((c) => c.label === label);
    // an absent contribution is a zero-magnitude column, never a dropped one
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
      overspend: eur < -band,
      zero: Math.abs(eur) <= ZERO_EPSILON_EUR,
      insideNoise: Math.abs(eur) <= band,
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
    insideNoise: false,
  });

  const origin = Math.min(0, ...columns.map((c) => c.base));
  const saved = w0 - paid;
  const hasControl = chain.contributions.some((c) => c.label === "Control");

  return {
    columns,
    w0,
    paid,
    saved,
    savedFraction: w0 > 0 ? saved / w0 : null,
    origin,
    band,
    control: hasControl ? (columns.find((c) => c.key === "control") ?? null) : null,
  };
}

/** Bar bottom in plotting coordinates, translated by `origin` so a net-credit period is
 * neither clipped by an axis pinned at 0 nor broken by ECharts' stacking, which cannot
 * express a floating bar with a negative base. Always >= 0 by construction. */
export function plotBase(column: WaterfallColumn, layout: WaterfallLayout): number {
  return column.base - layout.origin;
}

/** The running level after each column, in plotting coordinates: the y of the connector
 * linking one bar to the next. */
export function plotLevels(layout: WaterfallLayout): number[] {
  return layout.columns.map((c) => c.level - layout.origin);
}

/** Headroom above the tallest bar so its value label has somewhere to sit. Load-bearing
 * beyond aesthetics: the minimum-bar-height floor (BAR_MIN_PX) is applied AFTER the axis is
 * chosen, so it can push the tallest bar above axisMax and clip it unless this headroom
 * covers it. Exported so the invariant
 *   BAR_MIN_PX / plotHeightPx < 1 - 1/AXIS_HEADROOM
 * can be asserted against the chart component's real geometry (PLOT_HEIGHT in
 * SavingsLedgerWaterfall.vue), see savingsLedgerWaterfall.test.ts. Raising BAR_MIN_PX or
 * shrinking the chart without re-checking it silently clips the tallest bar, which is a
 * wrong picture, not a cosmetic regression. */
export const AXIS_HEADROOM = 1.05;
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
 * A whole-euro tick scale covering every bar with a little headroom: the smallest
 * whole-unit step that fits the chart inside AXIS_MAX_SPLITS ticks, pinned at an exact
 * multiple of it so the labels read "0, 2, 4, 6, 8" rather than ECharts' "14.81". Where
 * layout.origin is negative the ticks stay evenly spaced but the labels are offset by it
 * and so are not themselves round: the honest figure wins over the round one.
 */
export function waterfallAxis(layout: WaterfallLayout): WaterfallAxis {
  const top = Math.max(0, ...layout.columns.map((c) => c.base + c.span - layout.origin));
  const needed = top * AXIS_HEADROOM;
  // a non-finite top has no honest scale; every other case is covered by the loop below,
  // which is unbounded on purpose. A bounded one needs a fallback return that no input can
  // reach, i.e. an untestable branch in the code that decides what a bar looks like.
  if (!(needed > 0) || !Number.isFinite(needed)) return { max: 1, interval: 1 };
  for (let exp = 0; ; exp++) {
    for (const mantissa of AXIS_STEP_MANTISSAS) {
      const interval = mantissa * Math.pow(10, exp);
      const splits = Math.ceil(needed / interval);
      if (splits <= AXIS_MAX_SPLITS) return { max: interval * splits, interval };
    }
  }
}

/** A bar smaller than this reads as a rule, and with the "estimated" dashed outline on it
 * as a dotted hairline with no fill at all. */
export const BAR_MIN_PX = 4;

/** BAR_MIN_PX expressed in the chart's own value units. */
export function minSpan(axisMax: number, plotHeightPx: number): number {
  if (!(axisMax > 0) || !(plotHeightPx > 0)) return 0;
  return (BAR_MIN_PX / plotHeightPx) * axisMax;
}

/**
 * Bar height in plotting coordinates, floored at `floor` so a small-but-nonzero
 * contribution still reads as a bar. A `zero: true` column is returned untouched: a measure
 * that moved nothing must never be drawn as though it had. The floor applies to the height
 * only, so a bar's top can overshoot its true level by up to BAR_MIN_PX; the connectors and
 * every printed number still come from the true levels.
 */
export function plotSpan(column: WaterfallColumn, floor: number): number {
  if (column.zero) return column.span;
  return Math.max(column.span, floor);
}
