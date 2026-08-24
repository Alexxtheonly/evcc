import { shallowMount, config } from "@vue/test-utils";
import { describe, it, expect } from "vite-plus/test";
import en from "../../../../i18n/en.json";
import SavingsLedgerWaterfall from "./SavingsLedgerWaterfall.vue";
import { batteryColor } from "@/colors";
import { waterfallLayout, plotBase, WATERFALL_KEYS } from "./savingsLedgerWaterfall";
import type { LedgerChain } from "./savingsLedger.types";
// A real GET /api/savingsledger response from the owner's site (2 days, 2026-08-22..24).
// Kept as a fixture rather than hand-written numbers because it carries a genuine
// Control overspend at BOTH lenses, which is the case the diagram exists to render
// honestly (ADR-011 rule 1) and the one hand-rolled fixtures kept getting wrong.
import liveSample from "./__fixtures__/ledgerLiveSample";

const live = liveSample.chain as LedgerChain;

const settled = (perSlot: number, periodAverage = perSlot) => ({ perSlot, periodAverage });

function baseChain(overrides: Partial<LedgerChain> = {}): LedgerChain {
  return {
    worlds: [
      { label: "W0", settled: settled(44.24) },
      { label: "W1", settled: settled(13.91) },
      { label: "W2", settled: settled(6.25) },
      { label: "W3", settled: settled(2.83) },
    ],
    contributions: [
      { label: "PV", settled: settled(30.33) }, // W0-W1, saved
      { label: "Battery", settled: settled(7.66) }, // W1-W2, saved
      { label: "Control", settled: settled(3.42) }, // W2-W3, saved
    ],
    coverage: { validSlots: 651, totalSlots: 672, fraction: 651 / 672 },
    meterResidual: { sumKWh: 0.1, absSumKWh: 4.2, slots: 651 },
    batteryPhysics: {
      capacityKWh: 19.32,
      capacitySource: "device-reported capacity, persisted",
      etaC: 0.9,
      etaD: 0.9,
      etaSource: "constant (0.9), not derived",
      floorFrac: 0.041,
      floorSource: "lowest observed SoC in history",
      maxChargeKWh: 0.8,
      maxDischargeKWh: 0.52,
    },
    ...overrides,
  };
}

describe("waterfallLayout", () => {
  it("always emits the five columns in causal order", () => {
    const layout = waterfallLayout(baseChain(), "perSlot");
    expect(layout.columns.map((c) => c.key)).toEqual(WATERFALL_KEYS);
  });

  it("telescopes exactly: every level is that world's cost and the chain ends at W3", () => {
    const chain = baseChain();
    const layout = waterfallLayout(chain, "perSlot");
    const level = (key: string) => layout.columns.find((c) => c.key === key)!.level;

    expect(level("baseline")).toBeCloseTo(44.24, 12);
    expect(level("pv")).toBeCloseTo(13.91, 12); // W1
    expect(level("battery")).toBeCloseTo(6.25, 12); // W2
    expect(level("control")).toBeCloseTo(2.83, 12); // W3
    expect(layout.endLevel).toBeCloseTo(layout.paid, 12);
    expect(layout.saved).toBeCloseTo(44.24 - 2.83, 12);
    expect(layout.savedFraction).toBeCloseTo((44.24 - 2.83) / 44.24, 12);
  });

  it("telescopes exactly on the real API response, at both lenses", () => {
    for (const headline of ["perSlot", "periodAverage"] as const) {
      const layout = waterfallLayout(live, headline);
      const w = (i: number) => live.worlds[i]!.settled[headline];
      expect(layout.w0).toBe(w(0));
      expect(layout.paid).toBe(w(3));
      expect(layout.columns[1]!.level).toBeCloseTo(w(1), 12);
      expect(layout.columns[2]!.level).toBeCloseTo(w(2), 12);
      expect(layout.endLevel).toBeCloseTo(w(3), 12);
      // every middle bar spans exactly |contribution|
      for (const c of layout.columns.filter((x) => !x.total)) {
        expect(c.span).toBeCloseTo(Math.abs(c.eur), 12);
        expect(c.base + c.span).toBeCloseTo(Math.max(c.level, c.level + c.eur), 12);
      }
    }
  });

  it("draws a Control overspend as a rising bar and never clamps it", () => {
    const layout = waterfallLayout(live, "perSlot");
    const control = layout.columns.find((c) => c.key === "control")!;
    const battery = layout.columns.find((c) => c.key === "battery")!;

    expect(control.eur).toBeLessThan(0); // the real controller lost money this period
    expect(control.overspend).toBe(true);
    expect(control.zero).toBe(false);
    // rises: its bottom is the PREVIOUS level, its top the new (higher) one
    expect(control.base).toBeCloseTo(battery.level, 12);
    expect(control.base + control.span).toBeCloseTo(control.level, 12);
    expect(control.level).toBeGreaterThan(battery.level);
  });

  it("keeps a negative saved figure negative (no clamp to zero)", () => {
    const chain = baseChain({
      worlds: [
        { label: "W0", settled: settled(10) },
        { label: "W1", settled: settled(10) },
        { label: "W2", settled: settled(10) },
        { label: "W3", settled: settled(12) },
      ],
      contributions: [
        { label: "PV", settled: settled(0) },
        { label: "Battery", settled: settled(0) },
        { label: "Control", settled: settled(-2) },
      ],
    });
    const layout = waterfallLayout(chain, "perSlot");
    expect(layout.saved).toBe(-2);
    expect(layout.savedFraction).toBeCloseTo(-0.2, 12);
  });

  it("marks a zero-magnitude contribution as zero, with a zero-height bar", () => {
    const chain = baseChain({
      worlds: [
        { label: "W0", settled: settled(10) },
        { label: "W1", settled: settled(4) },
        { label: "W2", settled: settled(4) },
        { label: "W3", settled: settled(3) },
      ],
      contributions: [
        { label: "PV", settled: settled(6) },
        { label: "Battery", settled: settled(0.001) }, // below ZERO_EPSILON_EUR
        { label: "Control", settled: settled(1) },
      ],
    });
    const layout = waterfallLayout(chain, "perSlot");
    const battery = layout.columns.find((c) => c.key === "battery")!;
    expect(battery.zero).toBe(true);
    expect(battery.overspend).toBe(false);
    expect(battery.span).toBeCloseTo(0.001, 12);
  });

  it("emits absent contributions as zero columns rather than dropping them", () => {
    const chain = baseChain({
      worlds: [
        { label: "W0", settled: settled(10) },
        { label: "W1", settled: settled(4) },
        { label: "W2", settled: settled(4) },
        { label: "W3", settled: settled(4) },
      ],
      contributions: [{ label: "PV", settled: settled(6) }],
      batteryPhysics: undefined,
    });
    const layout = waterfallLayout(chain, "perSlot");
    expect(layout.columns.map((c) => c.key)).toEqual(WATERFALL_KEYS);
    for (const key of ["battery", "control"]) {
      const col = layout.columns.find((c) => c.key === key)!;
      expect(col.eur).toBe(0);
      expect(col.zero).toBe(true);
      expect(col.span).toBe(0);
    }
    expect(layout.endLevel).toBeCloseTo(4, 12);
  });

  it("marks nothing as estimated when batteryPhysics is absent", () => {
    const layout = waterfallLayout(baseChain({ batteryPhysics: undefined }), "perSlot");
    expect(layout.columns.every((c) => !c.estimated)).toBe(true);
  });

  it("marks battery and control - but not PV - as estimated when batteryPhysics is set", () => {
    const layout = waterfallLayout(baseChain(), "perSlot");
    const estimated = layout.columns.filter((c) => c.estimated).map((c) => c.key);
    expect(estimated).toEqual(["battery", "control"]);
  });

  it("shifts the plotting origin so a negative paid level is not clipped", () => {
    const chain = baseChain({
      worlds: [
        { label: "W0", settled: settled(10) },
        { label: "W1", settled: settled(2) },
        { label: "W2", settled: settled(-0.5) },
        { label: "W3", settled: settled(-0.5) },
      ],
      contributions: [
        { label: "PV", settled: settled(8) },
        { label: "Battery", settled: settled(2.5) },
        { label: "Control", settled: settled(0) },
      ],
    });
    const layout = waterfallLayout(chain, "perSlot");
    expect(layout.paid).toBe(-0.5);
    expect(layout.origin).toBeCloseTo(-0.5, 12);

    const paid = layout.columns.find((c) => c.key === "paid")!;
    expect(paid.base).toBe(-0.5); // spans [-0.5, 0], a credit hanging below the zero line
    expect(paid.span).toBe(0.5);

    // every bar plots at or above the shifted zero, so nothing is clipped and no stacked
    // bar ever needs a negative base
    for (const c of layout.columns) {
      expect(plotBase(c, layout)).toBeGreaterThanOrEqual(0);
    }
  });

  it("leaves the plotting origin at zero for a normal period", () => {
    const layout = waterfallLayout(live, "perSlot");
    expect(layout.origin).toBe(0);
    for (const c of layout.columns) {
      expect(plotBase(c, layout)).toBe(c.base);
    }
  });
});

// The chart wiring lives in the same file as the geometry it consumes: macOS'
// case-insensitive filesystem makes SavingsLedgerWaterfall.test.ts and
// savingsLedgerWaterfall.test.ts the same path, so they cannot be separate files.
// minimal $t/$i18n stand-ins, same pattern as SavingsLedgerCard.test.ts, so the
// assertions below read against the real English strings
const lookup = (key: string): string | undefined => {
  const v = key.split(".").reduce<any>((o, k) => o?.[k], en);
  return typeof v === "string" ? v : undefined;
};
config.global.mocks["$t"] = (key: string, params?: Record<string, unknown>) => {
  const v = lookup(key);
  if (typeof v !== "string") return key;
  if (!params) return v;
  return Object.entries(params).reduce((s, [k, val]) => s.replaceAll(`{${k}}`, String(val)), v);
};
config.global.mocks["$i18n"] = { locale: "en-US" };

function option(
  headline: "perSlot" | "periodAverage" = "periodAverage",
  chain: LedgerChain = live
): any {
  const wrapper = shallowMount(SavingsLedgerWaterfall, { props: { chain, headline } });
  return (wrapper.vm as any).chartOption;
}

describe("SavingsLedgerWaterfall chart option", () => {
  it("draws five categories as a transparent pedestal plus one visible series", () => {
    const o = option();
    expect(o.xAxis.data).toEqual(["baseline", "pv", "battery", "control", "paid"]);
    expect(o.series).toHaveLength(2);
    expect(o.series[0].itemStyle.color).toBe("transparent");
    expect(o.series[0].silent).toBe(true);
    expect(o.series[0].stack).toBe(o.series[1].stack);
    // pedestal and height are both non-negative, so the stack can never be split by
    // ECharts into separate positive/negative groups
    for (const base of o.series[0].data) expect(base).toBeGreaterThanOrEqual(0);
    for (const d of o.series[1].data) expect(d.value).toBeGreaterThanOrEqual(0);
  });

  it("carries the estimate marker in the axis label, not in a footnote", () => {
    const label = (key: string) => option().xAxis.axisLabel.formatter(key);
    expect(label("pv")).toBe("{n|solar}");
    expect(label("baseline")).toBe("{n|baseline}");
    expect(label("battery")).toBe("{n|battery}\n{e|estimated}");
    expect(label("control")).toBe("{n|control}\n{e|estimated}");
  });

  it("drops the estimate marker when the payload has no battery physics", () => {
    const noPhysics = { ...live, batteryPhysics: undefined };
    const label = (key: string) =>
      option("periodAverage", noPhysics).xAxis.axisLabel.formatter(key);
    expect(label("battery")).toBe("{n|battery}");
    expect(label("control")).toBe("{n|control}");
  });

  it("labels a saving as a fall in the bill and an overspend as a rise", () => {
    const o = option();
    const label = (i: number) => o.series[1].label.formatter({ dataIndex: i });
    expect(label(0)).toBe("€6.67"); // W0, a level
    expect(label(1)).toBe("-€3.95"); // solar took this off the bill
    expect(label(2)).toBe("-€2.52"); // battery took this off the bill
    expect(label(3)).toBe("+€0.41"); // control ADDED to it this period
    expect(label(4)).toBe("€0.60"); // W3, a level
  });

  it("colours an overspend apart from the palette and dashes the estimated columns", () => {
    // the CSS-variable-backed colours (colors.self/grid/price/danger) all read back as
    // "" under happy-dom, so this asserts on the two that don't: the battery palette,
    // and the fact that an overspend column is NOT drawn in its normal palette colour.
    const styles = option().series[1].data.map((d: any) => d.itemStyle);
    expect(styles[1].borderType).toBeUndefined(); // solar is measured
    expect(styles[2].borderType).toBe("dashed");
    expect(styles[3].borderType).toBe("dashed");
    expect(styles[2].color).toBe(batteryColor(0));
    expect(styles[3].color).not.toBe(batteryColor(1)); // control cost money here

    const saved: LedgerChain = {
      ...live,
      contributions: live.contributions.map((c) =>
        c.label === "Control" ? { label: "Control" as const, settled: settled(1) } : c
      ),
    };
    expect(option("periodAverage", saved).series[1].data[3].itemStyle.color).toBe(batteryColor(1));
  });

  it("shows the routing/timing split only under the per-slot headline", () => {
    const html = (headline: "perSlot" | "periodAverage") =>
      option(headline).tooltip.formatter({ dataIndex: 3 });
    expect(html("perSlot")).toContain("Routing");
    expect(html("perSlot")).toContain("Price timing");
    expect(html("periodAverage")).not.toContain("Routing");
    expect(html("periodAverage")).not.toContain("Price timing");
  });

  it("puts the real battery provenance in the estimated columns' tooltips", () => {
    const o = option();
    const battery = o.tooltip.formatter({ dataIndex: 2 });
    expect(battery).toContain("device-reported capacity, persisted");
    expect(battery).toContain("lowest observed SoC in history");
    expect(o.tooltip.formatter({ dataIndex: 1 })).not.toContain("device-reported capacity");
  });

  it("keeps the y axis pinned at zero and its labels in real money", () => {
    const o = option();
    expect(o.yAxis.min).toBe(0);
    expect(o.yAxis.axisLabel.formatter(2)).toBe("€2.00");
  });
});
