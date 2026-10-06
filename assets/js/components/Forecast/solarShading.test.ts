import { describe, expect, test } from "vite-plus/test";
import { compassKey, factorColor, shadingGrid, shadingStatusLine } from "./solarShading";
import { bin, status } from "./__fixtures__/solarShading";

describe("shadingGrid", () => {
  test("spans seen azimuths with one bin margin and elevations from the horizon", () => {
    const grid = shadingGrid(status());
    expect(grid.azimuths[0]).toBe(80);
    expect(grid.azimuths.at(-1)).toBe(190);
    expect(grid.elevations).toEqual([0, 5, 10, 15, 20, 25, 30, 35]);
    expect(grid.cells.map((c) => [c.x, c.y])).toEqual([
      [10, 7],
      [5, 5],
      [1, 0],
    ]);
  });

  test("no bins, no grid", () => {
    expect(shadingGrid(status({ bins: [] }))).toEqual({ azimuths: [], elevations: [], cells: [] });
  });

  test("margin stays on the circle", () => {
    const grid = shadingGrid(status({ bins: [bin(0, 0, 1), bin(350, 0, 1)] }));
    expect(grid.azimuths[0]).toBe(0);
    expect(grid.azimuths.at(-1)).toBe(350);
  });
});

describe("factorColor", () => {
  const colors = { below: "#ff0000", above: "#0000ff", neutral: "#fff" };
  const range = { min: 0.05, max: 1.5 };
  test("diverges around 1 from a neutral tint", () => {
    expect(factorColor(1, range, colors)).toBe("#d9d9ff");
    expect(factorColor(0.05, range, colors)).toBe("#ff0000");
    expect(factorColor(1.5, range, colors)).toBe("#0000ff");
    expect(factorColor(9, range, colors)).toBe("#0000ff");
    const half = factorColor(0.5, range, colors);
    expect(half.slice(1, 3)).toBe("ff");
    expect(half).not.toBe("#ff0000");
  });
});

describe("shadingStatusLine", () => {
  test("explains each state", () => {
    expect(shadingStatusLine(status()).key).toBe("forecast.energy.shading.statusInactive");
    expect(shadingStatusLine(status({ enabled: true, active: true }))).toEqual({
      key: "forecast.energy.shading.statusActive",
      params: { days: 43, slots: 2091 },
    });
    expect(
      shadingStatusLine(status({ available: false, reason: "coordinates", bins: [] })).key
    ).toBe("forecast.energy.shading.unavailable.coordinates");
    expect(shadingStatusLine(status({ available: false })).key).toBe(
      "forecast.energy.shading.unavailable.error"
    );
  });
});

test("compassKey", () => {
  expect(compassKey(90)).toBe("e");
  expect(compassKey(225)).toBe("sw");
  expect(compassKey(100)).toBeUndefined();
});
