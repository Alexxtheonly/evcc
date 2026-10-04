import type { EnergyDevice, EnergyForecastSlot } from "./energyIntelligence";

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

const hours = (slot: EnergyForecastSlot) => (ms(slot.end) - ms(slot.start)) / 3_600_000;

// Average power over the slot; slot lengths differ (partial first slot, coarse steps).
export const kw = (wh: number | undefined, slot: EnergyForecastSlot) =>
  finite(wh) && hours(slot) > 0 ? wh / 1000 / hours(slot) : undefined;

// Index of the slot containing `time`; the final edge belongs to the last slot.
export function slotIndexAt(slots: EnergyForecastSlot[], time: number) {
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
export function stepPoints(
  slots: EnergyForecastSlot[],
  value: (slot: EnergyForecastSlot, index: number) => number | undefined
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
export function stepBands(
  slots: EnergyForecastSlot[],
  range: (slot: EnergyForecastSlot) => [number | undefined, number | undefined]
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

export function gridChargeState(slot: EnergyForecastSlot): GridChargeState {
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
        if (point.chargeWh > 1)
          return { ...detail, soc, state: "charging" as const, powerKW: kw(point.chargeWh, slot) };
        if (point.dischargeWh > 1)
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
