import { readFileSync } from "node:fs";
import { describe, it, expect } from "vite-plus/test";
import {
  coverageDivergence,
  EV_TIMING_NOTE,
  alignToSlotStart,
  ceilToSlotStart,
  defaultWindow,
  shiftWindow,
  isWindowAtPresent,
  clampWindowToEarliest,
  pickSettled,
} from "./savingsLedgerChain";
import type { LedgerCoverage } from "./savingsLedger.types";

const settled = (perSlot: number, periodAverage = perSlot) => ({ perSlot, periodAverage });

// The UI recognises one backend note by its text (EV_TIMING_NOTE's doc comment says why
// it has to). This is the tie that makes that coupling loud: it reads the Go source and
// fails the moment noteEVTimingUnattributed is reworded, so the caption under the chart
// cannot silently stop rendering on a site that has a loadpoint.
describe("EV_TIMING_NOTE", () => {
  it("is exactly core/metrics/ledger_worlds.go's noteEVTimingUnattributed", () => {
    // relative to the repo root, which is where the test runner is started from
    const go = readFileSync("core/metrics/ledger_worlds.go", "utf8");
    const match = go.match(/const noteEVTimingUnattributed = "((?:[^"\\]|\\.)*)"/);
    expect(match, "noteEVTimingUnattributed not found - was it renamed or moved?").not.toBeNull();
    expect(match![1]).toBe(EV_TIMING_NOTE);
  });
});

describe("coverageDivergence", () => {
  it("returns null when headline and chain coverage agree", () => {
    const cov: LedgerCoverage = { validSlots: 672, totalSlots: 672, fraction: 1 };
    expect(coverageDivergence(cov, cov)).toBeNull();
  });

  it("returns both fractions when the chain's stricter slot filter dropped more slots", () => {
    const headline: LedgerCoverage = { validSlots: 670, totalSlots: 672, fraction: 670 / 672 };
    const chain: LedgerCoverage = { validSlots: 651, totalSlots: 672, fraction: 651 / 672 };
    const d = coverageDivergence(headline, chain);
    expect(d).not.toBeNull();
    expect(d!.headlineFraction).toBeCloseTo(670 / 672, 5);
    expect(d!.chainFraction).toBeCloseTo(651 / 672, 5);
  });
});

describe("pickSettled", () => {
  it("selects the requested lens", () => {
    const s = settled(1, 2);
    expect(pickSettled(s, "perSlot")).toBe(1);
    expect(pickSettled(s, "periodAverage")).toBe(2);
  });
});

describe("period window", () => {
  it("aligns to the 15-minute slot start", () => {
    const d = new Date("2026-08-23T13:07:42.123Z");
    const aligned = alignToSlotStart(d);
    expect(aligned.getSeconds()).toBe(0);
    expect(aligned.getMilliseconds()).toBe(0);
    expect(aligned.getMinutes() % 15).toBe(0);
  });

  it("defaultWindow spans 7 days ending at the aligned now", () => {
    const now = new Date("2026-08-23T13:07:00.000Z");
    const win = defaultWindow(now);
    expect(win.to.getTime() - win.from.getTime()).toBe(7 * 24 * 60 * 60 * 1000);
    expect(win.to.getTime()).toBeLessThanOrEqual(now.getTime());
  });

  it("shiftWindow(-1) pages one period back", () => {
    const now = new Date("2026-08-23T00:00:00.000Z");
    const win = defaultWindow(now);
    const prev = shiftWindow(win, -1, now);
    expect(prev.to.getTime()).toBe(win.from.getTime());
  });

  // D2: the default window auto-narrows its `from` to where the tariff history starts
  // (clampWindowToEarliest, below), so the window on screen is shorter than a period.
  // Paging back must step from THAT start - anchoring on `to` skipped the days between.
  it("shiftWindow(-1) steps from the displayed start of an auto-narrowed window", () => {
    const now = new Date("2026-08-24T08:15:00.000Z");
    const narrowed = clampWindowToEarliest(defaultWindow(now), "2026-08-21T10:30:00.000Z");
    expect(narrowed).not.toBeNull();
    const prev = shiftWindow(narrowed!, -1, now);
    expect(prev.to.toISOString()).toBe("2026-08-21T10:30:00.000Z");
    expect(prev.from.toISOString()).toBe("2026-08-14T10:30:00.000Z");
  });

  it("shiftWindow(1) never pages the window's end past now", () => {
    const now = new Date("2026-08-23T00:00:00.000Z");
    const win = defaultWindow(now);
    const next = shiftWindow(win, 1, now);
    expect(next.to.getTime()).toBe(alignToSlotStart(now).getTime());
    expect(isWindowAtPresent(next, now)).toBe(true);
  });
});

describe("ceilToSlotStart", () => {
  it("rounds up to the next 15-minute slot boundary", () => {
    const d = new Date("2026-08-23T13:07:42.123Z");
    const ceiled = ceilToSlotStart(d);
    expect(ceiled.getTime()).toBeGreaterThan(d.getTime());
    expect(ceiled.getSeconds()).toBe(0);
    expect(ceiled.getMilliseconds()).toBe(0);
    expect(ceiled.getMinutes() % 15).toBe(0);
    // exactly one slot past the floor of the same instant, never more
    expect(ceiled.getTime() - alignToSlotStart(d).getTime()).toBe(15 * 60 * 1000);
  });

  it("leaves an instant already on a slot boundary unchanged - no spurious extra slot", () => {
    const d = new Date("2026-08-21T12:30:00.000Z");
    expect(ceilToSlotStart(d).getTime()).toBe(d.getTime());
  });
});

describe("clampWindowToEarliest", () => {
  const win = {
    from: new Date("2026-08-17T00:00:00.000Z"),
    to: new Date("2026-08-24T00:00:00.000Z"),
  };

  it("clamps from forward to the ceiling-aligned earliest, keeping to unchanged", () => {
    const clamped = clampWindowToEarliest(win, "2026-08-21T12:30:00+02:00");
    expect(clamped).not.toBeNull();
    // 2026-08-21T12:30:00+02:00 = 2026-08-21T10:30:00Z, already slot-aligned
    expect(clamped!.from.getTime()).toBe(new Date("2026-08-21T10:30:00.000Z").getTime());
    expect(clamped!.to.getTime()).toBe(win.to.getTime());
  });

  it("earliest already on a slot boundary adds no extra slot", () => {
    const clamped = clampWindowToEarliest(win, "2026-08-21T10:30:00.000Z");
    expect(clamped!.from.getTime()).toBe(new Date("2026-08-21T10:30:00.000Z").getTime());
  });

  it("returns null when the earliest instant wouldn't actually narrow the window (at/before win.from)", () => {
    expect(clampWindowToEarliest(win, "2026-08-10T00:00:00.000Z")).toBeNull();
    expect(clampWindowToEarliest(win, win.from.toISOString())).toBeNull();
  });

  it("returns null when the earliest instant leaves no window at all (at/after win.to)", () => {
    expect(clampWindowToEarliest(win, "2026-08-25T00:00:00.000Z")).toBeNull();
    expect(clampWindowToEarliest(win, win.to.toISOString())).toBeNull();
  });

  it("returns null for an unparseable earliest string", () => {
    expect(clampWindowToEarliest(win, "not-a-date")).toBeNull();
    expect(clampWindowToEarliest(win, "")).toBeNull();
  });
});
