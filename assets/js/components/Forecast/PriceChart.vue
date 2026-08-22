<template>
	<div ref="scrollEl" class="forecast-chart-scroll scroll-overlay-fix" @scroll="onScroll">
		<div ref="chartEl" :style="{ height: '200px', width: chartWidth + 'px' }"></div>
	</div>
	<LegendList v-if="legends.length" :legends="legends" class="mt-2" />
</template>

<script lang="ts">
import { defineComponent, type PropType } from "vue";
import {
	echarts,
	FONT_FAMILY,
	markPointLabel,
	tooltipStyle,
	tooltipTable,
	forecastGrid,
	forecastXAxes,
	forecastYAxis,
	clampStart,
	filterForecastSlots,
	minSlotIndex,
	maxSlotIndex,
} from "./echarts";
import colors, { batteryColor, dimColor, lighterColor, resolveColors } from "@/colors";
import formatter from "@/mixins/formatter";
import chartMixin from "./chartMixin";
import LegendList from "../Sessions/LegendList.vue";
import type { Legend } from "../Sessions/types";
import {
	batteryChargeWindows,
	batteryDischargeWindows,
	vehicleChargeWindows,
	adaptivePlanMarkers,
	type TimeWindow,
} from "./optimizerOverlay";
import { forecastToSeries } from "../Battery/history";
import type {
	CURRENCY,
	DeviceColors,
	EvOpt,
	OptimizerDecision,
	UiForecastSlot,
	Vehicle,
} from "@/types/evcc";
import { BATTERY_MODE, OPTIMIZER_VETO_REASON } from "@/types/evcc";

export default defineComponent({
	name: "PriceChart",
	components: { LegendList },
	mixins: [formatter, chartMixin],
	props: {
		grid: { type: Array as PropType<UiForecastSlot[]>, required: true },
		feedin: { type: Array as PropType<UiForecastSlot[]> },
		currency: { type: String as PropType<CURRENCY> },
		zoom: { type: Boolean, default: false },
		evopt: { type: Object as PropType<EvOpt> },
		vehicles: { type: Array as PropType<Vehicle[]>, default: () => [] },
		deviceColors: { type: Object as PropType<DeviceColors>, default: () => ({}) },
		optimizerDecision: { type: Object as PropType<OptimizerDecision> },
	},
	computed: {
		slots(): UiForecastSlot[] {
			return filterForecastSlots(this.grid, this.startDate, this.endDate);
		},
		feedinSlots(): UiForecastSlot[] {
			return this.feedin
				? filterForecastSlots(this.feedin, this.startDate, this.endDate)
				: [];
		},
		markPoints(): { coord: [number, number]; value: string }[] {
			const slots = this.slots;
			if (!slots.length) return [];
			const minIdx = minSlotIndex(slots);
			const maxIdx = maxSlotIndex(slots);
			const points: { coord: [number, number]; value: string }[] = [];
			if (slots[minIdx]) {
				points.push({
					coord: [clampStart(slots[minIdx]!.start, this.startDate), slots[minIdx]!.value],
					value: this.fmtPricePerKWh(slots[minIdx]!.value, this.currency, true, true),
				});
			}
			if (maxIdx !== minIdx && slots[maxIdx]) {
				points.push({
					coord: [clampStart(slots[maxIdx]!.start, this.startDate), slots[maxIdx]!.value],
					value: this.fmtPricePerKWh(slots[maxIdx]!.value, this.currency, true, true),
				});
			}
			return points;
		},
		yAxisConfig(): Record<string, unknown> {
			const values = [
				...this.slots.map((s) => s.value),
				...this.feedinSlots.map((s) => s.value),
			];
			const dataMin = Math.min(...values);
			const dataMax = Math.max(...values);
			const rangeMin = this.zoom ? dataMin : Math.min(0, dataMin);
			const rangeMax = Math.max(0, dataMax);
			const range = rangeMax - rangeMin || 1;
			const rawInterval = range / 5;
			const magnitude = Math.pow(10, Math.floor(Math.log10(rawInterval)));
			const nice = [1, 2, 2.5, 5, 10].find((n) => n * magnitude >= rawInterval) || 10;
			const interval = nice * magnitude;

			return {
				min: Math.floor(rangeMin / interval) * interval,
				max: Math.ceil(rangeMax / interval) * interval,
				interval,
			};
		},

		// --- optimizer schedule overlay (Phase 2.1) ---

		batteryChargeWindows(): TimeWindow[] {
			return batteryChargeWindows(this.evopt);
		},
		batteryDischargeWindows(): TimeWindow[] {
			return batteryDischargeWindows(this.evopt);
		},
		vehicleWindows() {
			return vehicleChargeWindows(this.evopt);
		},
		vehicleColors(): DeviceColors {
			const keys = this.vehicleWindows.map((w) => w.key);
			return resolveColors(keys, this.deviceColors);
		},
		overlayMarkArea(): Record<string, unknown> | undefined {
			const regions: { start: number; end: number; color: string }[] = [];
			// drop windows that ended before the visible range - clamping only the
			// start (and not dropping these) can hand echarts a region with
			// end < start when the optimizer stalls longer than one slot
			const isFuture = (w: TimeWindow) => w.end > this.startDate.getTime();
			const clamp = (w: TimeWindow) => ({
				start: clampStart(w.start, this.startDate),
				end: clampStart(w.end, this.startDate),
			});

			this.batteryChargeWindows
				.filter(isFuture)
				.forEach((w) => regions.push({ ...clamp(w), color: dimColor(colors.grid) || "" }));
			this.batteryDischargeWindows
				.filter(isFuture)
				.forEach((w) =>
					regions.push({ ...clamp(w), color: dimColor(batteryColor(0)) || "" })
				);
			this.vehicleWindows.forEach(({ key, windows }) =>
				windows.filter(isFuture).forEach((w) =>
					regions.push({
						...clamp(w),
						color: dimColor(this.vehicleColors[key] || null) || "",
					})
				)
			);

			if (!regions.length) return undefined;

			return {
				silent: true,
				data: regions.map((r) => [
					{ xAxis: r.start, itemStyle: { color: r.color } },
					{ xAxis: r.end },
				]),
			};
		},

		// --- battery SoC trajectory (Phase 2.2) ---

		batteryDetails() {
			return this.evopt?.details?.batteryDetails || [];
		},
		batteryDeviceColors(): Record<string, string> {
			const colorsByName: Record<string, string> = {};
			let index = 0;
			this.batteryDetails.forEach((d) => {
				if (d.type === "battery") colorsByName[d.name] = batteryColor(index++);
			});
			return colorsByName;
		},
		socSeries() {
			return forecastToSeries(this.evopt, Date.now());
		},
		hasSocSeries(): boolean {
			return this.socSeries.some((s) => s.points.length > 0);
		},

		// --- adaptive plan markers (Phase 2.3) ---

		planMarkers() {
			return adaptivePlanMarkers(
				this.vehicles,
				this.startDate.getTime(),
				this.endDate.getTime()
			);
		},

		// --- slot-0 decision annotation (Phase 2.4) ---

		optimizerDecisionLabel(): string | undefined {
			const d = this.optimizerDecision;
			if (!d) return undefined;

			const modeKeys: Record<string, string> = {
				[BATTERY_MODE.NORMAL]: "forecast.optimizer.modeNormal",
				[BATTERY_MODE.HOLD]: "forecast.optimizer.modeHold",
				[BATTERY_MODE.HOLDCHARGE]: "forecast.optimizer.modeHoldcharge",
				[BATTERY_MODE.CHARGE]: "forecast.optimizer.modeCharge",
				[BATTERY_MODE.UNKNOWN]: "forecast.optimizer.modeUnknown",
			};
			const reasonKeys: Record<string, string> = {
				[OPTIMIZER_VETO_REASON.PAYBACK]: "forecast.optimizer.reasonPayback",
				[OPTIMIZER_VETO_REASON.FORCED_IDLE]: "forecast.optimizer.reasonForcedIdle",
				[OPTIMIZER_VETO_REASON.DAMPING]: "forecast.optimizer.reasonDamping",
				[OPTIMIZER_VETO_REASON.LIVE_RATE]: "forecast.optimizer.reasonLiveRate",
			};

			const modeKey = modeKeys[d.mode];
			if (!modeKey) return undefined;

			let label = this.$t(modeKey) as string;
			const reasonKey = d.vetoReason && reasonKeys[d.vetoReason];
			if (reasonKey) {
				label += ` (${this.$t(reasonKey)})`;
			}
			return label;
		},

		markLineData(): Record<string, unknown>[] {
			const data: Record<string, unknown>[] = [];

			if (this.optimizerDecisionLabel) {
				data.push({
					xAxis: Date.now(),
					lineStyle: { color: colors.muted, type: "dashed", width: 1 },
					label: {
						formatter: this.optimizerDecisionLabel,
						position: "insideEndTop",
						color: colors.text,
						fontFamily: FONT_FAMILY,
						fontSize: 11,
					},
				});
			}

			this.planMarkers.forEach((marker) => {
				const color = this.vehicleColors[marker.key] || colors.muted || "";
				data.push({
					xAxis: marker.time,
					lineStyle: { color, type: "dotted", width: 1 },
					label: {
						formatter: this.$t("forecast.optimizer.planReadyBy", {
							vehicle: marker.title,
							soc: this.fmtPercentage(marker.soc),
						}),
						position: "insideEndTop",
						color,
						fontFamily: FONT_FAMILY,
						fontSize: 11,
					},
				});
			});

			return data;
		},

		legends(): Legend[] {
			// area swatches mirror overlayMarkArea, which shades at dimColor(...);
			// a full-opacity swatch would not match what is actually drawn
			const legends: Legend[] = [];
			if (this.batteryChargeWindows.length) {
				legends.push({
					label: this.$t("forecast.optimizer.legendBatteryCharge"),
					color: dimColor(colors.grid) || "",
					value: "",
					type: "area",
				});
			}
			if (this.batteryDischargeWindows.length) {
				legends.push({
					label: this.$t("forecast.optimizer.legendBatteryDischarge"),
					color: dimColor(batteryColor(0)) || "",
					value: "",
					type: "area",
				});
			}
			this.vehicleWindows.forEach(({ key, title }) => {
				legends.push({
					label: title,
					color: dimColor(this.vehicleColors[key] || null) || "",
					value: "",
					type: "area",
				});
			});
			legends.push(...this.socLegends);
			return legends;
		},

		// one legend entry per drawn SoC line, colored to match batteryDeviceColors -
		// a single generic entry would misrepresent N differently-colored lines
		socLegends(): Legend[] {
			return this.socSeries
				.filter((s) => s.points.length > 0)
				.map((s) => {
					const detail = this.batteryDetails.find(
						(d) => d.type === "battery" && d.name === s.key
					);
					const title = detail?.title || detail?.name || s.key;
					return {
						label: this.$t("forecast.optimizer.legendBatterySoc", { title }),
						color: this.batteryDeviceColors[s.key] || colors.text || "",
						value: "",
						type: "line",
					};
				});
		},

		chartOption(): Record<string, unknown> {
			const priceColor = colors.price || "";
			const exportColor = colors.export || "";

			// oxlint-disable-next-line typescript/no-this-alias
			const vThis = this;

			const priceLine = this.priceSeries(this.slots, priceColor, this.markPoints);
			if (this.overlayMarkArea) {
				priceLine["markArea"] = this.overlayMarkArea;
			}
			if (this.markLineData.length) {
				priceLine["markLine"] = {
					silent: true,
					symbol: "none",
					data: this.markLineData,
				};
			}

			const series: Record<string, unknown>[] = [
				priceLine,
				this.priceSeries(this.feedinSlots, exportColor),
			];

			if (this.hasSocSeries) {
				this.socSeries.forEach((s) => {
					series.push({
						name: s.key,
						type: "line",
						yAxisIndex: 1,
						step: "start",
						showSymbol: false,
						silent: true,
						lineStyle: {
							color: this.batteryDeviceColors[s.key] || colors.text,
							width: 2,
						},
						itemStyle: { color: this.batteryDeviceColors[s.key] || colors.text },
						data: s.points.map((p) => ({ value: [p.t, p.soc] })),
					});
				});
			}

			return {
				animationDuration: 0,
				animationDurationUpdate: 300,
				textStyle: { fontFamily: FONT_FAMILY },
				grid: { ...forecastGrid(), right: this.hasSocSeries ? 40 : 16 },
				tooltip: {
					trigger: "axis",
					axisPointer: { type: "line", snap: true, lineStyle: { color: "transparent" } },
					...tooltipStyle(priceColor, () => this.chart),
					formatter(params: { value: [string, number]; seriesIndex: number }[]) {
						const p = params[0];
						if (!p) return "";
						const d = new Date(p.value[0]);
						const time = `${vThis.weekdayShort(d)} ${vThis.fmtHourMinute(d)}`;
						const showLabels = params.length > 1;
						const labels = [
							vThis.$t("main.energyflow.gridImport"),
							vThis.$t("main.energyflow.pvExport"),
						];
						const rows = params
							.filter((s) => s.seriesIndex < 2)
							.map((s) => ({
								name: showLabels ? labels[s.seriesIndex] : undefined,
								values: [
									vThis.fmtPricePerKWh(s.value[1], vThis.currency, true, true),
								],
							}));
						return tooltipTable(time, rows);
					},
				},
				xAxis: forecastXAxes(
					this.startDate,
					this.endDate,
					this.hourShort,
					this.weekdayShort
				),
				yAxis: this.hasSocSeries
					? [
							forecastYAxis({
								...this.yAxisConfig,
								axisLabel: {
									color: colors.muted,
									formatter: (value: number) => {
										const v =
											this.currency && this.energyPriceSubunit(this.currency)
												? value * 100
												: value;
										return `${Math.round(v)}`;
									},
								},
							}),
							forecastYAxis({
								min: 0,
								max: 100,
								position: "right",
								splitLine: { show: false },
								axisLabel: {
									color: colors.muted,
									formatter: (value: number) => `${Math.round(value)}%`,
								},
							}),
						]
					: forecastYAxis({
							...this.yAxisConfig,
							axisLabel: {
								color: colors.muted,
								formatter: (value: number) => {
									const v =
										this.currency && this.energyPriceSubunit(this.currency)
											? value * 100
											: value;
									return `${Math.round(v)}`;
								},
							},
						}),
				series,
			};
		},
	},
	methods: {
		priceSeries(
			slots: UiForecastSlot[],
			color: string,
			points?: { coord: [number, number]; value: string }[]
		): Record<string, unknown> {
			const avg = slots.length ? slots.reduce((a, s) => a + s.value, 0) / slots.length : 0;
			const gradientDown = avg >= 0;
			return {
				type: "line",
				step: "start",
				cursor: "default",
				showSymbol: false,
				data: slots.map((s) => ({
					value: [clampStart(s.start, this.startDate), s.value],
				})),
				lineStyle: { color, width: 2 },
				areaStyle: {
					color: new echarts.graphic.LinearGradient(
						0,
						gradientDown ? 0 : 1,
						0,
						gradientDown ? 1 : 0,
						[
							{ offset: 0, color: lighterColor(color) || color },
							{ offset: 0.75, color: color + "00" },
							{ offset: 1, color: color + "00" },
						]
					),
				},
				itemStyle: { color },
				emphasis: { disabled: true },
				...(points
					? {
							markPoint: markPointLabel(
								color,
								this.tooltipVisible ? [] : points,
								this.startDate,
								this.endDate
							),
						}
					: {}),
			};
		},
	},
});
</script>
