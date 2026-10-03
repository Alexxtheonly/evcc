import { BATTERY_MODE } from "@/types/evcc";

// Optimizer decisions encode api.BatteryMode numerically; site state uses its string name.
const wireModes = [
  BATTERY_MODE.UNKNOWN,
  BATTERY_MODE.NORMAL,
  BATTERY_MODE.HOLD,
  BATTERY_MODE.CHARGE,
  BATTERY_MODE.HOLDCHARGE,
];

export const decodeBatteryMode = (
  mode: BATTERY_MODE | number | undefined
): BATTERY_MODE | undefined => (typeof mode === "number" ? wireModes[mode] : mode);
