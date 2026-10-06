import {
  TRANSFER_MIN_WH,
  type EnergyDevice,
  type HistoryDevice,
  type HistorySlot,
  type PlanHistory,
} from "./energyIntelligence";
import {
  finite,
  gridChargeState,
  kw,
  ms,
  type ChartVisibility,
  type GridBlock,
  type GridChargeState,
  type Point,
  type TimeSlot,
} from "./planChartModel";

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
  flagged: boolean;
  // planned soc is the level at `socAt`, the end of the governing run's entry
  planned?: { state: TransferState; powerKW?: number; soc?: number; socAt: string };
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
  planned?: {
    snapshot: string;
    grid?: GridChargeState;
    // what the controller decided, not proof of what was applied
    decision?: { action: string; sequence?: string[] };
  };
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
  const planned = slot.planned;
  // a run made inside the slot planned only its remainder
  const entry = { start: planned?.start ?? slot.start, end: planned?.end ?? slot.end };
  const plannedGrid = planned
    ? gridChargeState({
        gridChargeMinWh: planned.gridChargeMinWh,
        gridChargeMaxWh: planned.gridChargeMaxWh,
      })
    : undefined;
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
      }
      const plan = planned?.devices.find((entry) => entry.key === history.key);
      if (plan) {
        const state = transfer(plan.chargeWh, plan.dischargeWh);
        detail.planned = {
          state,
          powerKW:
            state === "idle"
              ? undefined
              : kw(state === "charging" ? plan.chargeWh : plan.dischargeWh, entry),
          soc: history.kind === "battery" && finite(plan.soc) ? plan.soc : undefined,
          socAt: entry.end,
        };
      }
      return detail;
    }),
    planned: planned
      ? {
          snapshot: planned.snapshot,
          grid: layers.grid && plannedGrid?.kind !== "unavailable" ? plannedGrid : undefined,
          decision: planned.decision
            ? {
                action: planned.decision.action,
                ...(planned.decision.sequence ? { sequence: planned.decision.sequence } : {}),
              }
            : undefined,
        }
      : undefined,
  };
}
