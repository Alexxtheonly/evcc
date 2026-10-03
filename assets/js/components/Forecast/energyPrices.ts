import type { LedgerCoverage, LedgerChain } from "./savingsLedger.types";

export interface ImportPriceMetric {
  importKWh: number;
  importCost: number;
  weightedPrice?: number;
  coverage: LedgerCoverage;
}

export interface ConsumptionPriceMetric {
  consumptionKWh: number;
  importKWh: number;
  exportKWh: number;
  importCost: number;
  exportRevenue: number;
  netCost: number;
  effectivePrice?: number;
  coverage: LedgerCoverage;
}

export interface WorldPriceMetric extends ConsumptionPriceMetric {
  label: "W0" | "W1" | "W2" | "W3";
  weightedPrice?: number;
}

export interface EnergyPriceBucket {
  date: string;
  from: string;
  to: string;
  grid: ImportPriceMetric;
  consumption: ConsumptionPriceMetric;
  worlds: WorldPriceMetric[];
  inventoryBenefit?: number;
  evTiming?: EVTimingPrice;
}

export interface EnergyPriceReport {
  month: string;
  timezone: string;
  today?: string;
  from: string;
  to: string;
  evaluatedTo: string;
  earliest?: string;
  totals: EnergyPriceBucket;
  days: EnergyPriceBucket[];
  comparison: {
    status: "available" | "partial" | "unavailable";
    reason?: string;
    notes?: string[];
    inventoryAdjusted?: LedgerChain["inventoryAdjusted"];
  };
  evTiming: EVTimingPrice;
}

export interface EVTimingPrice {
  status: "estimated" | "partial" | "unavailable";
  reason?: string;
  includedSessions: number;
  excludedSessions: number;
  energyKWh?: number;
  actualTariffCost?: number;
  immediateTariffCost?: number;
  savings?: number;
}

export const siteCalendarDate = (now: Date, timezone: string): string => {
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: timezone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(now);
  return ["year", "month", "day"]
    .map((type) => parts.find((part) => part.type === type)!.value)
    .join("-");
};

export const calendarDate = (value: string): Date => {
  const [year, month, day = 1] = value.split("-").map(Number);
  return new Date(year!, month! - 1, day, 12);
};

export const calendarKey = (year: number, month: number, day?: number): string =>
  `${year}-${String(month).padStart(2, "0")}${day == null ? "" : `-${String(day).padStart(2, "0")}`}`;
