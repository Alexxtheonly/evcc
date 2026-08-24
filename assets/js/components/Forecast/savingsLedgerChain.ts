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

// Sign convention for every contribution figure this module and savingsLedgerWaterfall.ts
// handle, straight from ledger_worlds.go's Contribution doc comment: Contribution =
// Cost(previous world) - Cost(this world). A measure that REDUCED cost (this world is
// cheaper) is POSITIVE - savings. A measure that made things WORSE (this world cost more
// than the previous one, e.g. Control when the real controller did worse than the W2 dumb
// rule) is NEGATIVE - overspend. Confirmed against TestChainOraclePerSlotEuros: Worlds =
// [1.76, 1.23, 0.0, 0.825] gives a Control contribution of 0.0 - 0.825 = -0.825, a real
// overspend case in that fixture.
//
// A measure under this magnitude renders as a rule, not a swatch - matches the Go side's
// own "drawn" filter in the (fake-data) mockup, applied here to real contributions.
export const ZERO_EPSILON_EUR = 0.005;

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
 * past the current slot boundary - the ledger has nothing to say about the future.
 *
 * Backwards is anchored on the window's own `from`, i.e. the period DISPLAYED, not on
 * its `to`: clampWindowToEarliest can move `from` forward (the default window narrows
 * itself to where the tariff history starts) while `to` stays where it was, so a step
 * anchored on `to` landed a full period before the *unnarrowed* start and left every day
 * in between unreachable. Forwards stays anchored on `to` - it is already bounded by now,
 * so it can neither skip days nor produce a zero-length window. */
export function shiftWindow(win: LedgerWindow, dir: 1 | -1, now: Date): LedgerWindow {
  const span = DEFAULT_WINDOW_DAYS * 24 * 60 * 60 * 1000;
  if (dir === -1) {
    return { from: new Date(win.from.getTime() - span), to: new Date(win.from.getTime()) };
  }
  const nowAligned = alignToSlotStart(now);
  let to = new Date(win.to.getTime() + span);
  if (to.getTime() > nowAligned.getTime()) to = nowAligned;
  return { from: new Date(to.getTime() - span), to };
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
export function clampWindowToEarliest(win: LedgerWindow, earliestRaw: string): LedgerWindow | null {
  const parsed = new Date(earliestRaw);
  if (Number.isNaN(parsed.getTime())) return null;

  const earliest = ceilToSlotStart(parsed);
  if (earliest.getTime() <= win.from.getTime()) return null;
  if (earliest.getTime() >= win.to.getTime()) return null;

  return { from: earliest, to: win.to };
}

/** True when the Control contribution (W2->W3, the real controller vs. the W2 dumb rule)
 * is a genuine overspend at the given headline. Drives the named clause under the chart:
 * a period where solar and the battery saved a great deal can legitimately headline
 * "saved 91 %" while this step lost money, and nothing else on the card says so. Absent
 * Control (no battery) is never a loss: there's nothing for software to have gotten
 * wrong. */
export function isControlOverspend(chain: LedgerChain, headline: SettlementHeadline): boolean {
  const control = chain.contributions.find((c) => c.label === "Control");
  if (!control) return false;
  return pickSettled(control.settled, headline) < -ZERO_EPSILON_EUR;
}
