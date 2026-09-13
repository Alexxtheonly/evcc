export interface EnergySettings {
  robust: boolean;
  arrivals: boolean;
  useLearnedEfficiency: boolean;
  batteryWear: Record<string, number>;
  batteryEnergyPlane?: Record<string, "ac" | "dc" | "unknown">;
  batteryEfficiency?: Record<string, { chargeEfficiency: number; dischargeEfficiency: number }>;
  settlementMode: "simulation" | "interval";
  settlementFrom?: string | null;
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
}

export interface EnergyDevice {
  key: string;
  name: string;
  title: string;
  kind: "battery" | "vehicle" | "expectedVehicle";
  capacityKWh: number;
  initialSoc: number;
  arrival?: string;
  departure?: string;
  plan: { start: string; chargeWh: number; dischargeWh: number; soc: number }[];
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

export function socPoints(values: number[]): string {
  return values
    .map((soc, index) => `${(index / Math.max(1, values.length - 1)) * 1000},${200 - soc * 2}`)
    .join(" ");
}

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
    if (!slot || (point.chargeWh <= 1 && point.dischargeWh <= 1)) continue;
    const charging = point.chargeWh > 1;
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
