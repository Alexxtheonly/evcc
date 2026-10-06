import { describe, expect, test } from "vite-plus/test";
import {
  defaultVisibility,
  pastGridBlocks,
  pastSlotDetails,
  pastSocPoints,
  pastView,
  SLOT_MS,
} from "./planChartModel";
import { buildPlanChartOption, type ChartInput } from "./planChartOption";
import type {
  EnergyDevice,
  EnergyForecastSlot,
  HistorySlot,
  PlanHistory,
} from "./energyIntelligence";
import { planHistoryAfternoon } from "./__fixtures__/planHistoryLive";

const PLAN_START = "2026-10-05T11:00:27Z";
const planSlot = (start: string, end: string): EnergyForecastSlot => ({
  start,
  end,
  homeLowWh: 50,
  homeWh: 100,
  homeHighWh: 200,
  solarLowWh: 0,
  solarWh: 0,
  solarHighWh: 0,
  gridPrice: 0.3,
});
const slots = [
  planSlot(PLAN_START, "2026-10-05T11:15:00Z"),
  planSlot("2026-10-05T11:15:00Z", "2026-10-05T11:30:00Z"),
];
const battery: EnergyDevice = {
  key: "battery:db:11",
  name: "db:11",
  title: "Home battery",
  kind: "battery",
  capacityKWh: 10,
  initialSoc: 42,
  plan: [
    { start: slots[0]!.start, chargeWh: 0, dischargeWh: 0, soc: 42 },
    { start: slots[1]!.start, chargeWh: 0, dischargeWh: 0, soc: 42 },
  ],
};

const measured = (start: string, values: Partial<HistorySlot> = {}): HistorySlot => {
  const end = new Date(Date.parse(start) + SLOT_MS).toISOString();
  return {
    start,
    end,
    measured: true,
    homeWh: 200,
    solarWh: 0,
    gridImportWh: 200,
    gridExportWh: 0,
    batteryChargeWh: 0,
    batteryDischargeWh: 0,
    loadpointWh: 0,
    gridChargeWh: 0,
    gridPrice: 0.25,
    feedInPrice: 0.08,
    gridCost: 0.05,
    feedInRevenue: 0,
    ...values,
  };
};

// 10:00 - 11:00: a clean grid-charging slot, a flagged one, a gap, and the last slot
// before "now"
const history: PlanHistory = {
  from: "2026-10-05T10:00:00Z",
  to: "2026-10-05T11:00:00Z",
  forecast: [
    measured("2026-10-05T10:00:00Z", {
      batteryChargeWh: 1500,
      gridImportWh: 1700,
      gridChargeWh: 1500,
      gridCost: 0.425,
      planned: {
        snapshotId: 7,
        snapshot: "2026-10-05T09:45:32Z",
        gridChargeMinWh: 0,
        gridChargeMaxWh: 0,
        devices: [
          {
            key: "battery:db:11",
            title: "Home battery",
            kind: "battery",
            chargeWh: 10,
            dischargeWh: 0,
            soc: 20,
          },
        ],
      },
    }),
    measured("2026-10-05T10:15:00Z", { flagged: true, gridChargeWh: 19 }),
    measured("2026-10-05T10:30:00Z", {
      homeWh: null,
      gridImportWh: null,
      gridChargeWh: null,
      gridCost: null,
    }),
    measured("2026-10-05T10:45:00Z"),
  ],
  devices: [
    {
      key: "battery:db:11",
      name: "db:11",
      title: "BYD",
      kind: "battery",
      capacityKWh: 10,
      plan: [
        { start: "2026-10-05T10:00:00Z", chargeWh: 1500, dischargeWh: 0, soc: 20 },
        { start: "2026-10-05T10:15:00Z", chargeWh: 0, dischargeWh: 0, soc: 35, flagged: true },
        { start: "2026-10-05T10:30:00Z", chargeWh: null, dischargeWh: null, soc: null },
        { start: "2026-10-05T10:45:00Z", chargeWh: 0, dischargeWh: 100, soc: 41 },
      ],
    },
    {
      key: "loadpoint:2",
      name: "db:13",
      title: "Heat pump",
      kind: "loadpoint",
      capacityKWh: 0,
      plan: [
        { start: "2026-10-05T10:00:00Z", chargeWh: 500, dischargeWh: 0, soc: null },
        { start: "2026-10-05T10:15:00Z", chargeWh: 0, dischargeWh: 0, soc: null },
        { start: "2026-10-05T10:30:00Z", chargeWh: 0, dischargeWh: 0, soc: null },
        { start: "2026-10-05T10:45:00Z", chargeWh: 0, dischargeWh: 0, soc: null },
      ],
    },
  ],
};

const planStart = Date.parse(PLAN_START);
const ms = (time: string) => Date.parse(time);

const input = (past = pastView(history, planStart, 1)): ChartInput => ({
  slots,
  devices: [battery],
  visibility: defaultVisibility([battery]),
  past,
  colors: {
    text: "#28293e",
    muted: "#93949e",
    border: "#e0e0e0",
    price: "#ff912f",
    solar: "#0fde41",
    devices: { [battery.key]: "#2dcc6f" },
  },
  width: 600,
  formatHour: (date) => `${date.getUTCHours()}`,
  formatWeekday: () => "Mon",
  formatPrice: (value) => `${value}`,
  formatPower: (value) => `${value}`,
  labels: { power: "kW", price: "ct", soc: "%" },
});

const series = (option: ReturnType<typeof buildPlanChartOption>["option"]) =>
  option.series as { id: string; data: [number, number | null][] }[];

describe("measured history window", () => {
  test("starts a whole window before the slot containing now and never overlaps the plan", () => {
    const view = pastView(history, planStart, 1)!;
    expect(view.start).toBe(ms("2026-10-05T10:00:00Z"));
    expect(view.end).toBe(planStart);
    expect(view.slots).toHaveLength(4);

    // a slot that would end after "now" (a stale plan, or the in-progress slot) is not drawn
    const stale = pastView(history, ms("2026-10-05T10:50:00Z"), 1)!;
    expect(stale.slots.map((slot) => slot.start)).toEqual([
      "2026-10-05T10:00:00Z",
      "2026-10-05T10:15:00Z",
      "2026-10-05T10:30:00Z",
    ]);
    // a narrower window drops the older slots
    const short = pastView(history, ms("2026-10-05T10:50:00Z"), 0.5)!;
    expect(short.slots.map((slot) => slot.start)).toEqual([
      "2026-10-05T10:15:00Z",
      "2026-10-05T10:30:00Z",
    ]);
  });

  test("Off and a missing response both draw the plan alone, identical to before", () => {
    expect(pastView(history, planStart, 0)).toBeUndefined();
    expect(pastView(undefined, planStart, 24)).toBeUndefined();
    const withoutPast = buildPlanChartOption({ ...input(), past: undefined });
    const off = buildPlanChartOption({ ...input(), past: pastView(history, planStart, 0) });
    expect(JSON.stringify(off)).toBe(JSON.stringify(withoutPast));
    expect(series(off.option).some((s) => s.id.startsWith("past-"))).toBe(false);
    expect((off.option.xAxis as { min: number }[])[0]!.min).toBe(planStart);
  });

  test("grid charging shades only measured grid energy at or above the transfer threshold", () => {
    const blocks = pastGridBlocks(history.forecast);
    expect(blocks).toEqual([
      {
        start: ms("2026-10-05T10:00:00Z"),
        end: ms("2026-10-05T10:15:00Z"),
        definite: true,
        minWh: 1500,
        maxWh: 1500,
      },
    ]);
  });

  test("charge level breaks at a gap and joins the plan's initial level at now", () => {
    const view = pastView(history, planStart, 1)!;
    expect(pastSocPoints(view, battery.key, 42)).toEqual([
      [ms("2026-10-05T10:00:00Z"), 20],
      [ms("2026-10-05T10:15:00Z"), 35],
      [ms("2026-10-05T10:30:00Z"), null],
      [ms("2026-10-05T10:45:00Z"), 41],
      [planStart, 42],
    ]);
    // no join across a missing last slot or without a live level
    const stale = pastView(history, ms("2026-10-05T10:50:00Z"), 1)!;
    expect(pastSocPoints(stale, battery.key, 42).at(-1)).toEqual([
      ms("2026-10-05T10:30:00Z"),
      null,
    ]);
    expect(pastSocPoints(view, battery.key, undefined).at(-1)).toEqual([
      ms("2026-10-05T10:45:00Z"),
      41,
    ]);
    expect(pastSocPoints(view, "loadpoint:2", 50)).toEqual([]);
  });

  test("past series are solid, share the plan's colours and stop at now", () => {
    const { option } = buildPlanChartOption(input());
    expect((option.xAxis as { min: number }[])[0]!.min).toBe(ms("2026-10-05T10:00:00Z"));
    const all = series(option);
    const ids = all.map((s) => s.id);
    expect(ids).toEqual(
      expect.arrayContaining([
        "past-region-power",
        "past-region-soc",
        "past-grid-power",
        "past-solar",
        "past-home",
        "past-soc-battery:db:11",
      ])
    );
    const home = all.find((s) => s.id === "past-home") as unknown as {
      data: [number, number | null][];
      lineStyle: { type: string; color: string };
    };
    const planHome = all.find((s) => s.id === "home") as unknown as {
      lineStyle: { color: string };
    };
    expect(home.lineStyle.type).toBe("solid");
    expect(home.lineStyle.color).toBe(planHome.lineStyle.color);
    // the gap at 10:30 is a break, not zero
    expect(home.data).toContainEqual([ms("2026-10-05T10:30:00Z"), null]);
    expect(home.data.every(([, value]) => value !== 0)).toBe(true);
    for (const s of all.filter(
      (s) => s.id.startsWith("past-") && s.id !== "past-soc-battery:db:11"
    ))
      expect(s.data.every(([time]) => time <= planStart)).toBe(true);
    // the price line runs through now without a break
    const price = all.find((s) => s.id === "price")!;
    expect(price.data[0]).toEqual([ms("2026-10-05T10:00:00Z"), 0.25]);
    expect(price.data).toContainEqual([planStart, 0.3]);
    expect(price.data.filter(([, value]) => value === null)).toEqual([]);
  });

  test("the real grid-charging afternoon builds finite series and shades 13:30 to 16:15 local", () => {
    const start = Date.parse("2026-10-05T17:00:27+02:00");
    const view = pastView(planHistoryAfternoon, start, 4)!;
    expect(view.slots).toHaveLength(16);
    const blocks = pastGridBlocks(view.slots);
    // 13:30 - 16:15 local; the 15:30 and 15:45 slots ran on surplus solar
    expect(new Date(blocks[0]!.start).toISOString()).toBe("2026-10-05T11:30:00.000Z");
    expect(new Date(blocks.at(-1)!.end).toISOString()).toBe("2026-10-05T14:15:00.000Z");
    expect(blocks.every((block) => block.definite)).toBe(true);
    const merged = blocks.reduce((sum, block) => sum + block.minWh, 0);
    expect(merged).toBeGreaterThan(9000);
    const { option } = buildPlanChartOption({
      ...input(view),
      slots: [planSlot("2026-10-05T15:00:27Z", "2026-10-05T15:15:00Z")],
    });
    for (const s of series(option))
      for (const point of s.data ?? [])
        if (Array.isArray(point))
          expect(point.every((v) => v === null || Number.isFinite(v))).toBe(true);
  });
});

describe("measured slot popup", () => {
  const view = pastView(history, planStart, 1)!;
  const visibility = defaultVisibility([battery]);

  test("a grid-charging slot reports what happened and the plan that was in force", () => {
    const details = pastSlotDetails(0, view, [battery], visibility)!;
    expect(details.measured).toBe(true);
    expect(details.flagged).toBe(false);
    expect(details.gridImport).toBeCloseTo(6.8);
    expect(details.gridChargeWh).toBe(1500);
    expect(details.gridCost).toBe(0.425);
    expect(details.price).toBe(0.25);
    const [home, heatPump] = details.devices;
    expect(home).toMatchObject({
      key: battery.key,
      title: "Home battery",
      soc: 20,
      socEnd: 35,
      state: "charging",
      powerKW: 6,
      gridWh: 1500,
      planned: { state: "idle", soc: 20 },
    });
    expect(heatPump).toMatchObject({ key: "loadpoint:2", title: "Heat pump", state: "charging" });
    expect(heatPump!.soc).toBeUndefined();
    expect(details.planned).toEqual({
      snapshot: "2026-10-05T09:45:32Z",
      grid: { kind: "none" },
    });
  });

  test("without a covering snapshot the planned section is omitted, flagged rows say so", () => {
    const details = pastSlotDetails(1, view, [battery], visibility)!;
    expect(details.planned).toBeUndefined();
    expect(details.devices[0]!.planned).toBeUndefined();
    expect(details.flagged).toBe(true);
    expect(details.devices[0]!.flagged).toBe(true);
    // the idle heat pump is not listed
    expect(details.devices.map((d) => d.key)).toEqual([battery.key]);
  });

  test("a gap shows no values instead of zeros and no end level", () => {
    const details = pastSlotDetails(2, view, [battery], visibility)!;
    expect(details.home).toBeUndefined();
    expect(details.gridImport).toBeUndefined();
    expect(details.gridChargeWh).toBeUndefined();
    expect(details.gridCost).toBeUndefined();
    expect(details.devices[0]).toMatchObject({ state: "noData" });
    expect(details.devices[0]!.soc).toBeUndefined();
    // the slot before the gap has no end level either
    expect(pastSlotDetails(1, view, [battery], visibility)!.devices[0]!.socEnd).toBeUndefined();
  });

  test("hidden layers and hidden devices stay out of the popup", () => {
    const hidden = defaultVisibility([battery]);
    hidden.layers.price = false;
    hidden.layers.grid = false;
    hidden.soc[battery.key] = false;
    const details = pastSlotDetails(0, view, [battery], hidden)!;
    expect(details.price).toBeUndefined();
    expect(details.gridChargeWh).toBeUndefined();
    expect(details.planned?.grid).toBeUndefined();
    expect(details.devices.map((d) => d.key)).toEqual(["loadpoint:2"]);
  });
});
