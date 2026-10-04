import { socTimeline } from "./energyIntelligence";
import {
  chartLayout,
  finite,
  gridChargeBlocks,
  hasSoc,
  kw,
  ms,
  socRibbon,
  stepBands,
  stepPoints,
  type Band,
  type ChartLayout,
  type ChartVisibility,
  type Point,
} from "./planChartModel";
import type { EnergyDevice, EnergyForecastSlot } from "./energyIntelligence";

export interface ChartColors {
  text: string;
  muted: string;
  border: string;
  price: string;
  solar: string;
  devices: Record<string, string>;
}

export interface ChartInput {
  slots: EnergyForecastSlot[];
  devices: EnergyDevice[];
  visibility: ChartVisibility;
  low?: number[];
  high?: number[];
  colors: ChartColors;
  width: number;
  formatHour: (date: Date) => string;
  formatWeekday: (date: Date) => string;
  formatPrice: (value: number) => string;
  formatPower: (value: number) => string;
  labels: { power: string; price: string; soc: string };
}

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

// Polygons drawn by a custom series: each data item is one band, its extremes encoded so
// the axis range includes the whole band.
function bandSeries(
  name: string,
  bands: Band[],
  color: string,
  axis: { xAxisIndex: number; yAxisIndex: number }
) {
  return {
    id: name,
    type: "custom",
    silent: true,
    ...axis,
    z: 1,
    encode: { x: [0, 1], y: [2, 3] },
    data: bands.map((band) => {
      const ys = [...band.upper, ...band.lower].map(([, y]) => y);
      return [band.upper[0]![0], band.upper.at(-1)![0], Math.min(...ys), Math.max(...ys)];
    }),
    renderItem: (
      params: { dataIndex: number },
      api: { coord: (value: [number, number]) => [number, number] }
    ) => {
      const band = bands[params.dataIndex]!;
      return {
        type: "polygon",
        shape: { points: [...band.upper, ...[...band.lower].reverse()].map((p) => api.coord(p)) },
        style: { fill: color },
      };
    },
  };
}

function lineSeries(
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

// Label every n hours so labels never crowd: about one label per 56 px.
export function labelStepHours(width: number, horizonHours: number) {
  const wanted = horizonHours / Math.max(1, width / 56);
  return [1, 2, 3, 4, 6, 12, 24].find((step) => step >= wanted) ?? 24;
}

type Axis = { xAxisIndex: number; yAxisIndex: number };
const POWER: Axis = { xAxisIndex: 0, yAxisIndex: 0 };
const PRICE: Axis = { xAxisIndex: 0, yAxisIndex: 1 };
const SOC: Axis = { xAxisIndex: 1, yAxisIndex: 2 };

// Vertical shading for grid charging, drawn in every visible grid.
function gridSeries(input: ChartInput, showSoc: boolean) {
  const { price } = input.colors;
  const blocks = input.visibility.layers.grid ? gridChargeBlocks(input.slots) : [];
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
  return [shading(POWER, "grid-power"), ...(showSoc ? [shading(SOC, "grid-soc")] : [])];
}

// Solar and household demand: estimate line over its stepped low-high band.
function forecastSeries(input: ChartInput) {
  const { slots, colors, visibility } = input;
  return [
    ...(visibility.layers.solar
      ? [
          bandSeries(
            "solar-range",
            stepBands(slots, (s) => [kw(s.solarLowWh, s), kw(s.solarHighWh, s)]),
            alpha(colors.solar, 0.16),
            POWER
          ),
          lineSeries(
            "solar",
            stepPoints(slots, (s) => kw(s.solarWh, s)),
            colors.solar,
            POWER,
            {
              area: alpha(colors.solar, 0.28),
            }
          ),
        ]
      : []),
    ...(visibility.layers.home
      ? [
          bandSeries(
            "home-range",
            stepBands(slots, (s) => [kw(s.homeLowWh, s), kw(s.homeHighWh, s)]),
            alpha(colors.text, 0.1),
            POWER
          ),
          lineSeries(
            "home",
            stepPoints(slots, (s) => kw(s.homeWh, s)),
            colors.text,
            POWER,
            {
              width: 1.5,
            }
          ),
        ]
      : []),
  ];
}

// Opt-in per-device power: charging above zero, discharging below.
function powerSeries(input: ChartInput) {
  const { slots, colors, visibility } = input;
  return input.devices
    .filter((device) => visibility.power[device.key])
    .map((device) => {
      const points = new Map(device.plan.map((point) => [point.start, point]));
      const color = colors.devices[device.key] ?? colors.muted;
      const value = (slot: EnergyForecastSlot) => {
        const point = points.get(slot.start);
        return point ? kw(point.chargeWh - point.dischargeWh, slot) : undefined;
      };
      return lineSeries(`power-${device.key}`, stepPoints(slots, value), color, POWER, {
        width: 1.5,
        area: alpha(color, 0.35),
      });
    });
}

function priceSeries(input: ChartInput) {
  if (!input.visibility.layers.price) return [];
  const price = (slot: EnergyForecastSlot) => (finite(slot.gridPrice) ? slot.gridPrice : undefined);
  return [lineSeries("price", stepPoints(input.slots, price), input.colors.price, PRICE)];
}

// Charge levels at interval ends, with the battery's scenario ribbon underneath.
function socSeries(input: ChartInput, socDevices: EnergyDevice[]) {
  const { slots, devices, colors, visibility } = input;
  const color = (device: EnergyDevice) => colors.devices[device.key] ?? colors.muted;
  const ribbon = socRibbon(devices, slots, input.low, input.high);
  const battery = devices.find((device) => device.kind === "battery");
  return [
    ...(ribbon && battery && visibility.soc[battery.key]
      ? [bandSeries("soc-range", [ribbon], alpha(color(battery), 0.18), SOC)]
      : []),
    ...socDevices.flatMap((device) => {
      const timeline = socTimeline(device, slots).map(
        (point) => [ms(point.time), point.soc] as [number, number]
      );
      return timeline.length
        ? [
            lineSeries(`soc-${device.key}`, timeline, color(device), SOC, {
              dashed: device.kind === "expectedVehicle",
            }),
          ]
        : [];
    }),
  ];
}

// Hour labels on the lowest grid (weekday under midnight) and dashed day boundaries.
function timeAxes(input: ChartInput, showSoc: boolean, start: number, end: number) {
  const { muted } = input.colors;
  const step = labelStepHours(input.width, (end - start) / 3_600_000);
  const hourAxis = (gridIndex: number, labels: boolean) => ({
    type: "time",
    gridIndex,
    show: gridIndex === 0 || showSoc,
    min: start,
    max: end,
    minInterval: 3_600_000,
    maxInterval: 3_600_000,
    axisLine: { show: false },
    axisTick: { show: false },
    splitLine: { show: false },
    axisLabel: {
      show: labels,
      color: muted,
      fontSize: 12,
      lineHeight: 14,
      hideOverlap: false,
      formatter: (value: number) => {
        const date = new Date(value);
        const hour = date.getHours();
        if (date.getMinutes() || hour % step) return "";
        return hour === 0
          ? `${input.formatHour(date)}\n${input.formatWeekday(date)}`
          : input.formatHour(date);
      },
    },
  });
  const dayAxis = (gridIndex: number) => ({
    type: "time",
    gridIndex,
    min: start,
    max: end,
    minInterval: 86_400_000,
    maxInterval: 86_400_000,
    axisLabel: { show: false },
    axisLine: { show: false },
    axisTick: { show: false },
    splitLine: {
      show: true,
      showMinLine: false,
      showMaxLine: false,
      lineStyle: { color: muted, type: "dashed", opacity: 0.6 },
    },
  });
  return [
    hourAxis(0, !showSoc),
    hourAxis(1, showSoc),
    dayAxis(0),
    ...(showSoc ? [dayAxis(1)] : []),
  ];
}

// Power (left) and price (right, ticks aligned) share the upper grid; charge level below.
function valueAxes(input: ChartInput, showSoc: boolean) {
  const { muted, border } = input.colors;
  const axis = (gridIndex: number, overrides: Record<string, unknown>) => ({
    type: "value",
    gridIndex,
    axisLine: { show: false },
    axisTick: { show: false },
    nameTextStyle: { color: muted, fontSize: 11 },
    axisLabel: { color: muted, fontSize: 11 },
    splitLine: { lineStyle: { color: border } },
    ...overrides,
  });
  return [
    axis(0, {
      name: input.labels.power,
      nameGap: 10,
      splitNumber: 4,
      axisLabel: { color: muted, fontSize: 11, formatter: input.formatPower },
    }),
    axis(0, {
      name: input.labels.price,
      nameGap: 10,
      position: "right",
      alignTicks: true,
      show: input.visibility.layers.price,
      splitLine: { show: false },
      axisLabel: { color: muted, fontSize: 11, formatter: input.formatPrice },
    }),
    axis(showSoc ? 1 : 0, {
      name: input.labels.soc,
      nameGap: 8,
      show: showSoc,
      min: 0,
      max: 100,
      interval: 50,
    }),
  ];
}

const gridRect = (layout: ChartLayout, top: number, height: number) => ({
  left: layout.left,
  right: layout.right,
  top,
  height,
});

export function buildPlanChartOption(input: ChartInput) {
  const { slots, devices, visibility } = input;
  const socDevices = devices.filter((device) => hasSoc(device) && visibility.soc[device.key]);
  const showSoc = socDevices.length > 0;
  const layout = chartLayout(showSoc);
  const start = ms(slots[0]!.start);
  const end = ms(slots.at(-1)!.end);
  const power = gridRect(layout, layout.powerTop, layout.powerHeight);
  return {
    layout,
    option: {
      animation: false,
      textStyle: { fontFamily: "Montserrat, sans-serif" },
      // without charge levels the second grid overlays the first so axis indices stay stable
      grid: [power, showSoc ? gridRect(layout, layout.socTop, layout.socHeight) : power],
      xAxis: timeAxes(input, showSoc, start, end),
      yAxis: valueAxes(input, showSoc),
      series: [
        ...gridSeries(input, showSoc),
        ...forecastSeries(input),
        ...powerSeries(input),
        ...priceSeries(input),
        ...(showSoc ? socSeries(input, socDevices) : []),
      ],
    },
  };
}
