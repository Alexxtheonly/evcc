import {
  TRANSFER_MIN_WH,
  socTimeline,
  type EnergyDevice,
  type EnergyForecastSlot,
  type EnergyInsights,
} from "./energyIntelligence";

const MIN_SLOT_WH = TRANSFER_MIN_WH;
// Windows below this total are not worth a headline sentence (they stay in the schedule).
const MIN_WINDOW_WH = 300;
// Short idle gaps (up to 30 min) inside a transfer do not split it into two sentences.
const MAX_GAP_SLOTS = 2;
// Charging counts as solar-only when the grid can supply at most this share of it.
const SOLAR_ONLY_SHARE = 0.05;

export interface TransferWindow {
  start: string;
  end: string;
  energyWh: number;
  charging: boolean;
  firstIndex: number;
  lastIndex: number;
}

const finite = (value: number | undefined | null): value is number =>
  value != null && Number.isFinite(value);

const planBySlot = (device: EnergyDevice, slots: EnergyForecastSlot[]) => {
  const byStart = new Map(device.plan.map((point) => [point.start, point]));
  return slots.map((slot) => byStart.get(slot.start));
};

// Contiguous charge or discharge runs, tolerating short idle gaps and dropping noise.
export function significantWindows(
  device: EnergyDevice,
  slots: EnergyForecastSlot[]
): TransferWindow[] {
  const points = planBySlot(device, slots);
  const windows: (TransferWindow & { idle: number })[] = [];
  points.forEach((point, index) => {
    const charge = point?.chargeWh ?? 0;
    const discharge = point?.dischargeWh ?? 0;
    const charging = charge >= MIN_SLOT_WH;
    const active = charging || discharge >= MIN_SLOT_WH;
    const current = windows.at(-1);
    if (!active) {
      if (current) current.idle++;
      return;
    }
    const energyWh = charging ? charge : discharge;
    if (current && current.charging === charging && current.idle <= MAX_GAP_SLOTS) {
      current.end = slots[index]!.end;
      current.energyWh += energyWh;
      current.lastIndex = index;
      current.idle = 0;
      return;
    }
    windows.push({
      start: slots[index]!.start,
      end: slots[index]!.end,
      energyWh,
      charging,
      firstIndex: index,
      lastIndex: index,
      idle: 0,
    });
  });
  return windows
    .filter((window) => window.energyWh >= MIN_WINDOW_WH)
    .map(({ idle: _idle, ...window }) => window);
}

const weightedPrice = (slots: EnergyForecastSlot[], weights: number[]) => {
  let energy = 0;
  let cost = 0;
  slots.forEach((slot, index) => {
    const weight = weights[index] ?? 0;
    if (weight > 0 && finite(slot.gridPrice)) {
      energy += weight;
      cost += weight * slot.gridPrice;
    }
  });
  return energy > 0 ? cost / energy : undefined;
};

export type ChargeSource = "solar" | "unknown";

export type HeadlineItem =
  | { kind: "unavailable" }
  | { kind: "idle"; end: string }
  | {
      kind: "batteryCharge";
      device: string;
      start: string;
      end: string;
      energyWh: number;
      source: ChargeSource;
      next?: { start: string; end: string; energyWh: number };
    }
  | {
      kind: "batteryDischarge";
      device: string;
      start: string;
      end: string;
      energyWh: number;
      price?: number;
    }
  | {
      kind: "vehicleCharge";
      device: string;
      start: string;
      end: string;
      energyWh: number;
      expected: boolean;
      endSoc?: number;
    }
  | {
      kind: "gridCharge";
      start: string;
      end: string;
      minWh: number;
      maxWh: number;
      price?: number;
    };

// The grid fields are site-level: they bound how much of all charging the grid supplies.
// "solar" is only claimed when the grid can supply (almost) none of it.
function chargeSource(window: TransferWindow, slots: EnergyForecastSlot[]): ChargeSource {
  const covered = slots.slice(window.firstIndex, window.lastIndex + 1);
  if (!covered.every((slot) => finite(slot.gridChargeMaxWh))) return "unknown";
  const gridMax = covered.reduce((sum, slot) => sum + slot.gridChargeMaxWh!, 0);
  return gridMax <= window.energyWh * SOLAR_ONLY_SHARE ? "solar" : "unknown";
}

// Consecutive windows of one direction form a phase: "charges during the day" stays one
// statement even when a car takes the solar surplus for an hour in between.
function phases(windows: TransferWindow[]) {
  const result: TransferWindow[][] = [];
  for (const window of windows) {
    const current = result.at(-1);
    if (current && current[0]!.charging === window.charging) current.push(window);
    else result.push([window]);
  }
  return result;
}

const span = (phase: TransferWindow[]) => ({
  start: phase[0]!.start,
  end: phase.at(-1)!.end,
  energyWh: phase.reduce((sum, window) => sum + window.energyWh, 0),
});

function batteryItems(device: EnergyDevice, slots: EnergyForecastSlot[]): HeadlineItem[] {
  const [first, second] = phases(significantWindows(device, slots));
  if (!first) return [];
  if (!first[0]!.charging) {
    const points = planBySlot(device, slots);
    const indices = first.flatMap((window) =>
      Array.from(
        { length: window.lastIndex - window.firstIndex + 1 },
        (_, i) => window.firstIndex + i
      )
    );
    return [
      {
        kind: "batteryDischarge",
        device: device.title,
        ...span(first),
        price: weightedPrice(
          indices.map((index) => slots[index]!),
          indices.map((index) => points[index]?.dischargeWh ?? 0)
        ),
      },
    ];
  }
  return [
    {
      kind: "batteryCharge",
      device: device.title,
      ...span(first),
      source: first.every((window) => chargeSource(window, slots) === "solar")
        ? "solar"
        : "unknown",
      next: second ? span(second) : undefined,
    },
  ];
}

function vehicleItem(device: EnergyDevice, slots: EnergyForecastSlot[]): HeadlineItem[] {
  const windows = significantWindows(device, slots).filter((window) => window.charging);
  const first = windows[0];
  const last = windows.at(-1);
  if (!first || !last) return [];
  const endPoint = planBySlot(device, slots)[last.lastIndex];
  return [
    {
      kind: "vehicleCharge",
      device: device.title,
      start: first.start,
      end: last.end,
      energyWh: windows.reduce((sum, window) => sum + window.energyWh, 0),
      expected: device.kind === "expectedVehicle",
      endSoc: device.capacityKWh > 0 && finite(endPoint?.soc) ? endPoint!.soc : undefined,
    },
  ];
}

// Site-level grid charging the plan definitely needs (lower allocation bound).
function gridChargeItem(slots: EnergyForecastSlot[]): HeadlineItem[] {
  const definite = slots.filter((slot) => (slot.gridChargeMinWh ?? 0) >= MIN_SLOT_WH);
  const minWh = definite.reduce((sum, slot) => sum + slot.gridChargeMinWh!, 0);
  if (minWh < MIN_WINDOW_WH) return [];
  return [
    {
      kind: "gridCharge",
      start: definite[0]!.start,
      end: definite.at(-1)!.end,
      minWh,
      maxWh: definite.reduce((sum, slot) => sum + (slot.gridChargeMaxWh ?? 0), 0),
      price: weightedPrice(
        definite,
        definite.map((slot) => slot.gridChargeMinWh!)
      ),
    },
  ];
}

const HEADLINE_LIMIT = 3;

const startOf = (item: HeadlineItem) => ("start" in item ? item.start : "");

// Plain-language summary of what the plan does next, ordered by start time.
export function planHeadline(insights: EnergyInsights | undefined): HeadlineItem[] {
  const slots = insights?.forecast ?? [];
  if (!insights || insights.status === "unavailable" || !slots.length)
    return [{ kind: "unavailable" }];
  const devices = insights.devices ?? [];
  const items = [
    ...devices
      .filter((device) => device.kind === "battery")
      .flatMap((device) => batteryItems(device, slots)),
    ...devices
      .filter((device) => device.kind !== "battery")
      .flatMap((device) => vehicleItem(device, slots)),
  ];
  const grid = gridChargeItem(slots);
  if (!items.length && !grid.length) return [{ kind: "idle", end: slots.at(-1)!.end }];
  const byStart = (a: HeadlineItem, b: HeadlineItem) =>
    Date.parse(startOf(a)) - Date.parse(startOf(b));
  // buying from the grid always gets a sentence; device statements share the rest
  return [...items.sort(byStart).slice(0, HEADLINE_LIMIT - grid.length), ...grid].sort(byStart);
}

export interface EnergyRange {
  value?: number;
  low?: number;
  high?: number;
}

export interface DaySummary {
  key: string;
  start: string;
  end: string;
  partial: boolean;
  hours: number;
  home: EnergyRange;
  solar: EnergyRange;
  gridImportKWh?: number;
  vehicleChargeKWh: number;
  batteries: {
    key: string;
    title: string;
    chargeKWh: number;
    dischargeKWh: number;
    minSoc?: number;
    maxSoc?: number;
  }[];
}

const dayFormat = (timeZone?: string) =>
  new Intl.DateTimeFormat("en-CA", {
    timeZone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  });

const localParts = (format: Intl.DateTimeFormat, time: string) => {
  const parts = Object.fromEntries(
    format.formatToParts(new Date(time)).map((part) => [part.type, part.value])
  );
  return {
    key: `${parts["year"]}-${parts["month"]}-${parts["day"]}`,
    midnight: parts["hour"] === "00" && parts["minute"] === "00",
  };
};

const rangeSum = (
  slots: EnergyForecastSlot[],
  pick: (slot: EnergyForecastSlot) => [number, number, number]
): EnergyRange => {
  let value: number | undefined = 0;
  let low: number | undefined = 0;
  let high: number | undefined = 0;
  for (const slot of slots) {
    const [l, v, h] = pick(slot);
    value = value != null && finite(v) ? value + v : undefined;
    low = low != null && finite(l) ? low + l : undefined;
    high = high != null && finite(h) ? high + h : undefined;
  }
  return {
    value: value != null ? value / 1000 : undefined,
    low: low != null ? low / 1000 : undefined,
    high: high != null ? high / 1000 : undefined,
  };
};

// Per local calendar day: slots are assigned by their start time; a day is partial when the
// plan does not cover it from midnight to midnight (DST days count their real 23 or 25 hours).
export function daySummaries(
  slots: EnergyForecastSlot[],
  devices: EnergyDevice[],
  timeZone?: string
): DaySummary[] {
  const format = dayFormat(timeZone);
  const groups: { key: string; indices: number[] }[] = [];
  slots.forEach((slot, index) => {
    const { key } = localParts(format, slot.start);
    const group = groups.at(-1);
    if (group?.key === key) group.indices.push(index);
    else groups.push({ key, indices: [index] });
  });
  const batteries = devices.filter((device) => device.kind === "battery");
  const vehicles = devices.filter((device) => device.kind !== "battery");
  const plans = new Map(devices.map((device) => [device.key, planBySlot(device, slots)]));
  const socs = new Map(
    batteries.map((device) => [
      device.key,
      new Map(socTimeline(device, slots).map((point) => [point.time, point.soc])),
    ])
  );

  return groups.map(({ key, indices }, groupIndex) => {
    const daySlots = indices.map((index) => slots[index]!);
    const first = daySlots[0]!;
    const last = daySlots.at(-1)!;
    const startsAtMidnight = localParts(format, first.start).midnight;
    const endParts = localParts(format, last.end);
    const endsAtMidnight = endParts.midnight && endParts.key !== key;
    const sumPlan = (device: EnergyDevice, field: "chargeWh" | "dischargeWh") =>
      indices.reduce((sum, index) => {
        const value = plans.get(device.key)![index]?.[field] ?? 0;
        return value >= TRANSFER_MIN_WH ? sum + value : sum;
      }, 0) / 1000;
    return {
      key,
      start: first.start,
      end: last.end,
      partial: !startsAtMidnight || !endsAtMidnight,
      hours:
        daySlots.reduce((sum, slot) => sum + Date.parse(slot.end) - Date.parse(slot.start), 0) /
        3_600_000,
      home: rangeSum(daySlots, (slot) => [slot.homeLowWh, slot.homeWh, slot.homeHighWh]),
      solar: rangeSum(daySlots, (slot) => [slot.solarLowWh, slot.solarWh, slot.solarHighWh]),
      gridImportKWh: daySlots.every((slot) => finite(slot.gridImportWh))
        ? daySlots.reduce((sum, slot) => sum + slot.gridImportWh!, 0) / 1000
        : undefined,
      vehicleChargeKWh: vehicles.reduce((sum, device) => sum + sumPlan(device, "chargeWh"), 0),
      batteries: batteries.map((device) => {
        const levels = socs.get(device.key)!;
        const values = [
          ...(groupIndex === 0 ? [levels.get(first.start)] : []),
          ...daySlots.map((slot) => levels.get(slot.end)),
        ].filter(finite);
        return {
          key: device.key,
          title: device.title,
          chargeKWh: sumPlan(device, "chargeWh"),
          dischargeKWh: sumPlan(device, "dischargeWh"),
          minSoc: values.length ? Math.min(...values) : undefined,
          maxSoc: values.length ? Math.max(...values) : undefined,
        };
      }),
    };
  });
}
