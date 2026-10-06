import { expect, test } from "vite-plus/test";
import { gateChecks, shadowVerdict } from "./heatingDegree";
import { autumn, winter } from "./__fixtures__/heatingDegree";

test("gate checks follow the backend's conditions", () => {
  expect(gateChecks(autumn()).map((c) => [c.key, c.met])).toEqual([
    ["days", true],
    ["samples", true],
    ["dailyR", false],
    ["slope", false],
  ]);
  expect(gateChecks(winter()).every((c) => c.met)).toBe(true);
  const none = { ...autumn(), dailyR: undefined, slopeWhPerSlotK: undefined, completeDays: 19 };
  expect(gateChecks(none).map((c) => c.met)).toEqual([false, true, false, false]);
});

test("shadow verdict needs an interval that excludes zero", () => {
  const [day, night] = winter().shadow;
  expect(shadowVerdict(day!)).toBe("better");
  expect(shadowVerdict(night!)).toBe("unclear");
  expect(shadowVerdict({ ...day!, ciLowKWh: 0.1, ciHighKWh: 0.4 })).toBe("worse");
  expect(shadowVerdict({ ...day!, ciLowKWh: -0.2, ciHighKWh: 0.1 })).toBe("unclear");
});
