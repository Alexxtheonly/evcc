import { describe, it, expect } from "vite-plus/test";
import {
  chainSegments,
  drawnSegments,
  chainBarLayout,
  coverageDivergence,
  isControlOverspend,
  alignToSlotStart,
  ceilToSlotStart,
  defaultWindow,
  shiftWindow,
  isWindowAtPresent,
  clampWindowToEarliest,
  pickSettled,
  ZERO_EPSILON_EUR,
  type ChainSegment,
} from "./savingsLedgerChain";
import type { LedgerChain, LedgerCoverage } from "./savingsLedger.types";

const settled = (perSlot: number, periodAverage = perSlot) => ({ perSlot, periodAverage });

// Sign convention (ledger_worlds.go's Contribution doc comment, confirmed against
// TestChainOraclePerSlotEuros): Contribution = Cost(previous world) - Cost(this world).
// Costs (Worlds[].Settled) are positive money paid; a measure that reduced cost is a
// POSITIVE contribution (savings), one that made things worse is NEGATIVE (overspend).
function baseChain(overrides: Partial<LedgerChain> = {}): LedgerChain {
  return {
    worlds: [
      { label: "W0", settled: settled(44.24) },
      { label: "W1", settled: settled(13.91) },
      { label: "W2", settled: settled(6.25) },
      { label: "W3", settled: settled(2.83) },
    ],
    contributions: [
      { label: "PV", settled: settled(30.33) }, // W0-W1: savings
      { label: "Battery", settled: settled(7.66) }, // W1-W2: savings
      { label: "Control", settled: settled(3.42, 0.32) }, // W2-W3: savings (real controller beat the dumb rule)
    ],
    coverage: { validSlots: 651, totalSlots: 672, fraction: 651 / 672 },
    meterResidual: { sumKWh: 0.1, absSumKWh: 4.2, slots: 651 },
    ...overrides,
  };
}

describe("chainSegments", () => {
  it("splits Control into routing+timing at the perSlot headline, summing to Control.perSlot", () => {
    const chain = baseChain({
      batteryPhysics: { capacityKWh: 19.3 } as any,
      control: { full: 3.42, routing: 0.32, timing: 3.1 },
    });
    const segs = chainSegments(chain, "perSlot");
    expect(segs.map((s) => s.key)).toEqual(["pv", "battery", "routing", "timing"]);
    const routing = segs.find((s) => s.key === "routing")!;
    const timing = segs.find((s) => s.key === "timing")!;
    expect(routing.eur + timing.eur).toBeCloseTo(3.42, 5);
  });

  it("does not split Control at the periodAverage headline - routing+timing don't sum to it", () => {
    const chain = baseChain({
      batteryPhysics: { capacityKWh: 19.3 } as any,
      control: { full: 3.42, routing: 0.32, timing: 3.1 },
    });
    const segs = chainSegments(chain, "periodAverage");
    expect(segs.map((s) => s.key)).toEqual(["pv", "battery", "control"]);
    expect(segs.find((s) => s.key === "control")!.eur).toBeCloseTo(0.32, 5);
  });

  it("marks PV measured and Battery/Control estimated when battery physics is present", () => {
    const chain = baseChain({
      batteryPhysics: { capacityKWh: 19.3 } as any,
      control: { full: 3.42, routing: 0.32, timing: 3.1 },
    });
    const segs = chainSegments(chain, "perSlot");
    expect(segs.find((s) => s.key === "pv")!.kind).toBe("measured");
    expect(segs.find((s) => s.key === "battery")!.kind).toBe("estimated");
    expect(segs.find((s) => s.key === "routing")!.kind).toBe("estimated");
  });

  it("renders the battery segment as zero-kind, not estimated, when there is no battery", () => {
    const chain = baseChain({
      contributions: [
        { label: "PV", settled: settled(30.33) },
        { label: "Battery", settled: settled(0) },
      ],
    });
    const segs = chainSegments(chain, "perSlot");
    expect(segs).toHaveLength(2); // pv + battery, no routing/timing (no control at all)
    expect(segs.find((s) => s.key === "battery")!.kind).toBe("zero");
  });

  it("flags overspend only below the negative zero epsilon - a NEGATIVE contribution means this measure cost money", () => {
    const chain = baseChain({
      contributions: [
        { label: "PV", settled: settled(-0.5) }, // PV made things worse this period
        { label: "Battery", settled: settled(-0.001) }, // sub-epsilon, renders as zero regardless of sign
      ],
    });
    const segs = chainSegments(chain, "perSlot");
    expect(segs.find((s) => s.key === "pv")).toMatchObject({ overspend: true, kind: "measured" });
    expect(segs.find((s) => s.key === "battery")).toMatchObject({
      overspend: false,
      kind: "zero",
    });
    expect(Math.abs(-0.001)).toBeLessThan(ZERO_EPSILON_EUR);
  });

  it("a positive (savings) contribution is never flagged overspend", () => {
    const chain = baseChain();
    const segs = chainSegments(chain, "periodAverage");
    expect(segs.every((s) => !s.overspend)).toBe(true);
  });
});

describe("drawnSegments", () => {
  it("excludes zero-kind segments from the bar geometry", () => {
    const chain = baseChain({
      contributions: [
        { label: "PV", settled: settled(30.33) },
        { label: "Battery", settled: settled(0) },
      ],
    });
    const segs = chainSegments(chain, "perSlot");
    expect(drawnSegments(segs).map((s) => s.key)).toEqual(["pv"]);
  });
});

describe("chainBarLayout", () => {
  it("all-savings period: parts sum exactly to W0, residual matches paid", () => {
    const chain = baseChain({
      batteryPhysics: { capacityKWh: 19.3 } as any,
      control: { full: 3.42, routing: 0.32, timing: 3.1 },
    });
    const segs = chainSegments(chain, "perSlot");
    const w0 = chain.worlds[0]!.settled.perSlot; // 44.24
    const paid = chain.worlds[3]!.settled.perSlot; // 2.83
    const layout = chainBarLayout(segs, w0)!;
    expect(layout.parts).toHaveLength(4);
    // every part is drawn forward (savings), so x0/x1 are monotonically increasing
    let prevX1 = 0;
    for (const p of layout.parts) {
      expect(p.x0).toBeCloseTo(prevX1, 6);
      expect(p.overspend).toBe(false);
      prevX1 = p.x1;
    }
    expect(layout.residualX0).toBeCloseTo(prevX1, 6);
    expect(layout.residualX0 + layout.residualWidth).toBeCloseTo(1, 6);
    expect(layout.residualWidth * w0).toBeCloseTo(paid, 2);
  });

  it("a lossy period: the overspend segment draws backward, and everything still sums to exactly W0", () => {
    // the mockup's own "week the control loop lost money" dataset, converted to the
    // real API's sign convention (positive = savings): W0=44.24, PV=+30.33,
    // Battery=+7.66, Routing=+0.15, Timing=-0.95 (a genuine overspend) -> paid=7.05
    const chain: LedgerChain = {
      worlds: [
        { label: "W0", settled: settled(44.24) },
        { label: "W1", settled: settled(13.91) },
        { label: "W2", settled: settled(6.25) },
        { label: "W3", settled: settled(7.05) },
      ],
      contributions: [
        { label: "PV", settled: settled(30.33) },
        { label: "Battery", settled: settled(7.66) },
        { label: "Control", settled: settled(-0.8, 0.15) }, // full = routing+timing = 0.15-0.95
      ],
      coverage: { validSlots: 672, totalSlots: 672, fraction: 1 },
      meterResidual: { sumKWh: 0, absSumKWh: 0, slots: 672 },
      batteryPhysics: { capacityKWh: 19.3 } as any,
      control: { full: -0.8, routing: 0.15, timing: -0.95 },
    };
    const segs = chainSegments(chain, "perSlot");
    const timing = segs.find((s) => s.key === "timing")!;
    expect(timing.overspend).toBe(true);

    const w0 = 44.24;
    const paid = 7.05;
    const layout = chainBarLayout(segs, w0)!;
    const timingPart = layout.parts.find((p) => p.key === "timing")!;
    // drawn backward: its width still equals |eur|/W0, but x0 < x1 always (min/max), and
    // it does NOT extend the cursor forward the way a savings segment would
    expect(timingPart.x1 - timingPart.x0).toBeCloseTo(0.95 / w0, 6);

    // the whole-bar identity: every drawn part's width plus the residual sums to
    // exactly 1 (=W0), never W0 + 2x|overspend| the way the mockup's own geometry does
    const cumulative = layout.residualX0;
    expect(cumulative).toBeCloseTo((w0 - paid) / w0, 6);
    expect(layout.residualWidth).toBeCloseTo(paid / w0, 6);
    expect(cumulative + layout.residualWidth).toBeCloseTo(1, 6);
  });

  it("returns null for a non-positive W0 (no meaningful bar)", () => {
    expect(chainBarLayout([], 0)).toBeNull();
    expect(chainBarLayout([], -1)).toBeNull();
  });

  it("net-credit period (Σcontributions > totalCostW0, paid < 0): residualWidth clamps to 0 without throwing", () => {
    // synthetic, not reachable from this site's real data (€0 feed-in), but the clamp
    // at Math.max(0, 1 - cumulative) exists for exactly this case and had zero test
    // coverage of any kind before this one - UAT-identified gap.
    const segments: ChainSegment[] = [
      { key: "pv", eur: 8, kind: "measured", overspend: false },
      { key: "battery", eur: 5, kind: "estimated", overspend: false },
    ];
    const w0 = 10; // Σcontributions (13) > w0 (10) -> paid = w0 - 13 = -3, a net credit
    expect(() => chainBarLayout(segments, w0)).not.toThrow();
    const layout = chainBarLayout(segments, w0);
    expect(layout).not.toBeNull();
    expect(layout!.residualWidth).toBe(0);
    // the parts themselves are untouched by the clamp - only the residual is
    expect(layout!.residualX0).toBeCloseTo(1.3, 6);
  });
});

describe("isControlOverspend", () => {
  it("is false when Control is positive (savings) or absent", () => {
    expect(isControlOverspend(baseChain(), "perSlot")).toBe(false);
    expect(
      isControlOverspend(
        baseChain({ contributions: [{ label: "PV", settled: settled(1) }] }),
        "perSlot"
      )
    ).toBe(false);
  });

  it("is true when Control is a genuine negative contribution at the given headline", () => {
    const chain = baseChain({
      contributions: [
        { label: "PV", settled: settled(30.33) },
        { label: "Battery", settled: settled(7.66) },
        { label: "Control", settled: settled(-0.95, -0.15) },
      ],
    });
    expect(isControlOverspend(chain, "perSlot")).toBe(true);
    expect(isControlOverspend(chain, "periodAverage")).toBe(true);
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
  const win = { from: new Date("2026-08-17T00:00:00.000Z"), to: new Date("2026-08-24T00:00:00.000Z") };

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
