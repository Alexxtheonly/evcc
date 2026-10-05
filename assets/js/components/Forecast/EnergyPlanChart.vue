<template>
	<div class="energy-chart">
		<div class="legend mb-2">
			<div
				class="legend-group"
				role="group"
				:aria-label="$t('forecast.energy.plan.layersForecast')"
			>
				<span class="legend-label" aria-hidden="true">{{
					$t("forecast.energy.plan.layersForecast")
				}}</span>
				<button
					v-for="layer in forecastLayers"
					:key="layer"
					type="button"
					class="legend-toggle"
					:aria-pressed="visibility.layers[layer]"
					:data-testid="`layer-${layer}`"
					@click="visibility.layers[layer] = !visibility.layers[layer]"
				>
					<span :class="['legend-mark', `mark-${layer}`]" aria-hidden="true" />
					{{ $t(`forecast.energy.plan.layer.${layer}`) }}
				</button>
			</div>
			<div
				v-if="socDevices.length"
				class="legend-group"
				role="group"
				:aria-label="$t('forecast.energy.chargeLevel')"
			>
				<span class="legend-label" aria-hidden="true">{{
					$t("forecast.energy.chargeLevel")
				}}</span>
				<button
					v-for="device in socDevices"
					:key="device.key"
					type="button"
					class="legend-toggle"
					:aria-pressed="!!visibility.soc[device.key]"
					:aria-label="`${$t('forecast.energy.chargeLevel')}: ${device.title}`"
					:data-testid="`soc-${device.key}`"
					@click="visibility.soc[device.key] = !visibility.soc[device.key]"
				>
					<span
						:class="[
							'legend-mark',
							'mark-line',
							{ dashed: device.kind === 'expectedVehicle' },
						]"
						:style="{ color: deviceColors[device.key] }"
						aria-hidden="true"
					/>
					{{ device.title }}
				</button>
			</div>
			<div v-if="devices.length" class="legend-group">
				<button
					type="button"
					class="btn btn-link btn-sm p-0 more-layers"
					:aria-expanded="moreLayers"
					:aria-controls="`${uid}-power`"
					data-testid="more-layers"
					@click="moreLayers = !moreLayers"
				>
					{{ $t("forecast.energy.plan.moreLayers") }}
					<span aria-hidden="true">{{ moreLayers ? "▴" : "▾" }}</span>
				</button>
			</div>
			<div
				v-if="devices.length"
				v-show="moreLayers"
				:id="`${uid}-power`"
				class="legend-group"
				role="group"
				:aria-label="$t('forecast.energy.plan.layersPower')"
			>
				<span class="legend-label" aria-hidden="true">{{
					$t("forecast.energy.plan.layersPower")
				}}</span>
				<button
					v-for="device in devices"
					:key="device.key"
					type="button"
					class="legend-toggle"
					:aria-pressed="!!visibility.power[device.key]"
					:aria-label="`${$t('forecast.energy.plan.layersPower')}: ${device.title}`"
					:data-testid="`power-${device.key}`"
					@click="visibility.power[device.key] = !visibility.power[device.key]"
				>
					<span
						class="legend-mark mark-area"
						:style="{ color: deviceColors[device.key] }"
						aria-hidden="true"
					/>
					{{ device.title }}
				</button>
			</div>
		</div>
		<div
			ref="wrap"
			class="chart-wrap"
			tabindex="0"
			role="group"
			:aria-label="$t('forecast.energy.plan.chartLabel')"
			:aria-describedby="`${uid}-live`"
			@pointerdown="onPointerDown"
			@pointermove="onPointerMove"
			@pointerup="onPointerUp"
			@pointerleave="onPointerLeave"
			@pointercancel="hideTooltip"
			@keydown="onKey"
		>
			<div ref="chartEl" class="chart-canvas" :style="{ height: `${layout.height}px` }" />
			<div
				v-if="crosshair"
				class="crosshair"
				aria-hidden="true"
				:style="{
					transform: `translateX(${crosshair.left}px)`,
					width: `${crosshair.width}px`,
					top: `${layout.powerTop}px`,
					height: `${crosshairHeight}px`,
				}"
			/>
		</div>
		<p class="small text-muted mt-1 mb-0">{{ $t("forecast.energy.plan.chartHint") }}</p>
		<span :id="`${uid}-live`" class="visually-hidden" aria-live="polite">{{
			tooltip.mode === "keyboard" ? liveText : ""
		}}</span>
		<div
			v-if="details"
			ref="tooltip"
			class="plan-tooltip shadow"
			:class="{ interactive: tooltip.mode === 'touch' }"
			:style="tooltipStyle"
			aria-hidden="true"
			data-testid="plan-tooltip"
		>
			<div class="fw-semibold mb-1">{{ interval(details.start, details.end) }}</div>
			<table class="tooltip-table">
				<tbody>
					<tr v-if="details.price != null">
						<th>
							<span class="legend-mark mark-price" />{{
								$t("forecast.energy.plan.layer.price")
							}}
						</th>
						<td>{{ fmtPricePerKWh(details.price, currency) }}</td>
					</tr>
					<tr v-if="details.home">
						<th>
							<span class="legend-mark mark-home" />{{
								$t("forecast.energy.plan.layer.home")
							}}
						</th>
						<td>
							{{ power(details.home.value) }}
							<small class="d-block">{{ range(details.home) }}</small>
						</td>
					</tr>
					<tr v-if="details.solar">
						<th>
							<span class="legend-mark mark-solar" />{{
								$t("forecast.energy.plan.layer.solar")
							}}
						</th>
						<td>
							{{ power(details.solar.value) }}
							<small class="d-block">{{ range(details.solar) }}</small>
						</td>
					</tr>
					<tr v-if="details.grid">
						<th>
							<span class="legend-mark mark-grid" />{{
								$t("forecast.energy.plan.layer.grid")
							}}
						</th>
						<td>{{ gridText(details.grid) }}</td>
					</tr>
					<tr v-for="device in details.devices" :key="device.key">
						<th>
							<span
								class="legend-mark mark-line"
								:style="{ color: deviceColors[device.key] }"
							/><span class="device-name" :title="device.title">{{
								device.title
							}}</span>
						</th>
						<td>
							<template v-if="device.soc != null">
								{{ percent(device.soc) }}
								<small class="d-block">{{ deviceState(device) }}</small>
							</template>
							<template v-else>{{ deviceState(device) }}</template>
						</td>
					</tr>
				</tbody>
			</table>
		</div>
	</div>
</template>
<script lang="ts">
import { defineComponent, markRaw, type PropType } from "vue";
import colors from "@/colors";
import formatter from "@/mixins/formatter";
import { CURRENCY, type DeviceColors } from "@/types/evcc";
import { is12hFormat } from "@/units";
import { echarts } from "./echarts";
import { CustomChart } from "echarts/charts";
import type { EnergyDevice, EnergyForecastSlot } from "./energyIntelligence";
import {
	FORECAST_LAYERS,
	chartLayout,
	defaultVisibility,
	deviceColorAssignment,
	hasSoc,
	placeTooltip,
	slotDetails,
	slotIndexAt,
	timeAtX,
	xAtTime,
	type ChartVisibility,
	type DeviceSlotDetail,
	type GridChargeState,
	type PointerMode,
} from "./planChartModel";
import { buildPlanChartOption } from "./planChartOption";

echarts.use([CustomChart]);

let uidCounter = 0;

export default defineComponent({
	mixins: [formatter],
	props: {
		devices: { type: Array as PropType<EnergyDevice[]>, required: true },
		slots: { type: Array as PropType<EnergyForecastSlot[]>, required: true },
		low: { type: Array as PropType<number[]>, default: undefined },
		high: { type: Array as PropType<number[]>, default: undefined },
		currency: { type: String as PropType<CURRENCY>, default: CURRENCY.EUR },
		deviceColorOverrides: { type: Object as PropType<DeviceColors>, default: () => ({}) },
	},
	data() {
		return {
			uid: `energy-plan-${++uidCounter}`,
			forecastLayers: FORECAST_LAYERS,
			visibility: defaultVisibility(this.devices) as ChartVisibility,
			width: 0,
			chart: null as echarts.ECharts | null,
			observer: null as ResizeObserver | null,
			frame: 0,
			pending: undefined as { x: number; y: number; mode: PointerMode } | undefined,
			touchActive: false,
			moreLayers: false,
			tooltip: {
				index: -1,
				anchor: { x: 0, y: 0 },
				mode: "mouse" as PointerMode,
			},
			position: undefined as { left: number; top: number; maxHeight: number } | undefined,
		};
	},
	computed: {
		socDevices() {
			return this.devices.filter(hasSoc);
		},
		deviceColors(): Record<string, string> {
			return deviceColorAssignment(this.devices, this.deviceColorOverrides, colors.palette);
		},
		start() {
			return Date.parse(this.slots[0]?.start ?? "");
		},
		end() {
			return Date.parse(this.slots.at(-1)?.end ?? "");
		},
		built() {
			if (!this.slots.length) return undefined;
			return buildPlanChartOption({
				slots: this.slots,
				devices: this.devices,
				visibility: this.visibility,
				low: this.low,
				high: this.high,
				width: this.width || 600,
				colors: {
					text: colors.text || "#28293e",
					muted: colors.muted || "#93949e",
					border: colors.border || "rgba(0,0,0,0.1)",
					price: colors.price || "#ff912f",
					solar: colors.self || "#0fde41",
					devices: this.deviceColors,
				},
				formatHour: (date) => this.hourShort(date),
				formatWeekday: (date) => this.weekdayShort(date),
				formatPrice: (value) => this.axisPrice(value),
				formatPower: (value) => value.toLocaleString(this.$i18n.locale),
				labels: {
					power: "kW",
					price: this.pricePerKWhUnit(this.currency),
					soc: "%",
				},
			});
		},
		layout() {
			return this.built?.layout ?? chartLayout(false);
		},
		crosshairHeight() {
			const layout = this.layout;
			return (
				(layout.socHeight
					? layout.socTop + layout.socHeight
					: layout.powerTop + layout.powerHeight) - layout.powerTop
			);
		},
		details() {
			return this.tooltip.index >= 0
				? slotDetails(this.tooltip.index, this.slots, this.devices, this.visibility)
				: undefined;
		},
		crosshair() {
			const slot = this.slots[this.tooltip.index];
			if (!slot || !this.width) return undefined;
			const left = xAtTime(
				Date.parse(slot.start),
				this.width,
				this.layout,
				this.start,
				this.end
			);
			const right = xAtTime(
				Date.parse(slot.end),
				this.width,
				this.layout,
				this.start,
				this.end
			);
			return { left, width: Math.max(2, right - left) };
		},
		tooltipStyle() {
			if (!this.position) return { left: "0px", top: "0px", visibility: "hidden" as const };
			return {
				left: `${this.position.left}px`,
				top: `${this.position.top}px`,
				maxHeight: `${this.position.maxHeight}px`,
			};
		},
		liveText() {
			const d = this.details;
			if (!d) return "";
			return [
				this.interval(d.start, d.end),
				d.price != null ? this.fmtPricePerKWh(d.price, this.currency) : "",
				...d.devices.map(
					(device) =>
						`${device.title}: ${device.soc != null ? `${this.percent(device.soc)}, ` : ""}${this.deviceState(device)}`
				),
			]
				.filter(Boolean)
				.join(". ");
		},
	},
	watch: {
		built: {
			handler() {
				this.$nextTick(() => this.render());
			},
			deep: false,
		},
		devices(devices: EnergyDevice[]) {
			const defaults = defaultVisibility(devices);
			this.visibility.soc = { ...defaults.soc, ...this.visibility.soc };
			this.visibility.power = { ...defaults.power, ...this.visibility.power };
		},
		slots() {
			if (this.tooltip.index >= this.slots.length) this.hideTooltip();
		},
		details() {
			if (this.details) this.$nextTick(() => this.place());
		},
	},
	mounted() {
		if (typeof ResizeObserver !== "undefined") {
			this.observer = new ResizeObserver(() => this.measure());
			this.observer.observe(this.$refs["wrap"] as HTMLElement);
		}
		this.measure();
		this.$nextTick(() => this.render());
		document.addEventListener("pointerdown", this.onDocumentPointerDown, true);
		document.addEventListener("scroll", this.onDocumentScroll, true);
	},
	beforeUnmount() {
		this.observer?.disconnect();
		cancelAnimationFrame(this.frame);
		document.removeEventListener("pointerdown", this.onDocumentPointerDown, true);
		document.removeEventListener("scroll", this.onDocumentScroll, true);
		this.chart?.dispose();
		this.chart = null;
	},
	methods: {
		measure() {
			const wrap = this.$refs["wrap"] as HTMLElement | undefined;
			const width = wrap?.clientWidth ?? 0;
			if (width !== this.width) {
				this.width = width;
				this.hideTooltip();
			}
			this.chart?.resize();
		},
		render() {
			const el = this.$refs["chartEl"] as HTMLElement | undefined;
			if (!el || !this.built) return;
			if (!this.chart || this.chart.getDom() !== el) {
				this.chart?.dispose();
				this.chart = markRaw(echarts.init(el, undefined, { renderer: "svg" }));
			}
			this.chart.resize();
			this.chart.setOption(this.built.option, { notMerge: true });
		},
		timeFormat(value: string, weekday: boolean) {
			return new Date(value).toLocaleString(this.$i18n.locale, {
				weekday: weekday ? "short" : undefined,
				hour: "2-digit",
				minute: "2-digit",
				hour12: is12hFormat(),
			});
		},
		interval(start: string, end: string) {
			return `${this.timeFormat(start, true)} – ${this.timeFormat(end, false)}`;
		},
		// Axis ticks are round numbers in the display unit (ct for subunit currencies).
		axisPrice(value: number) {
			const factor = this.pricePerKWhDisplayFactor(this.currency);
			return (value * factor).toLocaleString(this.$i18n.locale, {
				maximumFractionDigits: factor > 1 ? 0 : 2,
			});
		},
		number(value: number, digits = 1) {
			return value.toLocaleString(this.$i18n.locale, {
				minimumFractionDigits: digits,
				maximumFractionDigits: digits,
			});
		},
		power(value: number | undefined) {
			return value == null
				? this.$t("forecast.energy.plan.unavailable")
				: `${this.number(value)} kW`;
		},
		range(value: { low?: number; high?: number }) {
			if (value.low == null || value.high == null)
				return this.$t("forecast.energy.plan.rangeUnavailable");
			return this.$t("forecast.energy.plan.range", {
				low: this.number(value.low),
				high: this.number(value.high),
			});
		},
		percent(value: number) {
			return `${this.number(value, 0)} %`;
		},
		kwh(wh: number) {
			return `${this.number(wh / 1000, 2)} kWh`;
		},
		gridText(state: GridChargeState) {
			if (state.kind === "unavailable") return this.$t("forecast.energy.plan.unavailable");
			if (state.kind === "none") return this.$t("forecast.energy.plan.gridNone");
			if (state.kind === "definite")
				return this.$t("forecast.energy.plan.gridDefinite", {
					low: this.kwh(state.minWh),
					high: this.kwh(state.maxWh),
				});
			return this.$t("forecast.energy.plan.gridPossible", { high: this.kwh(state.maxWh) });
		},
		deviceState(device: DeviceSlotDetail) {
			if (device.state === "charging" || device.state === "discharging")
				return this.$t(`forecast.energy.plan.state.${device.state}`, {
					power: this.power(device.powerKW),
				});
			return this.$t(`forecast.energy.${device.state}`);
		},
		show(x: number, y: number, mode: PointerMode) {
			const wrap = this.$refs["wrap"] as HTMLElement | undefined;
			if (!wrap || !this.slots.length) return;
			const bounds = wrap.getBoundingClientRect();
			const index = slotIndexAt(
				this.slots,
				timeAtX(x - bounds.left, bounds.width, this.layout, this.start, this.end)
			);
			if (index < 0) return this.hideTooltip();
			if (this.tooltip.index < 0) this.position = undefined;
			this.tooltip = { index, anchor: { x, y }, mode };
			this.$nextTick(() => this.place());
		},
		// Coalesce pointer events to one update per frame so the popup follows smoothly.
		schedule(x: number, y: number, mode: PointerMode) {
			this.pending = { x, y, mode };
			if (this.frame) return;
			this.frame = requestAnimationFrame(() => {
				this.frame = 0;
				const pending = this.pending;
				this.pending = undefined;
				if (pending) this.show(pending.x, pending.y, pending.mode);
			});
		},
		place() {
			const el = this.$refs["tooltip"] as HTMLElement | undefined;
			if (!el) return;
			const rect = el.getBoundingClientRect();
			this.position = placeTooltip(
				this.tooltip.anchor,
				{ width: rect.width, height: el.scrollHeight || rect.height },
				{ width: window.innerWidth, height: window.innerHeight },
				this.tooltip.mode
			);
		},
		hideTooltip() {
			this.tooltip = { ...this.tooltip, index: -1 };
			this.position = undefined;
			this.touchActive = false;
		},
		onPointerDown(event: PointerEvent) {
			if (event.pointerType === "mouse") return;
			this.touchActive = true;
			this.show(event.clientX, event.clientY, "touch");
		},
		onPointerMove(event: PointerEvent) {
			if (event.pointerType === "mouse") this.schedule(event.clientX, event.clientY, "mouse");
			else if (this.touchActive) this.schedule(event.clientX, event.clientY, "touch");
		},
		// A lifted finger keeps the details open; outside taps, scrolling or Escape close them.
		onPointerUp(event: PointerEvent) {
			if (event.pointerType !== "mouse") this.touchActive = false;
		},
		onPointerLeave(event: PointerEvent) {
			if (event.pointerType === "mouse") this.hideTooltip();
		},
		onDocumentPointerDown(event: PointerEvent) {
			const target = event.target as Node | null;
			const wrap = this.$refs["wrap"] as HTMLElement | undefined;
			const tooltip = this.$refs["tooltip"] as HTMLElement | undefined;
			if (target && (wrap?.contains(target) || tooltip?.contains(target))) return;
			if (this.tooltip.index >= 0) this.hideTooltip();
		},
		onDocumentScroll(event: Event) {
			const tooltip = this.$refs["tooltip"] as HTMLElement | undefined;
			if (tooltip && event.target instanceof Node && tooltip.contains(event.target)) return;
			if (this.tooltip.index >= 0) this.hideTooltip();
		},
		onKey(event: KeyboardEvent) {
			if (event.key === "Escape") return this.hideTooltip();
			const current = this.tooltip.index;
			const next = {
				ArrowLeft: current < 0 ? 0 : current - 1,
				ArrowRight: current + 1,
				Home: 0,
				End: this.slots.length - 1,
			}[event.key];
			if (next == null) return;
			event.preventDefault();
			const index = Math.max(0, Math.min(this.slots.length - 1, next));
			const wrap = this.$refs["wrap"] as HTMLElement;
			const bounds = wrap.getBoundingClientRect();
			const slot = this.slots[index]!;
			const mid = (Date.parse(slot.start) + Date.parse(slot.end)) / 2;
			const x = bounds.left + xAtTime(mid, bounds.width, this.layout, this.start, this.end);
			if (this.tooltip.index < 0) this.position = undefined;
			this.tooltip = {
				index,
				anchor: { x, y: bounds.top + this.layout.powerTop },
				mode: "keyboard",
			};
		},
	},
});
</script>
<style scoped>
.legend {
	display: flex;
	flex-direction: column;
	gap: 0.4rem;
}
.legend-group {
	display: flex;
	flex-wrap: wrap;
	align-items: center;
	gap: 0.35rem;
}
.legend-label {
	font-size: 0.8rem;
	color: var(--bs-secondary-color);
	margin-right: 0.15rem;
}
.legend-toggle {
	display: inline-flex;
	align-items: center;
	gap: 0.4rem;
	padding: 0.2rem 0.55rem;
	font-size: 0.8125rem;
	line-height: 1.3;
	color: var(--evcc-default-text);
	background: transparent;
	border: 1px solid var(--bs-border-color);
	border-radius: 1rem;
	cursor: pointer;
}
.legend-toggle[aria-pressed="true"] {
	background: var(--bs-tertiary-bg);
	border-color: var(--bs-secondary-color);
}
.legend-toggle[aria-pressed="false"] {
	color: var(--bs-secondary-color);
}
.legend-toggle[aria-pressed="false"] .legend-mark {
	opacity: 0.35;
}
.legend-toggle:focus-visible,
.chart-wrap:focus-visible {
	outline: 2px solid var(--bs-primary);
	outline-offset: 2px;
}
.legend-mark {
	display: inline-block;
	flex-shrink: 0;
	width: 0.9rem;
	height: 0.6rem;
	border-radius: 2px;
	vertical-align: middle;
}
.mark-price {
	height: 0;
	border-top: 2px solid var(--evcc-price);
	border-radius: 0;
}
.mark-home {
	height: 0.6rem;
	background: color-mix(in srgb, var(--evcc-default-text) 14%, transparent);
	border-top: 2px solid var(--evcc-default-text);
	border-radius: 0;
}
.mark-solar {
	background: color-mix(in srgb, var(--evcc-self) 35%, transparent);
	border-top: 2px solid var(--evcc-self);
	border-radius: 0;
}
.mark-grid {
	background: color-mix(in srgb, var(--evcc-price) 25%, transparent);
	border: 1px dashed var(--evcc-price);
}
.mark-line {
	height: 0;
	border-top: 3px solid currentColor;
	border-radius: 0;
}
.mark-line.dashed {
	border-top-style: dashed;
}
.mark-area {
	background: currentColor;
	opacity: 0.7;
}
.chart-wrap {
	position: relative;
	touch-action: pan-y;
	user-select: none;
	-webkit-user-select: none;
	-webkit-touch-callout: none;
	cursor: crosshair;
	border-radius: 0.25rem;
}
.chart-canvas {
	width: 100%;
}
.crosshair {
	position: absolute;
	left: 0;
	pointer-events: none;
	background: color-mix(in srgb, var(--evcc-default-text) 10%, transparent);
	border-left: 1px solid color-mix(in srgb, var(--evcc-default-text) 55%, transparent);
	border-right: 1px solid color-mix(in srgb, var(--evcc-default-text) 55%, transparent);
	will-change: transform;
}
.plan-tooltip {
	position: fixed;
	z-index: 1050;
	min-width: 12rem;
	max-width: min(20rem, calc(100vw - 16px));
	overflow: auto;
	padding: 0.5rem 0.65rem;
	font-size: 0.8125rem;
	line-height: 1.3;
	color: var(--evcc-default-text);
	background: var(--evcc-box);
	border: 1px solid var(--bs-border-color);
	border-radius: 0.5rem;
	pointer-events: none;
}
.plan-tooltip.interactive {
	pointer-events: auto;
	overscroll-behavior: contain;
}
.tooltip-table {
	width: 100%;
	border-collapse: collapse;
}
.tooltip-table th {
	font-weight: normal;
	white-space: nowrap;
	padding: 0.15rem 0.75rem 0.15rem 0;
	vertical-align: top;
}
.tooltip-table th .legend-mark {
	margin-right: 0.35rem;
}
.tooltip-table td {
	text-align: right;
	padding: 0.15rem 0;
	white-space: nowrap;
	font-variant-numeric: tabular-nums;
	vertical-align: top;
}
.device-name {
	display: inline-block;
	max-width: min(10rem, calc(100vw - 15rem));
	overflow: hidden;
	text-overflow: ellipsis;
	white-space: nowrap;
	vertical-align: bottom;
}
.more-layers {
	font-size: 0.8125rem;
	color: var(--bs-secondary-color);
	text-decoration: none;
}
.tooltip-table small {
	color: var(--bs-secondary-color);
}
</style>
