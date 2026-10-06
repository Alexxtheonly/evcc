import { batteryColor } from "@/colors";
import { stripVehicleTitle } from "../Optimize/chart";
import {
  TRANSFER_MIN_WH,
  type EnergyDevice,
  type EnergyForecastSlot,
  type HistoryDevice,
  type HistorySlot,
  type PlanHistory,
} from "./energyIntelligence";

export const FORECAST_LAYERS = ["price", "home", "solar", "grid"] as const;
export type ForecastLayer = (typeof FORECAST_LAYERS)[number];

export interface ChartVisibility {
  layers: Record<ForecastLayer, boolean>;
  soc: Record<string, boolean>;
  power: Record<string, boolean>;
}

// Uncluttered default: forecasts, price, grid-charge shading and every charge level.
// Per-device power is implied by the charge-level slope and stays opt-in.
export function defaultVisibility(devices: EnergyDevice[]): ChartVisibility {
  return {
    layers: { price: true, home: true, solar: true, grid: true },
    soc: Object.fromEntries(devices.map((device) => [device.key, device.capacityKWh > 0])),
    power: Object.fromEntries(devices.map((device) => [device.key, false])),
  };
}

export const hasSoc = (device: EnergyDevice) => device.capacityKWh > 0;

export interface ChartLayout {
  height: number;
  left: number;
  right: number;
  powerTop: number;
  powerHeight: number;
  socTop: number;
  socHeight: number;
}

export function chartLayout(showSoc: boolean): ChartLayout {
  const powerTop = 28;
  const powerHeight = 190;
  const socTop = powerTop + powerHeight + 26;
  const socHeight = showSoc ? 90 : 0;
  return {
    left: 40,
    right: 48,
    powerTop,
    powerHeight,
    socTop,
    socHeight,
    height: (showSoc ? socTop + socHeight : powerTop + powerHeight) + 44,
  };
}

export const finite = (value: number | undefined | null): value is number =>
  value != null && Number.isFinite(value);

export const ms = (time: string) => Date.parse(time);

// Anything drawn on the time axis: a plan slot or a measured one.
export interface TimeSlot {
  start: string;
  end: string;
}

const hours = (slot: TimeSlot) => (ms(slot.end) - ms(slot.start)) / 3_600_000;

// Average power over the slot; slot lengths differ (partial first slot, coarse steps).
export const kw = (wh: number | undefined | null, slot: TimeSlot) =>
  finite(wh) && hours(slot) > 0 ? wh / 1000 / hours(slot) : undefined;

// Index of the slot containing `time`; the final edge belongs to the last slot.
export function slotIndexAt(slots: TimeSlot[], time: number) {
  const index = slots.findIndex((slot) => time >= ms(slot.start) && time < ms(slot.end));
  if (index >= 0) return index;
  return slots.length && time === ms(slots.at(-1)!.end) ? slots.length - 1 : -1;
}

export function timeAtX(x: number, width: number, layout: ChartLayout, start: number, end: number) {
  const plot = Math.max(1, width - layout.left - layout.right);
  const ratio = Math.max(0, Math.min(1, (x - layout.left) / plot));
  return start + ratio * (end - start);
}

export function xAtTime(
  time: number,
  width: number,
  layout: ChartLayout,
  start: number,
  end: number
) {
  const plot = Math.max(1, width - layout.left - layout.right);
  return layout.left + ((time - start) / Math.max(1, end - start)) * plot;
}

export type PointerMode = "mouse" | "touch" | "keyboard";

// Keeps the popup inside the viewport using its rendered size. Mouse: beside the pointer,
// flipping left near the right edge. Touch: above the finger (which hides what is below),
// below only when there is no room above. Oversized popups are capped and scroll.
export function placeTooltip(
  anchor: { x: number; y: number },
  size: { width: number; height: number },
  viewport: { width: number; height: number },
  mode: PointerMode,
  margin = 8,
  gap = 16
) {
  const maxHeight = viewport.height - 2 * margin;
  const width = Math.min(size.width, viewport.width - 2 * margin);
  const height = Math.min(size.height, maxHeight);
  const clampX = (left: number) =>
    Math.max(margin, Math.min(viewport.width - width - margin, left));
  const clampY = (top: number) =>
    Math.max(margin, Math.min(viewport.height - height - margin, top));
  if (mode === "mouse") {
    const right = anchor.x + gap;
    const left = right + width + margin <= viewport.width ? right : anchor.x - gap - width;
    return { left: clampX(left), top: clampY(anchor.y - height / 2), maxHeight };
  }
  const above = anchor.y - gap - height;
  const top = above >= margin ? above : anchor.y + gap;
  return { left: clampX(anchor.x - width / 2), top: clampY(top), maxHeight };
}

export type Point = [number, number | null];

// Two points per slot so values hold over their interval; missing values break the line.
export function stepPoints<S extends TimeSlot>(
  slots: S[],
  value: (slot: S, index: number) => number | undefined
): Point[] {
  const points: Point[] = [];
  slots.forEach((slot, index) => {
    const v = value(slot, index);
    if (!finite(v)) {
      if (points.length && points.at(-1)![1] !== null) points.push([ms(slot.start), null]);
      return;
    }
    points.push([ms(slot.start), v], [ms(slot.end), v]);
  });
  return points;
}

export interface Band {
  upper: [number, number][];
  lower: [number, number][];
}

// Stepped ranges; a slot without a finite range splits the band instead of claiming zero.
export function stepBands<S extends TimeSlot>(
  slots: S[],
  range: (slot: S) => [number | undefined, number | undefined]
): Band[] {
  const bands: Band[] = [];
  let current: Band | undefined;
  for (const slot of slots) {
    const [low, high] = range(slot);
    if (!finite(low) || !finite(high)) {
      current = undefined;
      continue;
    }
    if (!current) bands.push((current = { upper: [], lower: [] }));
    current.upper.push([ms(slot.start), high], [ms(slot.end), high]);
    current.lower.push([ms(slot.start), low], [ms(slot.end), low]);
  }
  return bands;
}

// Scenario range for the single stationary battery; aggregate multi-battery bounds are not
// assigned to one device. Values are interval-end charge levels like the plan itself.
export function socRibbon(
  devices: EnergyDevice[],
  slots: EnergyForecastSlot[],
  low: number[] | undefined,
  high: number[] | undefined
): Band | undefined {
  const batteries = devices.filter((device) => device.kind === "battery");
  const device = batteries.length === 1 ? batteries[0] : undefined;
  if (
    !device ||
    !hasSoc(device) ||
    low?.length !== slots.length ||
    high?.length !== slots.length ||
    ![...low, ...high].every(Number.isFinite)
  )
    return undefined;
  const start = finite(device.initialSoc)
    ? [[ms(slots[0]!.start), device.initialSoc] as [number, number]]
    : [];
  return {
    lower: [...start, ...low.map((soc, index) => [ms(slots[index]!.end), soc] as [number, number])],
    upper: [
      ...start,
      ...high.map((soc, index) => [ms(slots[index]!.end), soc] as [number, number]),
    ],
  };
}

export interface GridBlock {
  start: number;
  end: number;
  definite: boolean;
  minWh: number;
  maxWh: number;
}

// Site-level grid charging: definite where the lower allocation bound is positive,
// possible where only the upper bound is. Contiguous slots of one kind merge.
export function gridChargeBlocks(slots: EnergyForecastSlot[]): GridBlock[] {
  const blocks: GridBlock[] = [];
  for (const slot of slots) {
    if (!finite(slot.gridChargeMinWh) || !finite(slot.gridChargeMaxWh) || slot.gridChargeMaxWh <= 1)
      continue;
    const definite = slot.gridChargeMinWh > 1;
    const previous = blocks.at(-1);
    if (previous && previous.end === ms(slot.start) && previous.definite === definite) {
      previous.end = ms(slot.end);
      previous.minWh += slot.gridChargeMinWh;
      previous.maxWh += slot.gridChargeMaxWh;
    } else
      blocks.push({
        start: ms(slot.start),
        end: ms(slot.end),
        definite,
        minWh: slot.gridChargeMinWh,
        maxWh: slot.gridChargeMaxWh,
      });
  }
  return blocks;
}

export type GridChargeState =
  | { kind: "unavailable" }
  | { kind: "none" }
  | { kind: "definite" | "possible"; minWh: number; maxWh: number };

export function gridChargeState(
  slot: Pick<EnergyForecastSlot, "gridChargeMinWh" | "gridChargeMaxWh">
): GridChargeState {
  if (!finite(slot.gridChargeMinWh) || !finite(slot.gridChargeMaxWh))
    return { kind: "unavailable" };
  if (slot.gridChargeMaxWh <= 1) return { kind: "none" };
  return {
    kind: slot.gridChargeMinWh > 1 ? "definite" : "possible",
    minWh: slot.gridChargeMinWh,
    maxWh: slot.gridChargeMaxWh,
  };
}

export interface DeviceSlotDetail {
  key: string;
  title: string;
  soc?: number;
  state: "charging" | "discharging" | "idle" | "away" | "noPlan";
  powerKW?: number;
}

export interface SlotDetails {
  start: string;
  end: string;
  price?: number;
  home?: { value?: number; low?: number; high?: number };
  solar?: { value?: number; low?: number; high?: number };
  grid?: GridChargeState;
  devices: DeviceSlotDetail[];
}

// Everything the crosshair popup shows for one slot, limited to visible layers.
export function slotDetails(
  index: number,
  slots: EnergyForecastSlot[],
  devices: EnergyDevice[],
  visibility: ChartVisibility
): SlotDetails | undefined {
  const slot = slots[index];
  if (!slot) return undefined;
  const { layers } = visibility;
  const time = ms(slot.start);
  return {
    start: slot.start,
    end: slot.end,
    price: layers.price && finite(slot.gridPrice) ? slot.gridPrice : undefined,
    home: layers.home
      ? {
          value: kw(slot.homeWh, slot),
          low: kw(slot.homeLowWh, slot),
          high: kw(slot.homeHighWh, slot),
        }
      : undefined,
    solar: layers.solar
      ? {
          value: kw(slot.solarWh, slot),
          low: kw(slot.solarLowWh, slot),
          high: kw(slot.solarHighWh, slot),
        }
      : undefined,
    grid: layers.grid ? gridChargeState(slot) : undefined,
    devices: devices
      .filter((device) => visibility.soc[device.key] || visibility.power[device.key])
      .map((device) => {
        const point = device.plan.find((plan) => plan.start === slot.start);
        const detail = { key: device.key, title: device.title };
        if (!point) return { ...detail, state: "noPlan" as const };
        if (
          (device.arrival && time < ms(device.arrival)) ||
          (device.departure && time >= ms(device.departure))
        )
          return { ...detail, state: "away" as const };
        const soc = hasSoc(device) && finite(point.soc) ? point.soc : undefined;
        if (point.chargeWh >= TRANSFER_MIN_WH)
          return { ...detail, soc, state: "charging" as const, powerKW: kw(point.chargeWh, slot) };
        if (point.dischargeWh >= TRANSFER_MIN_WH)
          return {
            ...detail,
            soc,
            state: "discharging" as const,
            powerKW: kw(point.dischargeWh, slot),
          };
        return { ...detail, soc, state: "idle" as const };
      }),
  };
}

export const SLOT_MS = 900_000;

type HistoryPoint = HistoryDevice["plan"][number];

// The measured window in front of the plan. `end` is the plan start ("now"); only
// complete slots that end by then are shown, so nothing is drawn twice at the seam.
export interface PastView {
  start: number;
  end: number;
  slots: HistorySlot[];
  devices: HistoryDevice[];
  points: Record<string, Map<number, HistoryPoint>>;
}

export function pastView(
  history: PlanHistory | undefined,
  planStart: number,
  hoursBack: number
): PastView | undefined {
  if (!history || hoursBack <= 0 || !Number.isFinite(planStart)) return undefined;
  const start = Math.floor(planStart / SLOT_MS) * SLOT_MS - hoursBack * 3_600_000;
  const slots = history.forecast.filter(
    (slot) => ms(slot.start) >= start && ms(slot.end) <= planStart
  );
  const points = Object.fromEntries(
    history.devices.map((device) => [
      device.key,
      new Map(device.plan.map((point) => [ms(point.start), point])),
    ])
  );
  return { start, end: planStart, slots, devices: history.devices, points };
}

export const pastPoint = (view: PastView, key: string, slot: TimeSlot) =>
  view.points[key]?.get(ms(slot.start));

// Measured grid charging is a fact, so it only ever shades as "definite".
export function pastGridBlocks(slots: HistorySlot[]): GridBlock[] {
  const blocks: GridBlock[] = [];
  for (const slot of slots) {
    const wh = slot.gridChargeWh;
    if (!finite(wh) || wh < TRANSFER_MIN_WH) continue;
    const previous = blocks.at(-1);
    if (previous && previous.end === ms(slot.start)) {
      previous.end = ms(slot.end);
      previous.minWh += wh;
      previous.maxWh += wh;
    } else
      blocks.push({
        start: ms(slot.start),
        end: ms(slot.end),
        definite: true,
        minWh: wh,
        maxWh: wh,
      });
  }
  return blocks;
}

// Measured slot-start levels, joined to the plan's initial level at "now" when the
// last measured slot ends right before it.
export function pastSocPoints(view: PastView, key: string, initialSoc?: number): Point[] {
  const points: Point[] = [];
  for (const slot of view.slots) {
    const soc = pastPoint(view, key, slot)?.soc;
    if (finite(soc)) points.push([ms(slot.start), soc]);
    else if (points.length && points.at(-1)![1] !== null) points.push([ms(slot.start), null]);
  }
  const last = view.slots.at(-1);
  if (
    last &&
    finite(initialSoc) &&
    finite(pastPoint(view, key, last)?.soc) &&
    ms(last.end) >= view.end - SLOT_MS
  )
    points.push([view.end, initialSoc]);
  return points;
}

// Net measured power of a device: charging above zero, discharging below.
export function pastDevicePower(view: PastView, key: string, slot: TimeSlot) {
  const point = pastPoint(view, key, slot);
  if (!point || !finite(point.chargeWh)) return undefined;
  const discharge = point.dischargeWh ?? 0;
  return finite(discharge) ? kw(point.chargeWh - discharge, slot) : undefined;
}

type TransferState = "charging" | "discharging" | "idle";

const transfer = (chargeWh: number, dischargeWh: number): TransferState =>
  chargeWh >= TRANSFER_MIN_WH
    ? "charging"
    : dischargeWh >= TRANSFER_MIN_WH
      ? "discharging"
      : "idle";

export interface PastDeviceDetail {
  key: string;
  title: string;
  soc?: number;
  socEnd?: number;
  state: TransferState | "noData";
  powerKW?: number;
  gridWh?: number;
  flagged: boolean;
  planned?: { state: TransferState; powerKW?: number; soc?: number };
}

export interface PastSlotDetails {
  measured: true;
  start: string;
  end: string;
  flagged: boolean;
  price?: number;
  home?: number;
  solar?: number;
  gridImport?: number;
  gridExport?: number;
  gridChargeWh?: number;
  gridCost?: number;
  feedInRevenue?: number;
  devices: PastDeviceDetail[];
  planned?: { snapshot: string; grid?: GridChargeState };
}

// The popup for a measured slot: what happened, and - when a snapshot covers the slot -
// what the plan in force at its start intended. Plan devices keep their visibility
// toggles; measured devices without a plan entry show only when they moved energy.
export function pastSlotDetails(
  index: number,
  view: PastView,
  devices: EnergyDevice[],
  visibility: ChartVisibility
): PastSlotDetails | undefined {
  const slot = view.slots[index];
  if (!slot) return undefined;
  const { layers } = visibility;
  const next = view.slots[index + 1];
  const contiguous = next && ms(next.start) === ms(slot.end);
  const batteries = view.devices.filter((device) => device.kind === "battery");
  const planned = slot.planned;
  const planDevices = new Map(devices.map((device) => [device.key, device]));

  const shown = view.devices.filter((history) => {
    const device = planDevices.get(history.key);
    if (device) return visibility.soc[device.key] || visibility.power[device.key];
    const point = pastPoint(view, history.key, slot);
    return (
      (point?.chargeWh ?? 0) >= TRANSFER_MIN_WH || (point?.dischargeWh ?? 0) >= TRANSFER_MIN_WH
    );
  });
  const ordered = [
    ...devices.flatMap((device) => shown.filter((history) => history.key === device.key)),
    ...shown.filter((history) => !planDevices.has(history.key)),
  ];

  return {
    measured: true,
    start: slot.start,
    end: slot.end,
    flagged: !!slot.flagged,
    price: layers.price && finite(slot.gridPrice) ? slot.gridPrice : undefined,
    home: layers.home ? kw(slot.homeWh, slot) : undefined,
    solar: layers.solar ? kw(slot.solarWh, slot) : undefined,
    gridImport: kw(slot.gridImportWh, slot),
    gridExport: kw(slot.gridExportWh, slot),
    gridChargeWh: layers.grid && finite(slot.gridChargeWh) ? slot.gridChargeWh : undefined,
    gridCost: finite(slot.gridCost) ? slot.gridCost : undefined,
    feedInRevenue: finite(slot.feedInRevenue) ? slot.feedInRevenue : undefined,
    devices: ordered.map((history) => {
      const device = planDevices.get(history.key);
      const point = pastPoint(view, history.key, slot);
      const detail: PastDeviceDetail = {
        key: history.key,
        title: device?.title ?? history.title,
        state: "noData",
        flagged: !!point?.flagged,
      };
      if (history.kind === "battery") {
        if (finite(point?.soc)) detail.soc = point.soc;
        const end = contiguous ? pastPoint(view, history.key, next)?.soc : undefined;
        if (finite(end)) detail.socEnd = end;
      }
      if (point && finite(point.chargeWh)) {
        const discharge = finite(point.dischargeWh) ? point.dischargeWh : 0;
        detail.state = transfer(point.chargeWh, discharge);
        if (detail.state !== "idle")
          detail.powerKW = kw(detail.state === "charging" ? point.chargeWh : discharge, slot);
        // the site-level grid share belongs to a device only when there is one battery
        if (
          detail.state === "charging" &&
          history.kind === "battery" &&
          batteries.length === 1 &&
          finite(slot.gridChargeWh)
        )
          detail.gridWh = slot.gridChargeWh;
      }
      const plan = planned?.devices.find((entry) => entry.key === history.key);
      if (plan) {
        const state = transfer(plan.chargeWh, plan.dischargeWh);
        detail.planned = {
          state,
          powerKW:
            state === "idle"
              ? undefined
              : kw(state === "charging" ? plan.chargeWh : plan.dischargeWh, slot),
          soc: history.kind === "battery" && finite(plan.soc) ? plan.soc : undefined,
        };
      }
      return detail;
    }),
    planned: planned
      ? {
          snapshot: planned.snapshot,
          grid: layers.grid
            ? gridChargeState({
                gridChargeMinWh: planned.gridChargeMinWh,
                gridChargeMaxWh: planned.gridChargeMaxWh,
              })
            : undefined,
        }
      : undefined,
  };
}

// Batteries keep evcc's battery palette (as on the Results tab and battery pages); default car
// colours skip greens and amber/orange hues that would read as battery, solar or the price
// line. User colours win; evcc keys them by the loadpoint title without "(vehicle)".
const AVOID_FOR_CARS = new Set(
  ["#FBBF24", "#F59E0B", "#B45309", "#34D399", "#10B981", "#047857"].map((color) =>
    color.toUpperCase()
  )
);

// Neighbouring cars get far-apart hues first (blue, pink, cyan) instead of palette order.
const CAR_PREFERENCE = ["#60A5FA", "#F472B6", "#22D3EE"];

export function deviceColorAssignment(
  devices: EnergyDevice[],
  overrides: Record<string, string | undefined>,
  palette: string[]
): Record<string, string> {
  const override = (device: EnergyDevice) => overrides[stripVehicleTitle(device.title)];
  const taken = new Set(
    devices.flatMap((device) =>
      device.kind !== "battery" && override(device) ? [override(device)!.toUpperCase()] : []
    )
  );
  const free = [...CAR_PREFERENCE, ...palette]
    .filter(
      (color, index, all) => all.findIndex((c) => c.toUpperCase() === color.toUpperCase()) === index
    )
    .filter((color) => palette.some((c) => c.toUpperCase() === color.toUpperCase()))
    .filter((color) => !AVOID_FOR_CARS.has(color.toUpperCase()) && !taken.has(color.toUpperCase()));
  let batteryIndex = 0;
  let carIndex = 0;
  return Object.fromEntries(
    devices.map((device) => {
      if (device.kind === "battery") return [device.key, batteryColor(batteryIndex++)];
      return [
        device.key,
        override(device) || free[carIndex++ % Math.max(1, free.length)] || palette[0]!,
      ];
    })
  );
}
