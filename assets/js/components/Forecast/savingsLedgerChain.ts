// Pure transforms for the savings ledger chain bar and period window. Kept framework-free
// for unit testing; mirrors core/metrics/ledger_worlds.go.

import type { LedgerChain, LedgerCoverage, LedgerSettled } from "./savingsLedger.types";

export const SLOT_MINUTES = 15;

/** Which of Settled's two prices is shown as the headline figure. The API has no "this site
 * settles per-slot" declaration (isDynamicTariff only reports that the price series varies,
 * not how the site is billed), so periodAverage is the conservative default and perSlot is
 * one toggle away. */
export type SettlementHeadline = "perSlot" | "periodAverage";

export function pickSettled(s: LedgerSettled, headline: SettlementHeadline): number {
  return headline === "perSlot" ? s.perSlot : s.periodAverage;
}

// A contribution under this magnitude renders as a rule, not a swatch. A DRAWING threshold,
// never an evidence one: half a cent is two orders of magnitude below the measurement
// uncertainty the same payload publishes, so a sign claim gated on it can be underwritten
// by nothing at all. Use contributionBand() for any sign claim.
export const ZERO_EPSILON_EUR = 0.005;

/** The smallest magnitude a contribution must reach before its SIGN may be asserted: the
 * period's own measurement noise (chain.meterResidual.eurBand), never below
 * ZERO_EPSILON_EUR so a period with a perfect residual still gets the drawing threshold. */
export function contributionBand(chain: LedgerChain): number {
  return Math.max(ZERO_EPSILON_EUR, chain.meterResidual?.eurBand ?? 0);
}

/** Coverage differs between the headline (grid+home+tariff only) and the chain (also needs
 * PV/battery/loadpoint) whenever the site has degraded data the plain grid meter doesn't
 * need. Null when they match, so a caller only renders the extra note when there is
 * something to explain. */
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

/** core/metrics/ledger_worlds.go's noteEVTimingUnattributed, byte for byte. The API has no
 * note IDs, so recognising this one means comparing prose; savingsLedgerChain.test.ts reads
 * that Go file and fails the build the moment the string is reworded, so the caption under
 * the chart can never silently stop rendering. Match it whole, never by a prefix: a
 * half-recognised note is a note whose meaning may already have moved. */
export const EV_TIMING_NOTE =
  "EV charge timing is not attributed to any measure - PV/Battery/Control all price a loadpoint's energy at when it was actually drawn, so shifting a charge to a cheaper slot shows EUR 0 of value here even when it saved money";

export const DEFAULT_WINDOW_DAYS = 7;

/** Truncate to the current 15-minute slot start, in local wall-clock time. Safe against
 * ErrLedgerRangeUnaligned as long as the viewer's UTC offset is itself a multiple of 15
 * minutes, true for every real-world timezone. */
export function alignToSlotStart(d: Date): Date {
  const aligned = new Date(d);
  aligned.setSeconds(0, 0);
  aligned.setMinutes(Math.floor(aligned.getMinutes() / SLOT_MINUTES) * SLOT_MINUTES);
  return aligned;
}

/** Ceiling counterpart to alignToSlotStart. Flooring would be dishonest for
 * clampWindowToEarliest below: it can land back before the first priced slot and get the
 * retried request refused all over again. */
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

/** Page the window by one period's worth of days, never past the current slot boundary.
 *
 * Backwards anchors on the window's own `from`, the period DISPLAYED, not on `to`:
 * clampWindowToEarliest can move `from` forward while `to` stays put, and a step anchored
 * on `to` then lands a full period before the unnarrowed start, leaving every day in
 * between unreachable. Forwards stays anchored on `to`, which is already bounded by now. */
export function shiftWindow(win: LedgerWindow, dir: 1 | -1, now: Date): LedgerWindow {
  const span = DEFAULT_WINDOW_DAYS * 24 * 60 * 60 * 1000;
  if (dir === -1) {
    return {
      from: new Date(win.from.getTime() - span),
      to: new Date(win.from.getTime()),
    };
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
 * has tariff data for (the `earliest` field on a 422 ErrBeforeTariffStart body).
 * Ceiling-aligned so the retried request cannot land on an unpriced instant and be refused
 * again.
 *
 * Returns null whenever clamping would not honestly help: earliestRaw does not parse, or
 * the aligned instant is not strictly inside (win.from, win.to). The caller must then fall
 * back to the plain refusal and never fabricate a window. */
export function clampWindowToEarliest(win: LedgerWindow, earliestRaw: string): LedgerWindow | null {
  const parsed = new Date(earliestRaw);
  if (Number.isNaN(parsed.getTime())) return null;

  const earliest = ceilToSlotStart(parsed);
  if (earliest.getTime() <= win.from.getTime()) return null;
  if (earliest.getTime() >= win.to.getTime()) return null;

  return { from: earliest, to: win.to };
}
