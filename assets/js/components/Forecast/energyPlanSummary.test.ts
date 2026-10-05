import { describe, expect, test } from "vite-plus/test";
import { daySummaries, planHeadline, significantWindows } from "./energyPlanSummary";
import {
  TRANSFER_MIN_WH,
  planWindows,
  type EnergyDevice,
  type EnergyForecastSlot,
  type EnergyInsights,
} from "./energyIntelligence";
import { livePlanAfternoon, livePlanEvening } from "./__fixtures__/energyPlanLive";

const QUARTER = 15 * 60 * 1000;

// Consecutive 15-minute slots from `start`; grid allocation fields present unless overridden.
function makeSlots(start: string, count: number, overrides: Partial<EnergyForecastSlot> = {}) {
  return Array.from({ length: count }, (_, index) => {
    const from = Date.parse(start) + index * QUARTER;
    return {
      start: new Date(from).toISOString(),
      end: new Date(from + QUARTER).toISOString(),
      homeLowWh: 50,
      homeWh: 100,
      homeHighWh: 200,
      solarLowWh: 0,
      solarWh: 0,
      solarHighWh: 0,
      gridPrice: 0.3,
      gridImportWh: 100,
      gridChargeMinWh: 0,
      gridChargeMaxWh: 0,
      ...overrides,
    } as EnergyForecastSlot;
  });
}

function device(
  key: string,
  kind: EnergyDevice["kind"],
  slots: EnergyForecastSlot[],
  flows: Record<number, [number, number]>,
  extra: Partial<EnergyDevice> = {}
): EnergyDevice {
  let soc = extra.initialSoc ?? 50;
  return {
    key,
    name: key,
    title: `Title of ${key}`,
    kind,
    capacityKWh: 10,
    initialSoc: 50,
    plan: slots.map((slot, index) => {
      const [chargeWh, dischargeWh] = flows[index] ?? [0, 0];
      soc += (chargeWh - dischargeWh) / 100;
      return { start: slot.start, chargeWh, dischargeWh, soc };
    }),
    ...extra,
  };
}

const range = (from: number, to: number, value: [number, number]) =>
  Object.fromEntries(Array.from({ length: to - from + 1 }, (_, i) => [from + i, value]));

const insights = (
  forecast: EnergyForecastSlot[],
  devices: EnergyDevice[],
  status: EnergyInsights["status"] = "ready"
): EnergyInsights => ({
  updated: forecast[0]?.start ?? "",
  automatic: true,
  status,
  settings: {
    robust: true,
    arrivals: false,
    useLearnedEfficiency: false,
    batteryWear: {},
    settlementMode: "simulation",
  },
  forecast,
  devices,
});

describe("plan headline", () => {
  test("a plan without material transfers says so instead of inventing an action", () => {
    const slots = makeSlots("2026-10-05T10:00:00Z", 8);
    const battery = device("battery", "battery", slots, { 2: [15, 0], 5: [0, 19] });
    const car = device("car", "vehicle", slots, {});
    expect(planHeadline(insights(slots, [battery, car]))).toEqual([
      { kind: "idle", end: slots[7]!.end },
    ]);
  });

  test("empty, missing and unavailable plans are unavailable, never idle", () => {
    const slots = makeSlots("2026-10-05T10:00:00Z", 4);
    expect(planHeadline(undefined)).toEqual([{ kind: "unavailable" }]);
    expect(planHeadline(insights([], []))).toEqual([{ kind: "unavailable" }]);
    expect(planHeadline(insights(slots, [], "unavailable"))).toEqual([{ kind: "unavailable" }]);
  });

  test("solar charging merges across a car's midday gap and names the following discharge", () => {
    const slots = makeSlots("2026-10-05T08:00:00Z", 40, { solarWh: 800 });
    // charge 0-5, a car takes the surplus 6-11, charge again 12-17, discharge 24-35
    const battery = device("battery", "battery", slots, {
      ...range(0, 5, [200, 0]),
      ...range(12, 17, [200, 0]),
      ...range(24, 35, [0, 150]),
    });
    const [item] = planHeadline(insights(slots, [battery]));
    expect(item).toEqual({
      kind: "batteryCharge",
      device: "Title of battery",
      start: slots[0]!.start,
      end: slots[17]!.end,
      energyWh: 2400,
      source: "solar",
      next: { start: slots[24]!.start, end: slots[35]!.end, energyWh: 1800 },
    });
  });

  test("charging is only attributed to solar when the grid can supply (almost) none of it", () => {
    const base = makeSlots("2026-10-05T08:00:00Z", 8);
    const flows = range(0, 3, [500, 0]);
    const possibleGrid = base.map((slot, index) =>
      index < 4 ? { ...slot, gridChargeMaxWh: 400 } : slot
    );
    const unknownGrid = base.map((slot) => ({ ...slot, gridChargeMaxWh: undefined }));
    for (const slots of [possibleGrid, unknownGrid]) {
      const [item] = planHeadline(insights(slots, [device("b", "battery", slots, flows)]));
      expect(item).toMatchObject({ kind: "batteryCharge", source: "unknown" });
    }
  });

  test("grid charging reports the definite site-level energy and its energy-weighted price", () => {
    const slots = makeSlots("2026-10-06T00:00:00Z", 12).map((slot, index) =>
      index >= 8 && index <= 10
        ? {
            ...slot,
            gridPrice: index === 8 ? 0.2 : 0.3,
            gridChargeMinWh: index === 8 ? 1000 : 500,
            gridChargeMaxWh: 1000,
          }
        : slot
    );
    const battery = device("b", "battery", slots, range(8, 10, [1000, 0]));
    const items = planHeadline(insights(slots, [battery]));
    expect(items).toContainEqual({
      kind: "gridCharge",
      start: slots[8]!.start,
      end: slots[10]!.end,
      minWh: 2000,
      maxWh: 3000,
      price: (1000 * 0.2 + 500 * 0.3 + 500 * 0.3) / 2000,
    });
    expect(items.find((item) => item.kind === "batteryCharge")).toMatchObject({
      source: "unknown",
    });
  });

  test("vehicle charging sums its windows, reports the final level and flags expected cars", () => {
    const slots = makeSlots("2026-10-05T10:00:00Z", 24);
    const car = device("car", "vehicle", slots, {
      ...range(2, 5, [500, 0]),
      ...range(14, 15, [500, 0]),
    });
    const heater = device("heater", "expectedVehicle", slots, range(20, 23, [500, 0]), {
      capacityKWh: 0,
      initialSoc: undefined,
    });
    expect(planHeadline(insights(slots, [car, heater]))).toEqual([
      {
        kind: "vehicleCharge",
        device: "Title of car",
        start: slots[2]!.start,
        end: slots[15]!.end,
        energyWh: 3000,
        expected: false,
        endSoc: 80,
      },
      {
        kind: "vehicleCharge",
        device: "Title of heater",
        start: slots[20]!.start,
        end: slots[23]!.end,
        energyWh: 2000,
        expected: true,
        endSoc: undefined,
      },
    ]);
  });

  test("a battery that discharges first reports the price it avoids and stops there", () => {
    const slots = makeSlots("2026-10-05T17:00:00Z", 8).map((slot, index) => ({
      ...slot,
      gridPrice: index < 2 ? 0.5 : 0.3,
    }));
    const battery = device("b", "battery", slots, {
      0: [0, 300],
      1: [0, 300],
      2: [0, 600],
      6: [400, 0],
      7: [400, 0],
    });
    expect(planHeadline(insights(slots, [battery]))).toEqual([
      {
        kind: "batteryDischarge",
        device: "Title of b",
        start: slots[0]!.start,
        end: slots[2]!.end,
        energyWh: 1200,
        price: (300 * 0.5 + 300 * 0.5 + 600 * 0.3) / 1200,
      },
    ]);
  });

  test("statements are ordered by start time and capped at three", () => {
    const slots = makeSlots("2026-10-05T10:00:00Z", 20);
    const devices = [3, 0, 9, 6].map((offset) =>
      device(`car${offset}`, "vehicle", slots, range(offset, offset + 1, [400, 0]))
    );
    const items = planHeadline(insights(slots, devices));
    expect(items.map((item) => ("device" in item ? item.device : ""))).toEqual([
      "Title of car0",
      "Title of car3",
      "Title of car6",
    ]);
  });

  test("windows tolerate a 30 minute pause but split on a longer one and drop noise", () => {
    const slots = makeSlots("2026-10-05T10:00:00Z", 16);
    const battery = device("b", "battery", slots, {
      0: [200, 0],
      3: [200, 0],
      7: [200, 0],
      12: [10, 0],
    });
    expect(
      significantWindows(battery, slots).map((w) => [w.firstIndex, w.lastIndex, w.energyWh])
    ).toEqual([[0, 3, 400]]);
  });

  test("real snapshot with two cars and a battery reads as the owner would describe it", () => {
    const items = planHeadline(livePlanEvening.optimizerInsights);
    expect(items.map((item) => item.kind)).toEqual([
      "batteryCharge",
      "vehicleCharge",
      "vehicleCharge",
    ]);
    expect(items[0]).toMatchObject({
      device: "Home battery 19 kWh",
      source: "solar",
      start: "2026-10-05T10:15:00+02:00",
      end: "2026-10-05T18:00:00+02:00",
      next: { start: "2026-10-05T18:15:00+02:00", end: "2026-10-06T00:00:00+02:00" },
    });
    expect(items[1]).toMatchObject({ device: "Carport (Family car)", endSoc: 50.61 });
    expect(planHeadline(livePlanAfternoon.optimizerInsights)[0]).toMatchObject({
      kind: "batteryCharge",
      device: "Home battery 19 kWh",
      start: "2026-10-04T13:08:54+02:00",
    });
  });
});

test("a definite grid charge is never crowded out by battery and car statements", () => {
  // reviewer's probe: battery discharge, two cars, then 4 kWh definite grid charging at 03:00
  const slots = makeSlots("2026-10-05T18:00:00Z", 40).map((slot, index) =>
    index >= 36 ? { ...slot, gridChargeMinWh: 1000, gridChargeMaxWh: 1000, gridPrice: 0.24 } : slot
  );
  const devices = [
    device("battery", "battery", slots, range(0, 7, [0, 300])),
    device("carA", "vehicle", slots, range(8, 11, [1000, 0])),
    device("carB", "vehicle", slots, range(12, 15, [1000, 0])),
  ];
  const items = planHeadline(insights(slots, devices));
  expect(items).toHaveLength(3);
  expect(items.map((item) => item.kind)).toEqual([
    "batteryDischarge",
    "vehicleCharge",
    "gridCharge",
  ]);
  expect(items[2]).toMatchObject({ start: slots[36]!.start, minWh: 4000, price: 0.24 });
});

test("headline, schedule windows and day tiles ignore the same sub-threshold slots", () => {
  const slots = makeSlots("2026-10-05T10:00:00Z", 8);
  const battery = device("b", "battery", slots, {
    1: [TRANSFER_MIN_WH - 1, 0],
    2: [400, 0],
    3: [400, 0],
  });
  const [headline] = planHeadline(insights(slots, [battery]));
  const [window] = planWindows(battery, slots);
  expect(headline).toMatchObject({ start: slots[2]!.start, energyWh: 800 });
  expect(window).toMatchObject({ start: slots[2]!.start, energyWh: 800 });
  expect(daySummaries(slots, [battery], "Europe/Berlin")[0]!.batteries[0]!.chargeKWh).toBe(0.8);
});

describe("day summaries", () => {
  test("the 25-hour DST day is one full day, not partial and not 24 hours", () => {
    // Europe/Berlin leaves summer time on 2026-10-25: 100 quarter hours
    const slots = makeSlots("2026-10-24T22:00:00Z", 100);
    const [day, ...rest] = daySummaries(slots, [], "Europe/Berlin");
    expect(rest).toHaveLength(0);
    expect(day).toMatchObject({ key: "2026-10-25", partial: false, hours: 25 });
    expect(day!.home).toEqual({ value: 10, low: 5, high: 20 });
  });

  test("a plan starting mid-afternoon has a partial first day, a full day and a partial last day", () => {
    const slots = makeSlots("2026-10-04T13:00:00Z", 4 * 40);
    const days = daySummaries(slots, [], "Europe/Berlin");
    expect(days.map((day) => [day.key, day.partial, day.hours])).toEqual([
      ["2026-10-04", true, 9],
      ["2026-10-05", false, 24],
      ["2026-10-06", true, 7],
    ]);
  });

  test("missing ranges and grid import are unavailable while genuine zeros stay zero", () => {
    const slots = makeSlots("2026-10-04T22:00:00Z", 8);
    slots[3] = { ...slots[3]!, homeHighWh: NaN, gridImportWh: undefined };
    const [day] = daySummaries(slots, [], "Europe/Berlin");
    expect(day!.home).toEqual({ value: 0.8, low: 0.4, high: undefined });
    expect(day!.solar).toEqual({ value: 0, low: 0, high: 0 });
    slots[5] = { ...slots[5]!, solarWh: NaN };
    expect(daySummaries(slots, [], "Europe/Berlin")[0]!.solar).toEqual({
      value: undefined,
      low: 0,
      high: 0,
    });
    expect(day!.gridImportKWh).toBeUndefined();
    const complete = daySummaries(makeSlots("2026-10-04T22:00:00Z", 8), [], "Europe/Berlin");
    expect(complete[0]!.gridImportKWh).toBeCloseTo(0.8);
  });

  test("battery throughput and charge-level extremes per day, including the starting level", () => {
    const slots = makeSlots("2026-10-04T21:00:00Z", 8);
    const battery = device("b", "battery", slots, { 0: [500, 0], 6: [0, 1500] });
    const unknown = device("u", "battery", slots, {}, { initialSoc: undefined });
    unknown.plan = unknown.plan.map(({ soc: _soc, ...point }) => point);
    const car = device("car", "vehicle", slots, { 1: [2000, 0], 7: [1000, 0] });
    const days = daySummaries(slots, [battery, unknown, car], "Europe/Berlin");
    expect(days).toHaveLength(2);
    expect(days[0]!.batteries).toEqual([
      { key: "b", title: "Title of b", chargeKWh: 0.5, dischargeKWh: 0, minSoc: 50, maxSoc: 55 },
      {
        key: "u",
        title: "Title of u",
        chargeKWh: 0,
        dischargeKWh: 0,
        minSoc: undefined,
        maxSoc: undefined,
      },
    ]);
    expect(days[1]!.batteries[0]).toMatchObject({ dischargeKWh: 1.5, minSoc: 40, maxSoc: 55 });
    expect(days.map((day) => day.vehicleChargeKWh)).toEqual([2, 1]);
  });
});
