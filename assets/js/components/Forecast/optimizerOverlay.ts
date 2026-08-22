// Pure transforms that turn the evopt optimizer result and vehicle adaptive plans into the
// timeline overlays the price chart renders. Kept framework-free for unit testing.

import type { EvOpt, RepeatingPlan } from "@/types/evcc";
import { loadpointTitle } from "../Optimize/chart";

export interface TimeWindow {
  start: number; // epoch ms
  end: number; // epoch ms
}

export interface KeyedWindows {
  key: string; // matches the device/loadpoint color key
  title: string;
  windows: TimeWindow[];
}

// ignore near-zero optimizer plan values (LP solver noise, not measurement noise)
const ACTIVE_THRESHOLD = 1; // Wh

// slotBounds pairs each optimizer slot with its start/end epoch ms
function slotBounds(evopt: EvOpt): TimeWindow[] {
  const ts = evopt.details?.timestamp || [];
  const dt = evopt.req?.time_series?.dt || [];
  return ts.map((t, i) => {
    const start = new Date(t).getTime();
    return { start, end: start + (dt[i] || 0) * 1000 };
  });
}

// collapse merges adjacent active slots into contiguous windows
function collapse(slots: TimeWindow[], active: boolean[]): TimeWindow[] {
  const windows: TimeWindow[] = [];
  let current: TimeWindow | null = null;
  slots.forEach((slot, i) => {
    if (!active[i]) {
      current = null;
      return;
    }
    if (current && current.end === slot.start) {
      current.end = slot.end;
    } else {
      current = { ...slot };
      windows.push(current);
    }
  });
  return windows;
}

// batteryChargeWindows returns the slots where a home battery is charged from the
// grid. grid_import is a single netted figure per slot covering household load,
// vehicle charging and battery charging together, so a slot isn't "battery grid
// charge" just because grid_import and battery charging_power are both nonzero -
// the import may be fully explained by non-battery consumption (e.g. household
// load covered by grid while the battery charges from PV surplus). Only the
// import left over after household load and vehicle charging is attributable to
// the battery.
export function batteryChargeWindows(evopt: EvOpt | undefined): TimeWindow[] {
  if (!evopt?.res?.batteries || !evopt.details?.batteryDetails) return [];
  const slots = slotBounds(evopt);
  const details = evopt.details.batteryDetails;
  const gridImport = evopt.res.grid_import || [];
  const householdDemand = evopt.req?.time_series?.gt || [];

  const active = slots.map((_, i) => {
    const nonBatteryLoad =
      (householdDemand[i] || 0) +
      details.reduce((sum, d, bi) => {
        if (d.type !== "vehicle") return sum;
        return sum + (evopt.res.batteries[bi]?.charging_power?.[i] || 0);
      }, 0);
    if ((gridImport[i] || 0) - nonBatteryLoad <= 0) return false;
    return details.some((d, bi) => {
      if (d.type !== "battery") return false;
      return (evopt.res.batteries[bi]?.charging_power?.[i] || 0) > ACTIVE_THRESHOLD;
    });
  });
  return collapse(slots, active);
}

// batteryDischargeWindows returns the slots where a home battery is discharging
export function batteryDischargeWindows(evopt: EvOpt | undefined): TimeWindow[] {
  if (!evopt?.res?.batteries || !evopt.details?.batteryDetails) return [];
  const slots = slotBounds(evopt);
  const details = evopt.details.batteryDetails;

  const active = slots.map((_, i) => {
    return details.some((d, bi) => {
      if (d.type !== "battery") return false;
      return (evopt.res.batteries[bi]?.discharging_power?.[i] || 0) > ACTIVE_THRESHOLD;
    });
  });
  return collapse(slots, active);
}

// vehicleChargeWindows returns the charging slots per vehicle (loadpoint) entry.
// Keyed by the vehicle's own config name - the same key space state.vehicles
// (and therefore adaptivePlanMarkers below) uses - so a slot's color and a
// vehicle's plan marker resolve to the same color. Falls back to the loadpoint
// title only for entries the optimizer couldn't attribute to a config'd vehicle
// (e.g. a guest vehicle), which never carry adaptive plans anyway.
export function vehicleChargeWindows(evopt: EvOpt | undefined): KeyedWindows[] {
  if (!evopt?.res?.batteries || !evopt.details?.batteryDetails) return [];
  const slots = slotBounds(evopt);

  return evopt.details.batteryDetails.flatMap((detail, bi): KeyedWindows[] => {
    if (detail.type !== "vehicle") return [];
    const active = slots.map(
      (_, i) => (evopt.res.batteries[bi]?.charging_power?.[i] || 0) > ACTIVE_THRESHOLD
    );
    const windows = collapse(slots, active);
    if (!windows.length) return [];
    const key = detail.name || loadpointTitle(detail);
    return [{ key, title: detail.title || detail.name, windows }];
  });
}

export interface PlanMarker {
  key: string;
  title: string;
  time: number; // epoch ms
  soc: number;
}

// tzOffsetMs returns the offset (ms) between utcMs and how utcMs's wall-clock
// reading in tz would be numbered if read as UTC - i.e. formatToParts(utcMs) - utcMs.
function tzOffsetMs(utcMs: number, tz: string): number {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: tz,
    hourCycle: "h23",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).formatToParts(new Date(utcMs));
  const get = (type: string) => Number(parts.find((p) => p.type === type)?.value || 0);
  const asUtc = Date.UTC(
    get("year"),
    get("month") - 1,
    get("day"),
    get("hour"),
    get("minute"),
    get("second")
  );
  return asUtc - utcMs;
}

// zonedTimeToUtc returns the epoch ms for the given wall-clock date/time in tz.
// Two-pass correction: a one-shot correction (offset observed at the naive
// guess) is exact only when that offset also holds at the answer, which fails
// right around a DST transition. The offset is re-derived at the first
// candidate and, if it disagrees with the guess's offset, the candidate is
// redone with the re-derived offset - which is always consistent with itself
// (re-deriving a third time never disagrees again).
//
// Convention: this always converges on the offset that applies strictly after
// the transition instant. So an ambiguous local time (fall-back repeat, e.g.
// 02:30 occurring twice) resolves to its later occurrence, and a nonexistent
// local time (spring-forward gap, e.g. 02:30 when clocks jump 02:00->03:00)
// resolves by advancing past the gap to the post-transition clock.
function zonedTimeToUtc(
  year: number,
  month: number, // 1-12
  day: number,
  hour: number,
  minute: number,
  tz: string
): number {
  const wallAsUtc = Date.UTC(year, month - 1, day, hour, minute);
  const offset1 = tzOffsetMs(wallAsUtc, tz);
  const candidate = wallAsUtc - offset1;
  const offset2 = tzOffsetMs(candidate, tz);
  return offset2 === offset1 ? candidate : wallAsUtc - offset2;
}

// nextRepeatingOccurrence returns the next epoch ms the plan applies within
// [fromMs, horizonMs], or null when it is inactive, malformed, or out of range.
export function nextRepeatingOccurrence(
  plan: RepeatingPlan,
  fromMs: number,
  horizonMs: number
): number | null {
  if (!plan.active || !plan.weekdays?.length || !plan.tz || !plan.time) return null;

  const match = /^(\d{1,2}):(\d{2})$/.exec(plan.time);
  if (!match) return null;
  const hour = Number(match[1]);
  const minute = Number(match[2]);

  let todayParts: { year: number; month: number; day: number };
  try {
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone: plan.tz,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    }).formatToParts(new Date(fromMs));
    const get = (type: string) => Number(parts.find((p) => p.type === type)?.value);
    todayParts = { year: get("year"), month: get("month"), day: get("day") };
  } catch {
    return null; // invalid tz
  }

  const anchor = Date.UTC(todayParts.year, todayParts.month - 1, todayParts.day);
  const weekdays = new Set(plan.weekdays);

  for (let offset = 0; offset <= 7; offset++) {
    const candidateDate = new Date(anchor + offset * 24 * 3600 * 1000);
    if (!weekdays.has(candidateDate.getUTCDay())) continue;

    const time = zonedTimeToUtc(
      candidateDate.getUTCFullYear(),
      candidateDate.getUTCMonth() + 1,
      candidateDate.getUTCDate(),
      hour,
      minute,
      plan.tz
    );
    if (time >= fromMs && time <= horizonMs) return time;
  }

  return null;
}

// adaptivePlanMarkers builds ready-by markers for vehicles whose adaptive plans are
// currently active. User repeating plans always take precedence and are rendered
// elsewhere, so they are intentionally not considered here.
export function adaptivePlanMarkers(
  vehicles: {
    name?: string;
    title?: string;
    adaptivePlans?: RepeatingPlan[] | null;
    adaptivePlansActive?: boolean;
  }[],
  fromMs: number,
  horizonMs: number
): PlanMarker[] {
  return vehicles.flatMap((vehicle): PlanMarker[] => {
    if (!vehicle.adaptivePlansActive || !vehicle.adaptivePlans?.length) return [];
    return vehicle.adaptivePlans.flatMap((plan): PlanMarker[] => {
      const time = nextRepeatingOccurrence(plan, fromMs, horizonMs);
      if (time === null) return [];
      return [
        {
          key: vehicle.name || vehicle.title || "",
          title: vehicle.title || vehicle.name || "",
          time,
          soc: plan.soc,
        },
      ];
    });
  });
}
