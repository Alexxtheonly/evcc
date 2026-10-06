import type { SolarShadingBin, SolarShadingStatus } from "../solarShading";

const parameters = {
  azimuthStep: 10,
  elevationStep: 5,
  halfLifeDays: 14,
  windowDays: 60,
  minFactor: 0.05,
  maxFactor: 1.5,
  minSlots: 4,
};

export const bin = (az: number, el: number, factor: number): SolarShadingBin => ({
  azimuthFrom: az,
  azimuthTo: az + 10,
  elevationFrom: el,
  elevationTo: el + 5,
  factor,
  low: factor * 0.7,
  high: factor * 1.2,
  slots: 22,
  weight: 17,
  forecastKWh: 3.1,
  lastSample: "2026-10-05T13:15:00+02:00",
  source: "learned",
});

export const status = (overrides: Partial<SolarShadingStatus> = {}): SolarShadingStatus => ({
  enabled: false,
  active: false,
  available: true,
  location: { lat: 49.16, lon: 9.17 },
  leadMinutes: 60,
  trainingWindows: [],
  updated: "2026-10-06T07:59:36+02:00",
  days: 43,
  samples: 2091,
  fallback: "daily",
  fallbackFactor: 0.87,
  parameters,
  bins: [bin(180, 35, 0.31), bin(130, 25, 1.09), bin(90, 0, 0.35)],
  ...overrides,
});
