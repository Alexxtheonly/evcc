import type { EnergyDevice } from "./energyIntelligence";
import { kw, stepPoints } from "./planChartModel";
import type { ChartInput } from "./planChartOption";
import { POWER, SOC, alpha, gridShading, lineSeries, type Axis } from "./planChartSeries";
import { pastDevicePower, pastGridBlocks, pastSocPoints } from "./planHistoryModel";

// Measured past: a tinted region ending at a "now" line, solid measured lines in the
// plan's colours (no estimate bands), measured grid charging and charge levels.
export function pastSeries(input: ChartInput, showSoc: boolean, socDevices: EnergyDevice[]) {
  const past = input.past;
  if (!past) return [];
  const { colors, visibility } = input;
  const region = (axis: Axis, id: string) => ({
    id,
    type: "line",
    data: [],
    silent: true,
    z: 0,
    ...axis,
    markArea: {
      silent: true,
      itemStyle: { color: alpha(colors.text, 0.05) },
      data: [[{ xAxis: past.start }, { xAxis: past.end }]],
    },
    markLine: {
      silent: true,
      symbol: "none",
      label: { show: false },
      lineStyle: { color: colors.muted, type: "solid", width: 1 },
      data: [{ xAxis: past.end }],
    },
  });
  const color = (device: EnergyDevice) => colors.devices[device.key] ?? colors.muted;
  const measured = past.slots;
  return [
    region(POWER, "past-region-power"),
    ...(showSoc ? [region(SOC, "past-region-soc")] : []),
    ...gridShading(
      input,
      showSoc,
      visibility.layers.grid ? pastGridBlocks(measured) : [],
      "past-grid"
    ),
    ...(visibility.layers.solar
      ? [
          lineSeries(
            "past-solar",
            stepPoints(measured, (s) => kw(s.solarWh, s)),
            colors.solar,
            POWER,
            { area: alpha(colors.solar, 0.28) }
          ),
        ]
      : []),
    ...(visibility.layers.home
      ? [
          lineSeries(
            "past-home",
            stepPoints(measured, (s) => kw(s.homeWh, s)),
            colors.text,
            POWER,
            { width: 1.5 }
          ),
        ]
      : []),
    ...input.devices
      .filter((device) => visibility.power[device.key] && past.points[device.key])
      .map((device) => {
        const battery = device.kind === "battery";
        return lineSeries(
          `past-power-${device.key}`,
          stepPoints(measured, (s) => pastDevicePower(past, device.key, s)),
          color(device),
          POWER,
          { width: battery ? 2 : 1.5, area: battery ? undefined : alpha(color(device), 0.35) }
        );
      }),
    ...(showSoc
      ? socDevices.flatMap((device) => {
          const points = pastSocPoints(past, device.key, device.initialSoc);
          return points.length
            ? [lineSeries(`past-soc-${device.key}`, points, color(device), SOC)]
            : [];
        })
      : []),
  ];
}
