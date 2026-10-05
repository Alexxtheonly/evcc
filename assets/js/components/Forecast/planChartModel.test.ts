import { describe, expect, test } from "vite-plus/test";
import { batteryColor } from "@/colors";
import {
  chartLayout,
  deviceColorAssignment,
  defaultVisibility,
  gridChargeBlocks,
  gridChargeState,
  kw,
  placeTooltip,
  slotDetails,
  slotIndexAt,
  socRibbon,
  stepBands,
  stepPoints,
  timeAtX,
  xAtTime,
} from "./planChartModel";
import type { EnergyDevice, EnergyForecastSlot } from "./energyIntelligence";

const slot = (start: string, end: string, extra: Partial<EnergyForecastSlot> = {}) =>
  ({
    start,
    end,
    homeLowWh: 50,
    homeWh: 100,
    homeHighWh: 200,
    solarLowWh: 0,
    solarWh: 0,
    solarHighWh: 0,
    gridPrice: 0.3,
    ...extra,
  }) as EnergyForecastSlot;

// partial first slot (5 min), then a full quarter hour
const slots = [
  slot("2026-10-05T10:10:00Z", "2026-10-05T10:15:00Z"),
  slot("2026-10-05T10:15:00Z", "2026-10-05T10:30:00Z"),
];
const t = (iso: string) => Date.parse(iso);

const battery: EnergyDevice = {
  key: "battery:a",
  name: "a",
  title: "Storage A",
  kind: "battery",
  capacityKWh: 10,
  initialSoc: 20,
  plan: [
    { start: slots[0]!.start, chargeWh: 250, dischargeWh: 0, soc: 22.5 },
    { start: slots[1]!.start, chargeWh: 0, dischargeWh: 500, soc: 17.5 },
  ],
};

describe("tooltip placement", () => {
  const viewport = { width: 400, height: 800 };
  const size = { width: 200, height: 150 };

  test("mouse: beside the pointer, flipped left near the right edge, vertically clamped", () => {
    expect(placeTooltip({ x: 50, y: 400 }, size, viewport, "mouse")).toEqual({
      left: 66,
      top: 325,
      maxHeight: 784,
    });
    expect(placeTooltip({ x: 300, y: 400 }, size, viewport, "mouse").left).toBe(84);
    expect(placeTooltip({ x: 300, y: 790 }, size, viewport, "mouse").top).toBe(800 - 150 - 8);
    expect(placeTooltip({ x: 300, y: 5 }, size, viewport, "mouse").top).toBe(8);
  });

  test("touch: above the finger, below it only when there is no room above", () => {
    expect(placeTooltip({ x: 200, y: 500 }, size, viewport, "touch")).toEqual({
      left: 100,
      top: 500 - 16 - 150,
      maxHeight: 784,
    });
    expect(placeTooltip({ x: 200, y: 100 }, size, viewport, "touch").top).toBe(116);
    expect(placeTooltip({ x: 5, y: 500 }, size, viewport, "touch").left).toBe(8);
  });

  test("uses the rendered size: an oversized popup is capped to the viewport and scrolls", () => {
    const tall = placeTooltip({ x: 200, y: 790 }, { width: 600, height: 2000 }, viewport, "touch");
    expect(tall).toEqual({ left: 8, top: 8, maxHeight: 784 });
    const short = placeTooltip({ x: 200, y: 300 }, { width: 200, height: 60 }, viewport, "touch");
    expect(short.top).toBe(300 - 16 - 60);
  });
});

describe("time mapping", () => {
  test("slots are found by elapsed time including the partial first slot and the final edge", () => {
    expect(slotIndexAt(slots, t("2026-10-05T10:14:59Z"))).toBe(0);
    expect(slotIndexAt(slots, t("2026-10-05T10:15:00Z"))).toBe(1);
    expect(slotIndexAt(slots, t("2026-10-05T10:30:00Z"))).toBe(1);
    expect(slotIndexAt(slots, t("2026-10-05T10:31:00Z"))).toBe(-1);
    expect(slotIndexAt([], 0)).toBe(-1);
  });

  test("pixel and time conversions are inverse inside the plot and clamp outside", () => {
    const layout = chartLayout(true);
    const start = t(slots[0]!.start);
    const end = t(slots[1]!.end);
    const width = layout.left + layout.right + 400;
    expect(xAtTime(start, width, layout, start, end)).toBe(layout.left);
    expect(xAtTime(end, width, layout, start, end)).toBe(layout.left + 400);
    expect(timeAtX(layout.left + 100, width, layout, start, end)).toBe(start + (end - start) / 4);
    expect(timeAtX(0, width, layout, start, end)).toBe(start);
    expect(timeAtX(width, width, layout, start, end)).toBe(end);
  });

  test("average power uses each slot's real length", () => {
    expect(kw(250, slots[0]!)).toBeCloseTo(3);
    expect(kw(250, slots[1]!)).toBeCloseTo(1);
    expect(kw(NaN, slots[1]!)).toBeUndefined();
  });
});

describe("series data", () => {
  test("missing values break step lines and bands instead of drawing zero", () => {
    const three = [
      ...slots,
      slot("2026-10-05T10:30:00Z", "2026-10-05T10:45:00Z", { gridPrice: NaN, homeHighWh: NaN }),
      slot("2026-10-05T10:45:00Z", "2026-10-05T11:00:00Z", { gridPrice: 0.1 }),
    ];
    const points = stepPoints(three, (s) =>
      Number.isFinite(s.gridPrice) ? s.gridPrice : undefined
    );
    expect(points).toEqual([
      [t("2026-10-05T10:10:00Z"), 0.3],
      [t("2026-10-05T10:15:00Z"), 0.3],
      [t("2026-10-05T10:15:00Z"), 0.3],
      [t("2026-10-05T10:30:00Z"), 0.3],
      [t("2026-10-05T10:30:00Z"), null],
      [t("2026-10-05T10:45:00Z"), 0.1],
      [t("2026-10-05T11:00:00Z"), 0.1],
    ]);
    const bands = stepBands(three, (s) => [s.homeLowWh, s.homeHighWh]);
    expect(bands).toHaveLength(2);
    expect(bands[0]!.upper.at(-1)).toEqual([t("2026-10-05T10:30:00Z"), 200]);
    expect(bands[1]!.lower[0]).toEqual([t("2026-10-05T10:45:00Z"), 50]);
  });

  test("the scenario ribbon belongs to a single battery with complete finite bounds", () => {
    expect(socRibbon([battery], slots, [10, 15], [30, 25])).toEqual({
      lower: [
        [t(slots[0]!.start), 20],
        [t(slots[0]!.end), 10],
        [t(slots[1]!.end), 15],
      ],
      upper: [
        [t(slots[0]!.start), 20],
        [t(slots[0]!.end), 30],
        [t(slots[1]!.end), 25],
      ],
    });
    const second = { ...battery, key: "battery:b" };
    expect(socRibbon([battery, second], slots, [10, 15], [30, 25])).toBeUndefined();
    expect(socRibbon([battery], slots, [10, NaN], [30, 25])).toBeUndefined();
    expect(socRibbon([battery], slots, [10], [30])).toBeUndefined();
    expect(socRibbon([{ ...battery, capacityKWh: 0 }], slots, [10, 15], [30, 25])).toBeUndefined();
  });

  test("grid charging blocks merge contiguous slots and keep definite and possible apart", () => {
    const grid = [
      slot("2026-10-05T02:00:00Z", "2026-10-05T02:15:00Z", {
        gridChargeMinWh: 500,
        gridChargeMaxWh: 600,
      }),
      slot("2026-10-05T02:15:00Z", "2026-10-05T02:30:00Z", {
        gridChargeMinWh: 400,
        gridChargeMaxWh: 400,
      }),
      slot("2026-10-05T02:30:00Z", "2026-10-05T02:45:00Z", {
        gridChargeMinWh: 0,
        gridChargeMaxWh: 300,
      }),
      slot("2026-10-05T02:45:00Z", "2026-10-05T03:00:00Z", {
        gridChargeMinWh: 0,
        gridChargeMaxWh: 0,
      }),
      slot("2026-10-05T03:00:00Z", "2026-10-05T03:15:00Z"),
    ];
    expect(gridChargeBlocks(grid)).toEqual([
      {
        start: t("2026-10-05T02:00:00Z"),
        end: t("2026-10-05T02:30:00Z"),
        definite: true,
        minWh: 900,
        maxWh: 1000,
      },
      {
        start: t("2026-10-05T02:30:00Z"),
        end: t("2026-10-05T02:45:00Z"),
        definite: false,
        minWh: 0,
        maxWh: 300,
      },
    ]);
    expect(grid.map((s) => gridChargeState(s).kind)).toEqual([
      "definite",
      "definite",
      "possible",
      "none",
      "unavailable",
    ]);
  });
});

describe("visibility and option", () => {
  const energyOnly: EnergyDevice = {
    key: "loadpoint:heater",
    name: "heater",
    title: "Heater",
    kind: "vehicle",
    capacityKWh: 0,
    plan: [{ start: slots[1]!.start, chargeWh: 300, dischargeWh: 0, soc: 0 }],
  };
  const car: EnergyDevice = {
    key: "loadpoint:car",
    name: "car",
    title: "Car",
    kind: "expectedVehicle",
    capacityKWh: 50,
    initialSoc: 40,
    arrival: slots[1]!.start,
    plan: [
      { start: slots[0]!.start, chargeWh: 0, dischargeWh: 0, soc: 40 },
      { start: slots[1]!.start, chargeWh: 1000, dischargeWh: 0, soc: 42 },
    ],
  };
  const devices = [battery, energyOnly, car];

  test("defaults show every charge level and no per-device power", () => {
    expect(defaultVisibility(devices)).toEqual({
      layers: { price: true, home: true, solar: true, grid: true },
      soc: { "battery:a": true, "loadpoint:heater": false, "loadpoint:car": true },
      power: { "battery:a": false, "loadpoint:heater": false, "loadpoint:car": false },
    });
  });

  test("popup values follow visible layers, units and availability", () => {
    const visibility = defaultVisibility(devices);
    visibility.power["loadpoint:heater"] = true;
    visibility.layers.solar = false;
    const first = slotDetails(0, slots, devices, visibility)!;
    expect(first.price).toBe(0.3);
    expect(first.solar).toBeUndefined();
    expect(first.home!.value).toBeCloseTo(1.2);
    expect(first.grid).toEqual({ kind: "unavailable" });
    expect(first.devices).toEqual([
      { key: "battery:a", title: "Storage A", soc: 22.5, state: "charging", powerKW: 3 },
      { key: "loadpoint:heater", title: "Heater", state: "noPlan" },
      { key: "loadpoint:car", title: "Car", state: "away" },
    ]);
    const second = slotDetails(1, slots, devices, visibility)!;
    expect(second.devices[1]).toEqual({
      key: "loadpoint:heater",
      title: "Heater",
      soc: undefined,
      state: "charging",
      powerKW: 1.2,
    });
    expect(second.devices[2]).toMatchObject({ state: "charging", soc: 42 });
    visibility.layers.price = false;
    visibility.soc["battery:a"] = false;
    const hidden = slotDetails(1, slots, devices, visibility)!;
    expect(hidden.price).toBeUndefined();
    expect(hidden.devices.map((d) => d.key)).toEqual(["loadpoint:heater", "loadpoint:car"]);
    expect(slotDetails(5, slots, devices, visibility)).toBeUndefined();
  });
});

describe("device colours", () => {
  const palette = ["#60A5FA", "#FBBF24", "#22D3EE", "#F472B6", "#34D399", "#A78BFA", "#94A3B8"];
  const make = (key: string, kind: EnergyDevice["kind"], title = key): EnergyDevice => ({
    key,
    name: key,
    title,
    kind,
    capacityKWh: 10,
    plan: [],
  });

  test("batteries use evcc's battery palette, cars skip amber and green defaults", () => {
    const devices = [
      make("battery:a", "battery"),
      make("car:1", "vehicle"),
      make("car:2", "vehicle"),
      make("car:3", "expectedVehicle"),
      make("battery:b", "battery"),
    ];
    expect(deviceColorAssignment(devices, {}, palette)).toEqual({
      "battery:a": batteryColor(0),
      "car:1": "#60A5FA",
      "car:2": "#F472B6",
      "car:3": "#22D3EE",
      "battery:b": batteryColor(1),
    });
  });

  test("user colours are keyed by the loadpoint title without the vehicle, like evcc", () => {
    const devices = [make("loadpoint:0", "vehicle", "Wallbox Rechts (Luna)")];
    expect(deviceColorAssignment(devices, { "Wallbox Rechts": "#123456" }, palette)).toEqual({
      "loadpoint:0": "#123456",
    });
  });

  test("user colours for a car title win and are not handed to another car", () => {
    const devices = [make("car:1", "vehicle", "Garage"), make("car:2", "vehicle", "Carport")];
    expect(
      deviceColorAssignment(devices, { Garage: "#FBBF24", Carport: undefined }, palette)
    ).toEqual({
      "car:1": "#FBBF24",
      "car:2": "#60A5FA",
    });
    expect(deviceColorAssignment(devices, { Carport: "#60a5fa" }, palette)).toEqual({
      "car:1": "#F472B6",
      "car:2": "#60a5fa",
    });
  });
});
