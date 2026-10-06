import type { GridBlock, Point } from "./planChartModel";
import type { ChartInput } from "./planChartOption";

// Translucent variant of a theme colour; hex is converted to rgba because the renderer
// needs a colour it can parse.
export function alpha(color: string, value: number) {
  const hex = color.trim().replace(/^#/, "");
  const full =
    hex.length === 3 || hex.length === 4
      ? hex
          .slice(0, 3)
          .split("")
          .map((c) => c + c)
          .join("")
      : hex.slice(0, 6);
  if (/^[0-9a-f]{6}$/i.test(full) && color.trim().startsWith("#")) {
    const [r, g, b] = [0, 2, 4].map((offset) => parseInt(full.slice(offset, offset + 2), 16));
    return `rgba(${r}, ${g}, ${b}, ${value})`;
  }
  return `color-mix(in srgb, ${color} ${Math.round(value * 100)}%, transparent)`;
}

export function lineSeries(
  id: string,
  data: Point[] | [number, number][],
  color: string,
  axis: { xAxisIndex: number; yAxisIndex: number },
  options: { width?: number; dashed?: boolean; area?: string } = {}
) {
  return {
    id,
    type: "line",
    silent: true,
    animation: false,
    showSymbol: false,
    ...axis,
    z: 3,
    data,
    lineStyle: { color, width: options.width ?? 2, type: options.dashed ? "dashed" : "solid" },
    ...(options.area ? { areaStyle: { color: options.area, origin: 0 } } : {}),
  };
}

export type Axis = { xAxisIndex: number; yAxisIndex: number };
export const POWER: Axis = { xAxisIndex: 0, yAxisIndex: 0 };
export const PRICE: Axis = { xAxisIndex: 0, yAxisIndex: 1 };
export const SOC: Axis = { xAxisIndex: 1, yAxisIndex: 2 };

// Grid-charging shading over the given blocks, drawn in every visible grid.
export function gridShading(
  input: ChartInput,
  showSoc: boolean,
  blocks: GridBlock[],
  prefix: string
) {
  const { price } = input.colors;
  if (!blocks.length) return [];
  const shading = (axis: Axis, id: string) => ({
    id,
    type: "line",
    data: [],
    silent: true,
    ...axis,
    markArea: {
      silent: true,
      data: blocks.map((block) => [
        {
          xAxis: block.start,
          itemStyle: block.definite
            ? { color: alpha(price, 0.22) }
            : {
                color: alpha(price, 0.07),
                borderColor: alpha(price, 0.6),
                borderWidth: 1,
                borderType: "dashed",
              },
        },
        { xAxis: block.end },
      ]),
    },
  });
  return [shading(POWER, `${prefix}-power`), ...(showSoc ? [shading(SOC, `${prefix}-soc`)] : [])];
}
