// Household demand temperature correction (GET /api/energyplan/heatingdegree).

export type HeatingDegreeMode = "auto" | "on" | "off";

export type HeatingDegreeReason =
  | "no_tariff"
  | "few_days"
  | "few_samples"
  | "daily_r_low"
  | "slope_not_positive"
  | "error";

export interface HeatingDegreeDay {
  date: string;
  slots: number;
  uncorrectedKWh: number;
  uncappedKWh: number;
  capKWh: number;
  correctionKWh: number;
  capped: boolean;
  autoKWh: number;
  applied: boolean;
  autoApplied: boolean;
}

export interface HeatingDegreeShadowScore {
  window: "day" | "overnight";
  maxMissing: number;
  windows: number;
  required: number;
  ready: boolean;
  maeOnKWh?: number;
  maeAutoKWh?: number;
  deltaKWh?: number;
  ciLowKWh?: number;
  ciHighKWh?: number;
}

export interface HeatingDegreeStatus {
  mode: HeatingDegreeMode;
  temperatureForecast: boolean;
  applied: "auto" | "on" | "none";
  open: boolean;
  reason?: HeatingDegreeReason;
  error?: string;
  from?: string;
  computed?: string;
  samples: number;
  completeDays: number;
  dailyR?: number;
  slotR?: number;
  slopeWhPerSlotK?: number;
  slopeKWhPerDayK?: number;
  meanHDD?: number;
  thresholds: {
    minDays: number;
    minSamples: number;
    minDailyR: number;
    dayFill: number;
    baseTemp: number;
    capFloorKWh: number;
    capShare: number;
    autoMinSlotR: number;
    shadowMinWindows: number;
  };
  updated?: string;
  days: HeatingDegreeDay[];
  shadow: HeatingDegreeShadowScore[];
  shadowError?: string;
}

export interface GateCheck {
  key: "days" | "samples" | "dailyR" | "slope";
  value: number | undefined;
  threshold: number;
  met: boolean;
}

// gateChecks lists the "on" gate's conditions in the order the backend tests them.
export function gateChecks(s: HeatingDegreeStatus): GateCheck[] {
  const t = s.thresholds;
  return [
    { key: "days", value: s.completeDays, threshold: t.minDays, met: s.completeDays >= t.minDays },
    { key: "samples", value: s.samples, threshold: t.minSamples, met: s.samples >= t.minSamples },
    {
      key: "dailyR",
      value: s.dailyR,
      threshold: t.minDailyR,
      met: s.dailyR !== undefined && s.dailyR >= t.minDailyR,
    },
    {
      key: "slope",
      value: s.slopeWhPerSlotK,
      threshold: 0,
      met: s.slopeWhPerSlotK !== undefined && s.slopeWhPerSlotK > 0,
    },
  ];
}

// shadowVerdict summarises a ready score: "better" or "worse" only when the
// interval excludes zero.
export function shadowVerdict(s: HeatingDegreeShadowScore): "better" | "worse" | "unclear" {
  if (!s.ready || s.ciLowKWh === undefined || s.ciHighKWh === undefined) return "unclear";
  if (s.ciHighKWh < 0) return "better";
  if (s.ciLowKWh > 0) return "worse";
  return "unclear";
}
