import { describe, it, expect } from "vite-plus/test";
import {
  batteryChargeWindows,
  batteryDischargeWindows,
  vehicleChargeWindows,
  nextRepeatingOccurrence,
  adaptivePlanMarkers,
} from "./optimizerOverlay";
import type { EvOpt, RepeatingPlan } from "@/types/evcc";

const t = (iso: string) => new Date(iso).getTime();

// four 15min slots starting 2026-01-01T00:00:00Z
const timestamp = [
  "2026-01-01T00:00:00Z",
  "2026-01-01T00:15:00Z",
  "2026-01-01T00:30:00Z",
  "2026-01-01T00:45:00Z",
];
const dt = [900, 900, 900, 900];

function evopt(overrides: Partial<EvOpt>): EvOpt {
  return {
    req: { time_series: { dt } },
    res: { batteries: [], grid_import: [], grid_export: [] },
    details: { timestamp, batteryDetails: [] },
    ...overrides,
  } as unknown as EvOpt;
}

describe("batteryChargeWindows", () => {
  it("collapses adjacent grid-import charge slots into one window", () => {
    const e = evopt({
      res: {
        batteries: [{ charging_power: [500, 500, 0, 0], discharging_power: [0, 0, 0, 0] }],
        grid_import: [200, 200, 0, 0],
        grid_export: [0, 0, 0, 0],
      },
      details: {
        timestamp,
        batteryDetails: [{ type: "battery", name: "bat1", title: "Anker", capacity: 10 }],
      },
    } as unknown as Partial<EvOpt>);

    expect(batteryChargeWindows(e)).toEqual([{ start: t(timestamp[0]!), end: t(timestamp[2]!) }]);
  });

  it("ignores charging that is not sourced from the grid", () => {
    const e = evopt({
      res: {
        batteries: [{ charging_power: [500, 0, 0, 0], discharging_power: [0, 0, 0, 0] }],
        grid_import: [0, 0, 0, 0], // solar-only charge
        grid_export: [100, 0, 0, 0],
      },
      details: {
        timestamp,
        batteryDetails: [{ type: "battery", name: "bat1", title: "Anker", capacity: 10 }],
      },
    } as unknown as Partial<EvOpt>);

    expect(batteryChargeWindows(e)).toEqual([]);
  });

  it("ignores vehicle entries", () => {
    const e = evopt({
      res: {
        batteries: [{ charging_power: [500, 0, 0, 0], discharging_power: [0, 0, 0, 0] }],
        grid_import: [200, 0, 0, 0],
        grid_export: [0, 0, 0, 0],
      },
      details: {
        timestamp,
        batteryDetails: [{ type: "vehicle", name: "car", title: "EV", capacity: 60 }],
      },
    } as unknown as Partial<EvOpt>);

    expect(batteryChargeWindows(e)).toEqual([]);
  });

  it("does not credit grid import to the battery when household load already consumes it", () => {
    const e = evopt({
      req: { time_series: { dt, gt: [200, 200, 0, 0] } },
      res: {
        batteries: [{ charging_power: [500, 0, 0, 0], discharging_power: [0, 0, 0, 0] }],
        grid_import: [200, 200, 0, 0], // fully consumed by household load
        grid_export: [0, 0, 0, 0],
      },
      details: {
        timestamp,
        batteryDetails: [{ type: "battery", name: "bat1", title: "Anker", capacity: 10 }],
      },
    } as unknown as Partial<EvOpt>);

    expect(batteryChargeWindows(e)).toEqual([]);
  });

  it("credits only the grid import left over after household load to battery charging", () => {
    const e = evopt({
      req: { time_series: { dt, gt: [200, 0, 0, 0] } },
      res: {
        batteries: [{ charging_power: [500, 0, 0, 0], discharging_power: [0, 0, 0, 0] }],
        grid_import: [700, 0, 0, 0], // 200 household + 500 battery
        grid_export: [0, 0, 0, 0],
      },
      details: {
        timestamp,
        batteryDetails: [{ type: "battery", name: "bat1", title: "Anker", capacity: 10 }],
      },
    } as unknown as Partial<EvOpt>);

    expect(batteryChargeWindows(e)).toEqual([{ start: t(timestamp[0]!), end: t(timestamp[1]!) }]);
  });

  it("returns empty for missing optimizer data", () => {
    expect(batteryChargeWindows(undefined)).toEqual([]);
  });
});

describe("batteryDischargeWindows", () => {
  it("collapses discharge slots regardless of export", () => {
    const e = evopt({
      res: {
        batteries: [{ charging_power: [0, 0, 0, 0], discharging_power: [0, 300, 300, 0] }],
        grid_import: [0, 0, 0, 0],
        grid_export: [0, 0, 0, 0],
      },
      details: {
        timestamp,
        batteryDetails: [{ type: "battery", name: "bat1", title: "Anker", capacity: 10 }],
      },
    } as unknown as Partial<EvOpt>);

    expect(batteryDischargeWindows(e)).toEqual([
      { start: t(timestamp[1]!), end: t(timestamp[3]!) },
    ]);
  });
});

describe("vehicleChargeWindows", () => {
  it("keys windows by the vehicle's config name, same key space as adaptivePlanMarkers", () => {
    const e = evopt({
      res: {
        batteries: [{ charging_power: [500, 500, 0, 500], discharging_power: [0, 0, 0, 0] }],
        grid_import: [0, 0, 0, 0],
        grid_export: [0, 0, 0, 0],
      },
      details: {
        timestamp,
        batteryDetails: [
          { type: "vehicle", name: "car", title: "Carport (blue e-Golf)", capacity: 60 },
        ],
      },
    } as unknown as Partial<EvOpt>);

    const res = vehicleChargeWindows(e);
    expect(res).toHaveLength(1);
    expect(res[0]!.key).toBe("car");
    expect(res[0]!.title).toBe("Carport (blue e-Golf)");
    expect(res[0]!.windows).toEqual([
      { start: t(timestamp[0]!), end: t(timestamp[2]!) },
      { start: t(timestamp[3]!), end: t(new Date(t(timestamp[3]!) + 900_000).toISOString()) },
    ]);
  });

  it("falls back to the loadpoint title when the optimizer couldn't attribute a config'd vehicle", () => {
    const e = evopt({
      res: {
        batteries: [{ charging_power: [500, 0, 0, 0], discharging_power: [0, 0, 0, 0] }],
        grid_import: [0, 0, 0, 0],
        grid_export: [0, 0, 0, 0],
      },
      details: {
        timestamp,
        batteryDetails: [{ type: "vehicle", name: "", title: "Carport (guest)", capacity: 60 }],
      },
    } as unknown as Partial<EvOpt>);

    const res = vehicleChargeWindows(e);
    expect(res).toHaveLength(1);
    expect(res[0]!.key).toBe("Carport");
  });

  it("omits vehicles with no active charging slot", () => {
    const e = evopt({
      res: { batteries: [{ charging_power: [0, 0, 0, 0], discharging_power: [0, 0, 0, 0] }] },
      details: {
        timestamp,
        batteryDetails: [{ type: "vehicle", name: "car", title: "EV", capacity: 60 }],
      },
    } as unknown as Partial<EvOpt>);

    expect(vehicleChargeWindows(e)).toEqual([]);
  });
});

describe("nextRepeatingOccurrence", () => {
  const base: RepeatingPlan = {
    weekdays: [4], // Thursday
    time: "07:00",
    tz: "Europe/Berlin",
    soc: 80,
    active: true,
  };

  it("returns the next matching weekday within the horizon", () => {
    // 2026-01-01 is a Thursday
    const from = t("2026-01-01T00:00:00Z");
    const horizon = t("2026-01-10T00:00:00Z");
    const time = nextRepeatingOccurrence(base, from, horizon);
    // 07:00 CET (UTC+1 in January) = 06:00 UTC
    expect(time).toBe(t("2026-01-01T06:00:00Z"));
  });

  it("skips to the following week when today's slot already passed", () => {
    const from = t("2026-01-01T08:00:00Z"); // past 07:00 CET
    const horizon = t("2026-01-10T00:00:00Z");
    const time = nextRepeatingOccurrence(base, from, horizon);
    expect(time).toBe(t("2026-01-08T06:00:00Z"));
  });

  it("returns null outside the horizon", () => {
    const from = t("2026-01-01T08:00:00Z");
    const horizon = t("2026-01-02T00:00:00Z"); // next Thursday is beyond this
    expect(nextRepeatingOccurrence(base, from, horizon)).toBeNull();
  });

  it("returns null for inactive plans", () => {
    const from = t("2026-01-01T00:00:00Z");
    const horizon = t("2026-01-10T00:00:00Z");
    expect(nextRepeatingOccurrence({ ...base, active: false }, from, horizon)).toBeNull();
  });

  describe("DST transitions (Europe/Berlin)", () => {
    // 2026-03-29: clocks spring forward 02:00 CET -> 03:00 CEST, so 02:00-03:00
    // local does not exist. 2026-10-25: clocks fall back 03:00 CEST -> 02:00 CET,
    // so 02:00-03:00 local occurs twice. Both dates are Sundays (weekday 0).

    it("advances a nonexistent spring-forward local time past the gap", () => {
      const plan: RepeatingPlan = {
        weekdays: [0],
        time: "02:30",
        tz: "Europe/Berlin",
        soc: 80,
        active: true,
      };
      const from = t("2026-03-29T00:00:00Z");
      const horizon = t("2026-03-30T00:00:00Z");
      // 02:30 doesn't exist; resolves to the post-transition clock, 03:30 CEST
      expect(nextRepeatingOccurrence(plan, from, horizon)).toBe(t("2026-03-29T01:30:00Z"));
    });

    it("resolves an ambiguous fall-back local time to its later occurrence", () => {
      const plan: RepeatingPlan = {
        weekdays: [0],
        time: "02:30",
        tz: "Europe/Berlin",
        soc: 80,
        active: true,
      };
      const from = t("2026-10-25T00:00:00Z");
      const horizon = t("2026-10-26T00:00:00Z");
      // 02:30 occurs twice; resolves to the later (CET, post-transition) reading
      expect(nextRepeatingOccurrence(plan, from, horizon)).toBe(t("2026-10-25T01:30:00Z"));
    });

    it("resolves an unambiguous local time close to a fall-back transition correctly", () => {
      const plan: RepeatingPlan = {
        weekdays: [0],
        time: "01:30",
        tz: "Europe/Berlin",
        soc: 80,
        active: true,
      };
      const from = t("2026-10-24T00:00:00Z");
      const horizon = t("2026-10-26T00:00:00Z");
      // 01:30 CEST (pre-transition, UTC+2) - a one-shot correction resolves this
      // one hour late, to 02:30 local, since offset at the naive guess already
      // reads post-transition
      expect(nextRepeatingOccurrence(plan, from, horizon)).toBe(t("2026-10-24T23:30:00Z"));
    });
  });
});

describe("adaptivePlanMarkers", () => {
  const plan: RepeatingPlan = {
    weekdays: [4],
    time: "07:00",
    tz: "Europe/Berlin",
    soc: 80,
    active: true,
  };
  const from = t("2026-01-01T00:00:00Z");
  const horizon = t("2026-01-10T00:00:00Z");

  it("builds a marker for vehicles with active adaptive plans", () => {
    const vehicles = [
      { name: "car", title: "EV", adaptivePlans: [plan], adaptivePlansActive: true },
    ];
    const res = adaptivePlanMarkers(vehicles, from, horizon);
    expect(res).toEqual([{ key: "car", title: "EV", time: t("2026-01-01T06:00:00Z"), soc: 80 }]);
  });

  it("skips vehicles whose adaptive plans are not active", () => {
    const vehicles = [
      { name: "car", title: "EV", adaptivePlans: [plan], adaptivePlansActive: false },
    ];
    expect(adaptivePlanMarkers(vehicles, from, horizon)).toEqual([]);
  });
});
