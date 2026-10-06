import type { HeatingDegreeStatus } from "../heatingDegree";

const thresholds = {
  minDays: 20,
  minSamples: 2000,
  minDailyR: 0.3,
  dayFill: 0.9,
  baseTemp: 15,
  capFloorKWh: 2,
  capShare: 0.25,
  autoMinSlotR: 0.3,
  shadowMinWindows: 14,
};

const day = (date: string, slots: number, uncorrectedKWh: number) => ({
  date,
  slots,
  uncorrectedKWh,
  uncappedKWh: 0,
  capKWh: Math.max((2 * slots) / 96, 0.25 * uncorrectedKWh),
  correctionKWh: 0,
  capped: false,
  autoKWh: 0,
  applied: false,
  autoApplied: false,
});

// the house on 2026-10-06 (evcc database copy): early autumn, gate shut
export const autumn = (): HeatingDegreeStatus => ({
  mode: "auto",
  temperatureForecast: true,
  applied: "none",
  open: false,
  reason: "daily_r_low",
  from: "2026-09-06T00:00:00+02:00",
  computed: "2026-10-06T00:00:41+02:00",
  samples: 2689,
  completeDays: 26,
  dailyR: -0.179,
  slotR: -0.124,
  slopeWhPerSlotK: -5.38,
  slopeKWhPerDayK: -0.516,
  meanHDD: 2.23,
  thresholds,
  updated: "2026-10-06T19:31:21+02:00",
  days: [day("2026-10-06", 18, 3.78), day("2026-10-07", 96, 18.07), day("2026-10-08", 78, 14.29)],
  shadow: [
    { window: "day", maxMissing: 4, windows: 0, required: 14, ready: false },
    { window: "overnight", maxMissing: 2, windows: 0, required: 14, ready: false },
  ],
});

// a February-shaped winter (the parity fixture's fit) with "on" selected and a
// ready shadow score
export const winter = (): HeatingDegreeStatus => ({
  ...autumn(),
  mode: "on",
  applied: "on",
  open: true,
  reason: undefined,
  from: "2026-01-21T00:00:00+01:00",
  computed: "2026-02-20T00:00:38+01:00",
  samples: 2805,
  completeDays: 29,
  dailyR: 0.997,
  slotR: 0.62,
  slopeWhPerSlotK: 9.49,
  slopeKWhPerDayK: 0.911,
  meanHDD: 9.78,
  updated: "2026-02-20T00:00:40+01:00",
  days: [
    {
      ...day("2026-02-20", 95, 33.44),
      uncappedKWh: 11.9,
      correctionKWh: 8.36,
      capped: true,
      autoKWh: 11.9,
      applied: true,
      autoApplied: true,
    },
    {
      ...day("2026-02-21", 96, 27.25),
      uncappedKWh: -2.53,
      correctionKWh: -2.53,
      autoKWh: -2.53,
      applied: true,
      autoApplied: true,
    },
  ],
  shadow: [
    {
      window: "day",
      maxMissing: 4,
      windows: 21,
      required: 14,
      ready: true,
      maeOnKWh: 2.98,
      maeAutoKWh: 3.73,
      deltaKWh: -0.75,
      ciLowKWh: -1.43,
      ciHighKWh: -0.26,
    },
    { window: "overnight", maxMissing: 2, windows: 9, required: 14, ready: false },
  ],
});
