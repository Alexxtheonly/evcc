import { describe, expect, test } from "vite-plus/test";
import { defaultVisibility } from "./planChartModel";
import { alpha, buildPlanChartOption, type ChartInput } from "./planChartOption";
import type { EnergyDevice, EnergyForecastSlot } from "./energyIntelligence";
import { livePlanEvening } from "./__fixtures__/energyPlanLive";

const slot = (start: string, end: string) =>
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
  }) as EnergyForecastSlot;
const slots = [
  slot("2026-10-05T10:10:00Z", "2026-10-05T10:15:00Z"),
  slot("2026-10-05T10:15:00Z", "2026-10-05T10:30:00Z"),
];
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
  plan: [
    { start: slots[0]!.start, chargeWh: 0, dischargeWh: 0, soc: 40 },
    { start: slots[1]!.start, chargeWh: 1000, dischargeWh: 0, soc: 42 },
  ],
};
const devices = [battery, energyOnly, car];

test("translucent theme colours parse shorthand, 8-digit and non-hex values", () => {
  expect(alpha("#fff", 0.1)).toBe("rgba(255, 255, 255, 0.1)");
  expect(alpha(" #0fde41ff", 0.28)).toBe("rgba(15, 222, 65, 0.28)");
  expect(alpha("rgb(1, 2, 3)", 0.5)).toBe("color-mix(in srgb, rgb(1, 2, 3) 50%, transparent)");
});

describe("chart option", () => {
  const input = (overrides: Partial<ChartInput> = {}): ChartInput => ({
    slots,
    devices,
    visibility: defaultVisibility(devices),
    low: [10, 15],
    high: [30, 25],
    width: 800,
    colors: {
      text: "#28293e",
      muted: "#93949e",
      border: "#dddddd",
      price: "#ff912f",
      solar: "#0fde41",
      devices: { "battery:a": "#0ba631", "loadpoint:car": "#60a5fa" },
    },
    formatHour: (date) => `${date.getHours()}`,
    formatWeekday: () => "Mon",
    formatPrice: (value) => `${value}`,
    formatPower: (value) => `${value}`,
    labels: { power: "kW", price: "ct/kWh", soc: "%" },
    ...overrides,
  });
  const ids = (option: ReturnType<typeof buildPlanChartOption>) =>
    (option.option.series as { id: string }[]).map((series) => series.id);

  test("legend layers decide which series exist; the charge-level grid disappears when empty", () => {
    expect(ids(buildPlanChartOption(input()))).toEqual([
      "solar-range",
      "solar",
      "home-range",
      "home",
      "price",
      "soc-range",
      "soc-battery:a",
      "soc-loadpoint:car",
    ]);
    const visibility = defaultVisibility(devices);
    visibility.layers = { price: false, home: false, solar: true, grid: true };
    visibility.soc = { "battery:a": false, "loadpoint:car": false };
    visibility.power["battery:a"] = true;
    const reduced = buildPlanChartOption(input({ visibility }));
    expect(ids(reduced)).toEqual(["solar-range", "solar", "power-battery:a"]);
    expect(reduced.layout.socHeight).toBe(0);
    const axes = buildPlanChartOption(input({ formatPower: (v) => `p${v}` })).option
      .yAxis as unknown as { axisLabel: { formatter: (v: number) => string } }[];
    expect(axes[0]!.axisLabel.formatter(1.5)).toBe("p1.5");
    expect((reduced.option.yAxis as { show?: boolean }[])[1]!.show).toBe(false);
  });

  test("a battery power layer is a dashed line, never an area that blends into solar", () => {
    const visibility = defaultVisibility(devices);
    visibility.power = { "battery:a": true, "loadpoint:car": true };
    const series = buildPlanChartOption(input({ visibility })).option.series as unknown as {
      id: string;
      areaStyle?: unknown;
      lineStyle: { type: string };
    }[];
    const battery = series.find((s) => s.id === "power-battery:a")!;
    expect(battery.areaStyle).toBeUndefined();
    expect(battery.lineStyle.type).toBe("dashed");
    expect(series.find((s) => s.id === "power-loadpoint:car")!.areaStyle).toBeDefined();
  });

  test("grid charging shading spans both grids and follows its toggle", () => {
    const grid = slots.map((s) => ({ ...s, gridChargeMinWh: 400, gridChargeMaxWh: 400 }));
    expect(ids(buildPlanChartOption(input({ slots: grid }))).slice(0, 2)).toEqual([
      "grid-power",
      "grid-soc",
    ]);
    const visibility = defaultVisibility(devices);
    visibility.layers.grid = false;
    expect(ids(buildPlanChartOption(input({ slots: grid, visibility })))).not.toContain(
      "grid-power"
    );
  });

  test("the real snapshot builds without non-finite coordinates", () => {
    const live = livePlanEvening.optimizerInsights;
    const option = buildPlanChartOption(
      input({
        slots: live.forecast!,
        devices: live.devices!,
        visibility: defaultVisibility(live.devices!),
        low: live.scenarios?.batterySocLow,
        high: live.scenarios?.batterySocHigh,
      })
    );
    const data = (option.option.series as { data: unknown[] }[]).flatMap((series) => series.data);
    expect(JSON.stringify(data)).not.toMatch(/NaN|Infinity/);
    expect(ids(option)).toContain("soc-range");
  });
});
