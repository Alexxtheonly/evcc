import { socTimeline, type EnergyDevice, type EnergyForecastSlot } from "./energyIntelligence";

export const FORECAST_LAYERS = ["price", "home", "solar", "grid"] as const;
export type ForecastLayer = (typeof FORECAST_LAYERS)[number];

export interface ChartVisibility {
  layers: Record<ForecastLayer, boolean>;
  soc: Record<string, boolean>;
  power: Record<string, boolean>;
}

// Uncluttered default: forecasts, price, grid-charge shading and every charge level.
// Per-device power is implied by the charge-level slope and stays opt-in.
export function defaultVisibility(devices: EnergyDevice[]): ChartVisibility {
  return {
    layers: { price: true, home: true, solar: true, grid: true },
    soc: Object.fromEntries(devices.map((device) => [device.key, device.capacityKWh > 0])),
    power: Object.fromEntries(devices.map((device) => [device.key, false])),
  };
}

export const hasSoc = (device: EnergyDevice) => device.capacityKWh > 0;

export interface ChartLayout {
  height: number;
  left: number;
  right: number;
  powerTop: number;
  powerHeight: number;
  socTop: number;
  socHeight: number;
}

export function chartLayout(showSoc: boolean): ChartLayout {
  const powerTop = 28;
  const powerHeight = 190;
  const socTop = powerTop + powerHeight + 26;
  const socHeight = showSoc ? 90 : 0;
  return {
    left: 40,
    right: 48,
    powerTop,
    powerHeight,
    socTop,
    socHeight,
    height: (showSoc ? socTop + socHeight : powerTop + powerHeight) + 44,
  };
}

const finite = (value: number | undefined | null): value is number =>
  value != null && Number.isFinite(value);

const ms = (time: string) => Date.parse(time);

const hours = (slot: EnergyForecastSlot) => (ms(slot.end) - ms(slot.start)) / 3_600_000;

// Average power over the slot; slot lengths differ (partial first slot, coarse steps).
export const kw = (wh: number | undefined, slot: EnergyForecastSlot) =>
  finite(wh) && hours(slot) > 0 ? wh / 1000 / hours(slot) : undefined;

// Index of the slot containing `time`; the final edge belongs to the last slot.
export function slotIndexAt(slots: EnergyForecastSlot[], time: number) {
  const index = slots.findIndex((slot) => time >= ms(slot.start) && time < ms(slot.end));
  if (index >= 0) return index;
  return slots.length && time === ms(slots.at(-1)!.end) ? slots.length - 1 : -1;
}

export function timeAtX(x: number, width: number, layout: ChartLayout, start: number, end: number) {
  const plot = Math.max(1, width - layout.left - layout.right);
  const ratio = Math.max(0, Math.min(1, (x - layout.left) / plot));
  return start + ratio * (end - start);
}

export function xAtTime(
  time: number,
  width: number,
  layout: ChartLayout,
  start: number,
  end: number
) {
  const plot = Math.max(1, width - layout.left - layout.right);
  return layout.left + ((time - start) / Math.max(1, end - start)) * plot;
}

export type PointerMode = "mouse" | "touch" | "keyboard";

// Keeps the popup inside the viewport using its rendered size. Mouse: beside the pointer,
// flipping left near the right edge. Touch: above the finger (which hides what is below),
// below only when there is no room above. Oversized popups are capped and scroll.
export function placeTooltip(
  anchor: { x: number; y: number },
  size: { width: number; height: number },
  viewport: { width: number; height: number },
  mode: PointerMode,
  margin = 8,
  gap = 16
) {
  const maxHeight = viewport.height - 2 * margin;
  const width = Math.min(size.width, viewport.width - 2 * margin);
  const height = Math.min(size.height, maxHeight);
  const clampX = (left: number) =>
    Math.max(margin, Math.min(viewport.width - width - margin, left));
  const clampY = (top: number) =>
    Math.max(margin, Math.min(viewport.height - height - margin, top));
  if (mode === "mouse") {
    const right = anchor.x + gap;
    const left = right + width + margin <= viewport.width ? right : anchor.x - gap - width;
    return { left: clampX(left), top: clampY(anchor.y - height / 2), maxHeight };
  }
  const above = anchor.y - gap - height;
  const top = above >= margin ? above : anchor.y + gap;
  return { left: clampX(anchor.x - width / 2), top: clampY(top), maxHeight };
}

type Point = [number, number | null];

// Two points per slot so values hold over their interval; missing values break the line.
export function stepPoints(
  slots: EnergyForecastSlot[],
  value: (slot: EnergyForecastSlot, index: number) => number | undefined
): Point[] {
  const points: Point[] = [];
  slots.forEach((slot, index) => {
    const v = value(slot, index);
    if (!finite(v)) {
      if (points.length && points.at(-1)![1] !== null) points.push([ms(slot.start), null]);
      return;
    }
    points.push([ms(slot.start), v], [ms(slot.end), v]);
  });
  return points;
}

export interface Band {
  upper: [number, number][];
  lower: [number, number][];
}

// Stepped ranges; a slot without a finite range splits the band instead of claiming zero.
export function stepBands(
  slots: EnergyForecastSlot[],
  range: (slot: EnergyForecastSlot) => [number | undefined, number | undefined]
): Band[] {
  const bands: Band[] = [];
  let current: Band | undefined;
  for (const slot of slots) {
    const [low, high] = range(slot);
    if (!finite(low) || !finite(high)) {
      current = undefined;
      continue;
    }
    if (!current) bands.push((current = { upper: [], lower: [] }));
    current.upper.push([ms(slot.start), high], [ms(slot.end), high]);
    current.lower.push([ms(slot.start), low], [ms(slot.end), low]);
  }
  return bands;
}

// Scenario range for the single stationary battery; aggregate multi-battery bounds are not
// assigned to one device. Values are interval-end charge levels like the plan itself.
export function socRibbon(
  devices: EnergyDevice[],
  slots: EnergyForecastSlot[],
  low: number[] | undefined,
  high: number[] | undefined
): Band | undefined {
  const batteries = devices.filter((device) => device.kind === "battery");
  const device = batteries.length === 1 ? batteries[0] : undefined;
  if (
    !device ||
    !hasSoc(device) ||
    low?.length !== slots.length ||
    high?.length !== slots.length ||
    ![...low, ...high].every(Number.isFinite)
  )
    return undefined;
  const start = finite(device.initialSoc)
    ? [[ms(slots[0]!.start), device.initialSoc] as [number, number]]
    : [];
  return {
    lower: [...start, ...low.map((soc, index) => [ms(slots[index]!.end), soc] as [number, number])],
    upper: [
      ...start,
      ...high.map((soc, index) => [ms(slots[index]!.end), soc] as [number, number]),
    ],
  };
}

export interface GridBlock {
  start: number;
  end: number;
  definite: boolean;
}

// Site-level grid charging: definite where the lower allocation bound is positive,
// possible where only the upper bound is. Contiguous slots of one kind merge.
export function gridChargeBlocks(slots: EnergyForecastSlot[]): GridBlock[] {
  const blocks: GridBlock[] = [];
  for (const slot of slots) {
    if (!finite(slot.gridChargeMinWh) || !finite(slot.gridChargeMaxWh) || slot.gridChargeMaxWh <= 1)
      continue;
    const definite = slot.gridChargeMinWh > 1;
    const previous = blocks.at(-1);
    if (previous && previous.end === ms(slot.start) && previous.definite === definite)
      previous.end = ms(slot.end);
    else blocks.push({ start: ms(slot.start), end: ms(slot.end), definite });
  }
  return blocks;
}

export type GridChargeState =
  | { kind: "unavailable" }
  | { kind: "none" }
  | { kind: "definite" | "possible"; minWh: number; maxWh: number };

export function gridChargeState(slot: EnergyForecastSlot): GridChargeState {
  if (!finite(slot.gridChargeMinWh) || !finite(slot.gridChargeMaxWh))
    return { kind: "unavailable" };
  if (slot.gridChargeMaxWh <= 1) return { kind: "none" };
  return {
    kind: slot.gridChargeMinWh > 1 ? "definite" : "possible",
    minWh: slot.gridChargeMinWh,
    maxWh: slot.gridChargeMaxWh,
  };
}

export interface DeviceSlotDetail {
  key: string;
  title: string;
  soc?: number;
  state: "charging" | "discharging" | "idle" | "away" | "noPlan";
  powerKW?: number;
}

export interface SlotDetails {
  start: string;
  end: string;
  price?: number;
  home?: { value?: number; low?: number; high?: number };
  solar?: { value?: number; low?: number; high?: number };
  grid?: GridChargeState;
  devices: DeviceSlotDetail[];
}

// Everything the crosshair popup shows for one slot, limited to visible layers.
export function slotDetails(
  index: number,
  slots: EnergyForecastSlot[],
  devices: EnergyDevice[],
  visibility: ChartVisibility
): SlotDetails | undefined {
  const slot = slots[index];
  if (!slot) return undefined;
  const { layers } = visibility;
  const time = ms(slot.start);
  return {
    start: slot.start,
    end: slot.end,
    price: layers.price && finite(slot.gridPrice) ? slot.gridPrice : undefined,
    home: layers.home
      ? {
          value: kw(slot.homeWh, slot),
          low: kw(slot.homeLowWh, slot),
          high: kw(slot.homeHighWh, slot),
        }
      : undefined,
    solar: layers.solar
      ? {
          value: kw(slot.solarWh, slot),
          low: kw(slot.solarLowWh, slot),
          high: kw(slot.solarHighWh, slot),
        }
      : undefined,
    grid: layers.grid ? gridChargeState(slot) : undefined,
    devices: devices
      .filter((device) => visibility.soc[device.key] || visibility.power[device.key])
      .map((device) => {
        const point = device.plan.find((plan) => plan.start === slot.start);
        const detail = { key: device.key, title: device.title };
        if (!point) return { ...detail, state: "noPlan" as const };
        if (
          (device.arrival && time < ms(device.arrival)) ||
          (device.departure && time >= ms(device.departure))
        )
          return { ...detail, state: "away" as const };
        const soc = hasSoc(device) && finite(point.soc) ? point.soc : undefined;
        if (point.chargeWh > 1)
          return { ...detail, soc, state: "charging" as const, powerKW: kw(point.chargeWh, slot) };
        if (point.dischargeWh > 1)
          return {
            ...detail,
            soc,
            state: "discharging" as const,
            powerKW: kw(point.dischargeWh, slot),
          };
        return { ...detail, soc, state: "idle" as const };
      }),
  };
}

export interface ChartColors {
  text: string;
  muted: string;
  border: string;
  price: string;
  solar: string;
  grid: string;
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

export function buildPlanChartOption(input: ChartInput) {
  const { slots, devices, visibility, colors } = input;
  const { layers } = visibility;
  const socDevices = devices.filter((device) => hasSoc(device) && visibility.soc[device.key]);
  const showSoc = socDevices.length > 0;
  const layout = chartLayout(showSoc);
  const start = ms(slots[0]!.start);
  const end = ms(slots.at(-1)!.end);
  const step = labelStepHours(input.width, (end - start) / 3_600_000);
  const power = { xAxisIndex: 0, yAxisIndex: 0 };
  const price = { xAxisIndex: 0, yAxisIndex: 1 };
  const soc = { xAxisIndex: 1, yAxisIndex: 2 };
  const series: Record<string, unknown>[] = [];

  const blocks = layers.grid ? gridChargeBlocks(slots) : [];
  const markArea = (axis: { xAxisIndex: number; yAxisIndex: number }, id: string) => ({
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
            ? { color: alpha(colors.price, 0.22) }
            : {
                color: alpha(colors.price, 0.07),
                borderColor: alpha(colors.price, 0.6),
                borderWidth: 1,
                borderType: "dashed",
              },
        },
        { xAxis: block.end },
      ]),
    },
  });
  if (blocks.length) {
    series.push(markArea(power, "grid-power"));
    if (showSoc) series.push(markArea(soc, "grid-soc"));
  }

  if (layers.solar) {
    series.push(
      bandSeries(
        "solar-range",
        stepBands(slots, (s) => [kw(s.solarLowWh, s), kw(s.solarHighWh, s)]),
        alpha(colors.solar, 0.16),
        power
      ),
      lineSeries(
        "solar",
        stepPoints(slots, (s) => kw(s.solarWh, s)),
        colors.solar,
        power,
        {
          area: alpha(colors.solar, 0.28),
        }
      )
    );
  }
  if (layers.home) {
    series.push(
      bandSeries(
        "home-range",
        stepBands(slots, (s) => [kw(s.homeLowWh, s), kw(s.homeHighWh, s)]),
        alpha(colors.text, 0.1),
        power
      ),
      lineSeries(
        "home",
        stepPoints(slots, (s) => kw(s.homeWh, s)),
        colors.text,
        power,
        { width: 1.5 }
      )
    );
  }
  for (const device of devices.filter((d) => visibility.power[d.key])) {
    const points = new Map(device.plan.map((point) => [point.start, point]));
    const color = colors.devices[device.key] ?? colors.muted;
    series.push(
      lineSeries(
        `power-${device.key}`,
        stepPoints(slots, (s) => {
          const point = points.get(s.start);
          return point ? kw(point.chargeWh - point.dischargeWh, s) : undefined;
        }),
        color,
        power,
        { width: 1.5, area: alpha(color, 0.35) }
      )
    );
  }
  if (layers.price) {
    series.push(
      lineSeries(
        "price",
        stepPoints(slots, (s) => (finite(s.gridPrice) ? s.gridPrice : undefined)),
        colors.price,
        price,
        { width: 2 }
      )
    );
  }
  if (showSoc) {
    const ribbon = socRibbon(devices, slots, input.low, input.high);
    const ribbonDevice = devices.find((device) => device.kind === "battery");
    if (ribbon && ribbonDevice && visibility.soc[ribbonDevice.key]) {
      series.push(
        bandSeries(
          "soc-range",
          [ribbon],
          alpha(colors.devices[ribbonDevice.key] ?? colors.muted, 0.18),
          soc
        )
      );
    }
    for (const device of socDevices) {
      const timeline = socTimeline(device, slots).map(
        (point) => [ms(point.time), point.soc] as [number, number]
      );
      if (timeline.length)
        series.push(
          lineSeries(
            `soc-${device.key}`,
            timeline,
            colors.devices[device.key] ?? colors.muted,
            soc,
            {
              width: 2,
              dashed: device.kind === "expectedVehicle",
            }
          )
        );
    }
  }

  const xAxis = (gridIndex: number, labels: boolean) => [
    {
      type: "time",
      gridIndex,
      min: start,
      max: end,
      minInterval: 3_600_000,
      maxInterval: 3_600_000,
      axisLine: { show: false },
      axisTick: { show: false },
      splitLine: { show: false },
      axisLabel: {
        show: labels,
        color: colors.muted,
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
    },
  ];
  // Day boundaries: dashed lines at local midnight across every grid.
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
      lineStyle: { color: colors.muted, type: "dashed", opacity: 0.6 },
    },
  });
  const valueAxis = (gridIndex: number, overrides: Record<string, unknown>) => ({
    type: "value",
    gridIndex,
    axisLine: { show: false },
    axisTick: { show: false },
    nameTextStyle: { color: colors.muted, fontSize: 11 },
    axisLabel: { color: colors.muted, fontSize: 11 },
    splitLine: { lineStyle: { color: colors.border } },
    ...overrides,
  });

  return {
    layout,
    option: {
      animation: false,
      textStyle: { fontFamily: "Montserrat, sans-serif" },
      grid: [
        {
          left: layout.left,
          right: layout.right,
          top: layout.powerTop,
          height: layout.powerHeight,
        },
        ...(showSoc
          ? [
              {
                left: layout.left,
                right: layout.right,
                top: layout.socTop,
                height: layout.socHeight,
              },
            ]
          : [
              {
                left: layout.left,
                right: layout.right,
                top: layout.powerTop,
                height: layout.powerHeight,
              },
            ]),
      ],
      xAxis: [
        ...xAxis(0, !showSoc),
        ...xAxis(1, showSoc).map((axis) => ({ ...axis, show: showSoc })),
        dayAxis(0),
        ...(showSoc ? [dayAxis(1)] : []),
      ],
      yAxis: [
        valueAxis(0, {
          name: input.labels.power,
          nameGap: 10,
          splitNumber: 4,
          axisLabel: { color: colors.muted, fontSize: 11, formatter: (v: number) => `${v}` },
        }),
        valueAxis(0, {
          name: input.labels.price,
          nameGap: 10,
          position: "right",
          alignTicks: true,
          show: layers.price,
          splitLine: { show: false },
          axisLabel: { color: colors.muted, fontSize: 11, formatter: input.formatPrice },
        }),
        valueAxis(showSoc ? 1 : 0, {
          name: input.labels.soc,
          nameGap: 8,
          show: showSoc,
          min: 0,
          max: 100,
          interval: 50,
        }),
      ],
      series,
    },
  };
}
