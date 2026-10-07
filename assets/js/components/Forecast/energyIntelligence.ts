export interface EnergySettings {
  robust: boolean;
  arrivals: boolean;
  useLearnedEfficiency: boolean;
  batteryWear: Record<string, number>;
  batteryEnergyPlane?: Record<string, "ac" | "dc" | "unknown">;
  batteryEfficiency?: Record<string, { chargeEfficiency: number; dischargeEfficiency: number }>;
  settlementMode: "simulation" | "interval";
  settlementFrom?: string | null;
  solarShading?: boolean;
  heatingDegree?: "auto" | "on" | "off";
  // cheap-charge band reference (ADR-017); absent means the default
  cheapBandDays?: number;
  cheapBandMargin?: number;
  cheapBandPrice?: number;
}

export type EnergyBandStatus =
  | "inactive"
  | "reached"
  | "noReference"
  | "belowMinimum"
  | "charging"
  | "waitingSolar"
  | "waitingCheap"
  | "nothingCheap";

// A vehicle's cheap-charge band for the current plan (ADR-017).
export interface EnergyBand {
  status: EnergyBandStatus;
  topSoc: number;
  minRunMinutes: number;
  reference: {
    price?: number; // currency/kWh; absent without enough price history
    median?: number;
    source: "median" | "fixed";
    days: number;
    margin: number;
    samples: number;
  };
  nextStart?: string;
  nextPrice?: number;
}

export interface EnergyForecastSlot {
  start: string;
  end: string;
  homeLowWh: number;
  homeWh: number;
  homeHighWh: number;
  solarLowWh: number;
  solarWh: number;
  solarHighWh: number;
  gridPrice: number;
  gridImportWh?: number;
  gridChargeMinWh?: number;
  gridChargeMaxWh?: number;
}

export interface EnergyDevice {
  key: string;
  name: string;
  title: string;
  kind: "battery" | "vehicle" | "expectedVehicle";
  capacityKWh: number;
  initialSoc?: number;
  arrival?: string;
  departure?: string;
  plan: { start: string; chargeWh: number; dischargeWh: number; soc?: number }[];
  band?: EnergyBand;
}

export interface EnergyInsights {
  updated: string;
  automatic: boolean;
  status: "ready" | "degraded" | "unavailable";
  reason?: string;
  settings: EnergySettings;
  profile?: {
    source: string;
    samples: number;
    coveredBuckets: number;
    missingBuckets?: number[];
    rejectedSamples: number;
    lastGood?: string;
    lastGoodAgeSeconds?: number;
    reason?: string;
    rangeSource?: string;
    calibrationSamples?: number;
  };
  forecast?: EnergyForecastSlot[];
  devices?: EnergyDevice[];
  arrivalNotes?: { name: string; reason: string }[];
  economics?: {
    name: string;
    chargeEfficiency: number;
    dischargeEfficiency: number;
    source: string;
    candidateChargeEfficiency?: number;
    candidateDischargeEfficiency?: number;
    wearPerKWh?: number;
    calibrationReason?: string;
    measurementPlane?: string;
  }[];
  scenarios?: {
    status: string;
    reason?: string;
    selected?: string;
    costLow?: number;
    costHigh?: number;
    batterySocLow?: number[];
    batterySocHigh?: number[];
  };
  snapshotId?: number;
}

export const socTimeline = (device: EnergyDevice, slots: EnergyForecastSlot[]) => {
  if (!slots.length || device.capacityKWh <= 0) return [];
  return [
    ...(device.initialSoc != null && Number.isFinite(device.initialSoc)
      ? [{ time: slots[0]!.start, soc: device.initialSoc }]
      : []),
    ...device.plan.flatMap((point) => {
      const slot = slots.find((slot) => slot.start === point.start);
      return slot && point.soc != null && Number.isFinite(point.soc)
        ? [{ time: slot.end, soc: point.soc }]
        : [];
    }),
  ];
};

// Below this a slot is solver noise, not a planned transfer; shared by the headline, the
// schedule, the day tiles and the chart popup so they never disagree.
export const TRANSFER_MIN_WH = 20;

export function planWindows(device: EnergyDevice, slots: EnergyForecastSlot[]) {
  const windows: {
    start: string;
    end: string;
    energyWh: number;
    charging: boolean;
    index: number;
  }[] = [];
  for (const point of device.plan) {
    const index = slots.findIndex((slot) => slot.start === point.start);
    const slot = slots[index];
    if (!slot || (point.chargeWh < TRANSFER_MIN_WH && point.dischargeWh < TRANSFER_MIN_WH))
      continue;
    const charging = point.chargeWh >= TRANSFER_MIN_WH;
    const energyWh = charging ? point.chargeWh : point.dischargeWh;
    const previous = windows[windows.length - 1];
    if (previous && previous.end === point.start && previous.charging === charging) {
      previous.end = slot.end;
      previous.energyWh += energyWh;
    } else windows.push({ start: point.start, end: slot.end, energyWh, charging, index });
  }
  return windows;
}

export function forecastTotals(slots: EnergyForecastSlot[]) {
  return slots.reduce(
    (sum, slot) => ({
      home: sum.home + slot.homeWh / 1000,
      homeLow: sum.homeLow + slot.homeLowWh / 1000,
      homeHigh: sum.homeHigh + slot.homeHighWh / 1000,
      solar: sum.solar + slot.solarWh / 1000,
      solarLow: sum.solarLow + slot.solarLowWh / 1000,
      solarHigh: sum.solarHigh + slot.solarHighWh / 1000,
    }),
    { home: 0, homeLow: 0, homeHigh: 0, solar: 0, solarLow: 0, solarHigh: 0 }
  );
}

// Measured past from /api/energyplan/history, shaped like the plan. Every value is
// measured; null means not recorded (a gap), never zero.
export interface HistorySlot {
  start: string;
  end: string;
  measured: true;
  flagged?: boolean;
  homeWh: number | null;
  solarWh: number | null;
  gridImportWh: number | null;
  gridExportWh: number | null;
  batteryChargeWh: number | null;
  batteryDischargeWh: number | null;
  loadpointWh: number | null;
  gridChargeWh: number | null;
  gridPrice: number | null;
  feedInPrice: number | null;
  gridCost: number | null;
  feedInRevenue: number | null;
  planned?: {
    snapshotId: number;
    snapshot: string;
    // bounds of the governing run's entry; partial when the run started inside the slot
    start?: string;
    end?: string;
    gridChargeMinWh?: number;
    gridChargeMaxWh?: number;
    devices: {
      key: string;
      title: string;
      kind: string;
      chargeWh: number;
      dischargeWh: number;
      soc?: number;
    }[];
    // the controller's robust decision; sequence only when runs inside the slot changed it
    decision?: { action: string; reason?: string; sequence?: string[] };
  };
}

export interface HistoryDevice {
  key: string;
  name: string;
  title: string;
  kind: "battery" | "loadpoint";
  capacityKWh: number;
  // soc is the level at the slot START (the plan's is at the slot end)
  plan: {
    start: string;
    chargeWh: number | null;
    dischargeWh: number | null;
    soc: number | null;
    flagged?: boolean;
  }[];
}

export interface PlanHistory {
  from: string;
  to: string;
  forecast: HistorySlot[];
  devices: HistoryDevice[];
  unmapped?: { name: string; title: string }[];
}
