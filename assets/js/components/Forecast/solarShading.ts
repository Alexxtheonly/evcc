// Learned solar shading map (ADR-016): types and pure view helpers.

export interface SolarShadingBin {
  azimuthFrom: number;
  azimuthTo: number;
  elevationFrom: number;
  elevationTo: number;
  factor: number;
  low: number;
  high: number;
  slots: number;
  weight: number;
  forecastKWh: number;
  lastSample: string;
  source: "learned" | "neighbour";
}

export type SolarShadingReason = "coordinates" | "noPv" | "insufficientData" | "error";

export interface SolarShadingStatus {
  enabled: boolean;
  active: boolean;
  available: boolean;
  reason?: SolarShadingReason;
  error?: string;
  location?: { lat: number; lon: number };
  leadMinutes: number;
  trainingWindows: { from: string; to: string }[];
  updated?: string;
  days: number;
  samples: number;
  fallback: "daily" | "none";
  fallbackFactor: number;
  parameters: {
    azimuthStep: number;
    elevationStep: number;
    halfLifeDays: number;
    windowDays: number;
    minFactor: number;
    maxFactor: number;
    minSlots: number;
  };
  bins: SolarShadingBin[];
}

export interface ShadingGrid {
  azimuths: number[]; // lower bin edges, x axis
  elevations: number[]; // lower bin edges, y axis
  cells: { x: number; y: number; bin: SolarShadingBin }[];
}

// The grid spans the azimuths the map has seen, one empty bin of margin on
// each side, and elevations from the horizon to the highest bin seen. Cells
// without a bin have no evidence and are left empty.
export function shadingGrid(status: Pick<SolarShadingStatus, "bins" | "parameters">): ShadingGrid {
  const { bins, parameters } = status;
  if (!bins.length) return { azimuths: [], elevations: [], cells: [] };
  const az = parameters.azimuthStep;
  const el = parameters.elevationStep;
  const minAz = Math.max(0, Math.min(...bins.map((b) => b.azimuthFrom)) - az);
  const maxAz = Math.min(360 - az, Math.max(...bins.map((b) => b.azimuthFrom)) + az);
  const maxEl = Math.max(...bins.map((b) => b.elevationFrom));
  const azimuths: number[] = [];
  for (let a = minAz; a <= maxAz + 1e-9; a += az) azimuths.push(a);
  const elevations: number[] = [];
  for (let e = 0; e <= maxEl + 1e-9; e += el) elevations.push(e);
  const cells = bins.map((bin) => ({
    x: Math.round((bin.azimuthFrom - minAz) / az),
    y: Math.round(bin.elevationFrom / el),
    bin,
  }));
  return { azimuths, elevations, cells };
}

function parseHex(color: string): [number, number, number] | null {
  let c = color.trim();
  if (/^#[0-9a-f]{3}$/i.test(c)) c = `#${Array.from(c.slice(1), (d) => d + d).join("")}`;
  const m = /^#([0-9a-f]{6})/i.exec(c);
  if (!m) return null;
  const n = parseInt(m[1]!, 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}

function mix(a: string, b: string, t: number): string {
  const ca = parseHex(a);
  const cb = parseHex(b);
  if (!ca || !cb) return t < 0.5 ? a : b;
  const c = ca.map((v, i) => Math.round(v + (cb[i]! - v) * t));
  return `#${c.map((v) => v.toString(16).padStart(2, "0")).join("")}`;
}

// Diverging colour around 1.0: factors below 1 (less than forecast) blend
// toward `below`, above 1 toward `above`, from the neutral colour. Full
// intensity at the clamp bounds.
export function factorColor(
  factor: number,
  range: { min: number; max: number },
  colors: { below: string; above: string; neutral: string }
): string {
  if (factor < 1) {
    const t = Math.min(1, (1 - factor) / Math.max(1e-9, 1 - range.min));
    return mix(colors.neutral, colors.below, 0.15 + 0.85 * t);
  }
  const t = Math.min(1, (factor - 1) / Math.max(1e-9, range.max - 1));
  return mix(colors.neutral, colors.above, 0.15 + 0.85 * t);
}

// i18n key for the status line, with its parameters
export function shadingStatusLine(status: SolarShadingStatus): {
  key: string;
  params: Record<string, string | number>;
} {
  if (!status.available) {
    return { key: `forecast.energy.shading.unavailable.${status.reason ?? "error"}`, params: {} };
  }
  return {
    key: status.active
      ? "forecast.energy.shading.statusActive"
      : "forecast.energy.shading.statusInactive",
    params: { days: status.days, slots: status.samples },
  };
}

// compass direction key for an azimuth on a 45° boundary, else undefined
export function compassKey(azimuth: number): string | undefined {
  const names = ["n", "ne", "e", "se", "s", "sw", "w", "nw"];
  return azimuth % 45 === 0 ? names[(azimuth / 45) % 8] : undefined;
}
