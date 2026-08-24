// Pure transforms for the ADR-011 savings ledger chain bar and period window. Kept
// framework-free for unit testing (mirrors the Battery/history.ts and
// Forecast/optimizerOverlay.ts pattern).
//
// Grounded in core/metrics/ledger_worlds.go, read in full before writing this file -
// see savingsLedger.types.ts's field-level comments for the Go source each type mirrors.

import type { LedgerChain, LedgerCoverage, LedgerSettled } from "./savingsLedger.types";

export const SLOT_MINUTES = 15;

/** Which of Settled's two prices is shown as the headline figure. The API has no
 * "this site settles per-slot" declaration (isDynamicTariff only reports that the price
 * series varies, not how the site is billed - see ADR-011's Settlement section), so a
 * generic, upstreamable UI cannot default to per-slot the way the mockup's site-specific
 * narrative does. periodAverage is the conservative default; perSlot is one toggle away. */
export type SettlementHeadline = "perSlot" | "periodAverage";

export function pickSettled(s: LedgerSettled, headline: SettlementHeadline): number {
  return headline === "perSlot" ? s.perSlot : s.periodAverage;
}

// a measure under this magnitude renders as a rule, not a swatch - matches the Go side's
// own "drawn" filter in the (fake-data) mockup, applied here to real contributions.
export const ZERO_EPSILON_EUR = 0.005;

export type ChainSegmentKind = "measured" | "estimated" | "zero";
export type ChainSegmentKey = "pv" | "battery" | "routing" | "timing" | "control";

export interface ChainSegment {
  key: ChainSegmentKey;
  eur: number; // signed: positive = this measure cost money, negative = it saved money
  kind: ChainSegmentKind;
  overspend: boolean;
}

// Sign convention, straight from ledger_worlds.go's Contribution doc comment:
// Contribution = Cost(previous world) - Cost(this world). A measure that REDUCED cost
// (this world is cheaper) is POSITIVE - savings. A measure that made things WORSE (this
// world cost more than the previous one, e.g. Control when the real controller did
// worse than the W2 dumb rule) is NEGATIVE - overspend. Confirmed against
// TestChainOraclePerSlotEuros: Worlds = [1.76, 1.23, 0.0, 0.825] gives a Control
// contribution of 0.0 - 0.825 = -0.825, a real overspend case in that fixture.
function segment(key: ChainSegmentKey, eur: number, kind: ChainSegmentKind): ChainSegment {
  const zero = Math.abs(eur) <= ZERO_EPSILON_EUR;
  return { key, eur, kind: zero ? "zero" : kind, overspend: eur < -ZERO_EPSILON_EUR };
}

/**
 * The chain bar's segments, cost-descending: PV, Battery, then - ONLY at the perSlot
 * headline - Routing and Timing split out of Control. Control is not split at the
 * periodAverage headline because control.routing/control.timing are not a per-lens
 * pair like every other figure here: Routing is always the period-average-lens diff and
 * Timing is always Full (the perSlot-lens diff) minus Routing (ledger_worlds.go's
 * ControlSplit doc comment). Routing+Timing sums to Full, which is the perSlot Control
 * contribution - splitting it under a periodAverage headline would render two numbers
 * that don't sum to that headline's own Control figure, breaking the "parts sum to the
 * whole" guarantee the chain exists to demonstrate. At periodAverage, Control is shown
 * as one segment instead.
 *
 * PV is labelled "measured": it's arithmetic over directly measured PV/grid energy and
 * realised prices, no derived battery assumption involved. Battery and Control/Routing/
 * Timing are "estimated" whenever the site has a battery (chain.batteryPhysics is set)
 * because W2 depends on it - deriveBatteryPhysics's Source strings say whether that's a
 * device-reported fact or a fallback derived from history; this module doesn't fabricate
 * a numeric error bar the API doesn't provide (see LedgerBatteryPhysics's doc comment).
 */
export function chainSegments(chain: LedgerChain, headline: SettlementHeadline): ChainSegment[] {
  const pv = chain.contributions.find((c) => c.label === "PV");
  const battery = chain.contributions.find((c) => c.label === "Battery");
  const control = chain.contributions.find((c) => c.label === "Control");

  const segments: ChainSegment[] = [];
  if (pv) segments.push(segment("pv", pickSettled(pv.settled, headline), "measured"));
  if (battery) {
    const kind: ChainSegmentKind = chain.batteryPhysics ? "estimated" : "zero";
    segments.push(segment("battery", pickSettled(battery.settled, headline), kind));
  }
  if (headline === "perSlot" && chain.control) {
    segments.push(segment("routing", chain.control.routing, "estimated"));
    segments.push(segment("timing", chain.control.timing, "estimated"));
  } else if (headline === "periodAverage" && control) {
    segments.push(segment("control", pickSettled(control.settled, headline), "estimated"));
  }
  return segments;
}

/** Segments whose magnitude clears ZERO_EPSILON_EUR - what the bar geometry actually
 * draws as a swatch; a "zero" segment still appears as a row with a rule, never a bar. */
export function drawnSegments(segments: ChainSegment[]): ChainSegment[] {
  return segments.filter((s) => s.kind !== "zero");
}

export interface ChainBarPart {
  key: ChainSegmentKey;
  x0: number; // fraction of W0, left edge
  x1: number; // fraction of W0, right edge (x1 >= x0 always)
  eur: number;
  kind: ChainSegmentKind;
  overspend: boolean;
}

export interface ChainBarLayout {
  parts: ChainBarPart[]; // drawn (non-zero) segments, in causal order
  residualX0: number; // fraction of W0
  residualWidth: number; // fraction of W0
}

/**
 * Waterfall geometry: a bridge chart, not a simple left-to-right append. Each segment's
 * rectangle spans [cumulative-before, cumulative-after], where cumulative moves by the
 * segment's SIGNED eur/W0 - a savings segment (positive) advances the cursor right, an
 * overspend segment (negative) draws its rectangle back over ground already covered,
 * moving the cursor left. This is the only construction that satisfies "the parts
 * visibly sum to the whole" (task requirement) exactly for every sign combination:
 * cumulative after every segment equals (W0-paid)/W0 by the same telescoping identity
 * Contribution sums always satisfy (Cost(W0)-Cost(prev))+...+Cost(actual)=Cost(W0)), so
 * residualWidth = 1-cumulative is exactly paid/W0, never fudged.
 *
 * Deliberately NOT the mockup's approach (append every segment left-to-right by
 * |eur| regardless of sign, draw the residual at its own independent paid/W0 width):
 * verified against the mockup's own "a week the control loop lost money" dataset, that
 * geometry sums to W0 + 2x|overspend| instead of W0 - a real bug in the reference, not
 * something to replicate. Resolved in favour of ADR-011's own "parts sum to the whole"
 * requirement over matching the mockup's exact geometry.
 *
 * Returns null when totalCostW0 <= 0 (no meaningful bar - not reachable on a normal
 * site, since W0 prices every kWh of load with no offset at all).
 */
export function chainBarLayout(
  segments: ChainSegment[],
  totalCostW0: number
): ChainBarLayout | null {
  if (totalCostW0 <= 0) return null;
  let cumulative = 0;
  const parts: ChainBarPart[] = drawnSegments(segments).map((s) => {
    const before = cumulative;
    cumulative += s.eur / totalCostW0;
    return {
      key: s.key,
      x0: Math.min(before, cumulative),
      x1: Math.max(before, cumulative),
      eur: s.eur,
      kind: s.kind,
      overspend: s.overspend,
    };
  });
  // clamped at 0: an extreme period where overspends alone exceed every measured
  // saving (paid < 0, a net credit) draws a zero-width residual rather than a negative
  // one - a known simplification, not expected on a site with a nonzero feed-in price.
  return { parts, residualX0: cumulative, residualWidth: Math.max(0, 1 - cumulative) };
}

/** Coverage differs between the headline (realised, grid+home+tariff only) and the
 * chain (also needs PV/battery/loadpoint - see buildLedgerSlots's includeLoadpoint/
 * includeBattery doc comment) whenever the site has degraded PV/battery/EV data that
 * the plain grid meter doesn't need. Returns null when they match (the common case),
 * so a caller only renders the extra note when there's something to explain. */
export function coverageDivergence(
  headline: LedgerCoverage,
  chain: LedgerCoverage
): { headlineFraction: number; chainFraction: number } | null {
  if (headline.totalSlots === 0 && chain.totalSlots === 0) return null;
  if (headline.validSlots === chain.validSlots && headline.totalSlots === chain.totalSlots) {
    return null;
  }
  return { headlineFraction: headline.fraction, chainFraction: chain.fraction };
}

// --- period window -----------------------------------------------------------------

export const DEFAULT_WINDOW_DAYS = 7;

/** Truncate to the current 15-minute slot start, in local wall-clock time. Safe against
 * ErrLedgerRangeUnaligned (core/metrics/ledger_slots.go) as long as the viewer's UTC
 * offset is itself a multiple of 15 minutes, true for every real-world timezone. */
export function alignToSlotStart(d: Date): Date {
  const aligned = new Date(d);
  aligned.setSeconds(0, 0);
  aligned.setMinutes(Math.floor(aligned.getMinutes() / SLOT_MINUTES) * SLOT_MINUTES);
  return aligned;
}

/** Ceiling counterpart to alignToSlotStart: rounds UP to the next 15-minute slot
 * boundary, or returns the same instant unchanged if it's already exactly on one.
 * alignToSlotStart (floor) would be dishonest for clampWindowToEarliest below - flooring
 * the backend's earliest-available instant could land back before the first priced slot
 * and get refused all over again. */
export function ceilToSlotStart(d: Date): Date {
  const floored = alignToSlotStart(d);
  if (floored.getTime() === d.getTime()) return floored;
  return new Date(floored.getTime() + SLOT_MINUTES * 60 * 1000);
}

export interface LedgerWindow {
  from: Date;
  to: Date;
}

/** The default period: the DEFAULT_WINDOW_DAYS ending at the current slot boundary. */
export function defaultWindow(now: Date): LedgerWindow {
  const to = alignToSlotStart(now);
  const from = new Date(to.getTime() - DEFAULT_WINDOW_DAYS * 24 * 60 * 60 * 1000);
  return { from, to };
}

/** Page the window by one period's worth of days. Positive dir pages forward but never
 * past the current slot boundary - the ledger has nothing to say about the future. */
export function shiftWindow(win: LedgerWindow, dir: 1 | -1, now: Date): LedgerWindow {
  const ms = DEFAULT_WINDOW_DAYS * 24 * 60 * 60 * 1000 * dir;
  const nowAligned = alignToSlotStart(now);
  let to = new Date(win.to.getTime() + ms);
  if (to.getTime() > nowAligned.getTime()) to = nowAligned;
  const from = new Date(to.getTime() - DEFAULT_WINDOW_DAYS * 24 * 60 * 60 * 1000);
  return { from, to };
}

export function isWindowAtPresent(win: LedgerWindow, now: Date): boolean {
  return win.to.getTime() >= alignToSlotStart(now).getTime();
}

/** Narrows a refused window's `from` forward to the earliest instant the backend says it
 * actually has tariff data for - the `earliest` field on a 422 ErrBeforeTariffStart body
 * (server/http_savings_ledger_handler.go's savingsLedgerErrorBody), consumed by
 * SavingsLedgerCard.vue's fetch() ONLY for the default/present window, never a window the
 * user explicitly paged to (see that file for why). Ceiling-aligned via ceilToSlotStart so
 * the retried request can't land on an unpriced instant and get refused again.
 *
 * Returns null - caller must fall back to the plain refusal, never fabricate a window -
 * whenever clamping wouldn't honestly help: earliestRaw doesn't parse; the ceiling-aligned
 * earliest isn't strictly before win.to (nothing left in the window to show); or it isn't
 * strictly after win.from (clamping wouldn't narrow anything - the original refusal
 * already told the whole story). */
export function clampWindowToEarliest(
  win: LedgerWindow,
  earliestRaw: string
): LedgerWindow | null {
  const parsed = new Date(earliestRaw);
  if (Number.isNaN(parsed.getTime())) return null;

  const earliest = ceilToSlotStart(parsed);
  if (earliest.getTime() <= win.from.getTime()) return null;
  if (earliest.getTime() >= win.to.getTime()) return null;

  return { from: earliest, to: win.to };
}

/** True when the Control contribution (W2->W3, the real controller vs. the W2 dumb
 * rule) is a genuine overspend at the given headline - drives the loss banner. Absent
 * Control (no battery) is never a loss: there's nothing for software to have gotten
 * wrong. */
export function isControlOverspend(chain: LedgerChain, headline: SettlementHeadline): boolean {
  const control = chain.contributions.find((c) => c.label === "Control");
  if (!control) return false;
  return pickSettled(control.settled, headline) < -ZERO_EPSILON_EUR;
}
