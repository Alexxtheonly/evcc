<template>
	<div class="energy-chart">
		<div class="d-flex flex-wrap gap-2 mb-2" :aria-label="$t('forecast.energy.chartLayers')">
			<label v-for="layer in layers" :key="layer" class="legend-toggle small">
				<input v-model="visible[layer]" type="checkbox" class="form-check-input mt-0" />
				<span :class="['legend-mark', layer]" aria-hidden="true" />
				{{ $t(`forecast.energy.chart.${layer}`) }}
			</label>
		</div>
		<div v-if="devices.length" class="d-flex flex-wrap gap-2 mb-2">
			<button
				v-for="device in devices"
				:key="device.key"
				type="button"
				class="btn btn-sm btn-outline-secondary device-toggle"
				:aria-pressed="!hiddenDevices.includes(device.key)"
				@click="toggleDevice(device.key)"
			>
				<span class="device-mark" :style="{ background: deviceColors[device.key] }" />
				{{ device.title }}
			</button>
		</div>
		<p class="small mb-3">{{ $t("forecast.energy.chartHint") }}</p>
		<div
			v-if="slots.length"
			class="timeline"
			tabindex="0"
			role="group"
			:aria-label="inspectionLabel"
			@pointermove="inspectPointer"
			@pointerdown="inspectPointer"
			@pointerleave="tooltipPosition = undefined"
			@keydown="inspectKey"
		>
			<template v-if="visible.price">
				<div class="small fw-semibold mb-1">{{ $t("forecast.energy.chart.price") }}</div>
				<div v-if="hasPrices" class="plot-row price-row">
					<div class="y-axis small" aria-hidden="true">
						<span>{{ priceLabel(priceMax) }}</span
						><span>{{ priceLabel(priceMin) }}</span>
					</div>
					<svg viewBox="0 0 665 80" preserveAspectRatio="none" aria-hidden="true">
						<line x1="0" x2="665" :y1="priceY(0)" :y2="priceY(0)" class="grid-line" />
						<polyline
							v-for="(points, index) in pricePoints"
							:key="index"
							:points="points"
							class="price-line"
						/>
						<rect v-bind="selection" y="0" height="80" class="selection" />
					</svg>
				</div>
				<p v-else class="small">{{ $t("forecast.energy.priceUnavailable") }}</p>
			</template>
			<template v-if="visible.soc && socDevices.length">
				<div class="small fw-semibold mt-3 mb-1">
					{{ $t("forecast.energy.chargeLevel") }}
				</div>
				<div class="plot-row soc-row">
					<div class="y-axis small" aria-hidden="true">
						<span>100 %</span><span>50 %</span><span>0 %</span>
					</div>
					<svg viewBox="0 0 665 120" preserveAspectRatio="none" aria-hidden="true">
						<line
							v-for="soc in [0, 50, 100]"
							:key="soc"
							x1="0"
							x2="665"
							:y1="socY(soc)"
							:y2="socY(soc)"
							class="grid-line"
						/>
						<polygon
							v-if="envelope"
							:points="envelope"
							:fill="envelopeColor"
							class="soc-envelope"
						/>
						<template v-for="device in socDevices" :key="device.key">
							<polyline
								v-if="socTimeline(device, slots).length"
								:points="socPoints(socTimeline(device, slots))"
								:stroke="deviceColors[device.key]"
								class="soc-line"
							/>
						</template>
						<rect v-bind="selection" y="0" height="120" class="selection" />
						<line :x1="selectedEnd" :x2="selectedEnd" y1="0" y2="120" class="cursor" />
					</svg>
				</div>
				<p v-if="envelope" class="small mb-0 mt-1">{{ $t("forecast.energy.envelope") }}</p>
			</template>
			<div v-if="visible.grid" class="mt-3">
				<div class="small mb-1">{{ $t("forecast.energy.gridChargingTotal") }}</div>
				<div v-if="gridSlots.length" class="plot-row activity-row grid-charge-row">
					<div class="small" aria-hidden="true">↑</div>
					<svg viewBox="0 0 665 24" preserveAspectRatio="none" aria-hidden="true">
						<rect width="665" height="24" class="activity-track" />
						<rect
							v-for="slot in gridSlots"
							:key="slot.start"
							:x="x(slot.start)"
							:width="x(slot.end) - x(slot.start)"
							y="2"
							height="20"
							:class="slot.gridChargeMinWh! > 1 ? 'grid' : 'grid-possible'"
						/>
						<rect v-bind="selection" y="0" height="24" class="selection" />
					</svg>
				</div>
				<p v-else class="small mb-0">
					{{
						$t(
							hasGridData
								? "forecast.energy.noGridCharging"
								: "forecast.energy.gridChargingUnavailable"
						)
					}}
				</p>
				<p v-if="gridSlots.length" class="small mt-1 mb-0">
					{{ $t("forecast.energy.gridChargingHint") }}
				</p>
			</div>
			<template v-if="visible.charge || visible.discharge">
				<div v-for="device in shownDevices" :key="device.key" class="mt-3">
					<div class="small mb-1">{{ device.title }}</div>
					<div class="plot-row activity-row">
						<div class="small" aria-hidden="true">↑ / ↓</div>
						<svg viewBox="0 0 665 24" preserveAspectRatio="none" aria-hidden="true">
							<rect width="665" height="24" class="activity-track" />
							<template v-for="point in activities(device)" :key="point.start">
								<rect
									v-if="visible.charge && point.chargeWh > 1"
									:x="point.x"
									:width="point.width"
									y="1"
									height="10"
									class="charge"
								/>
								<rect
									v-if="visible.discharge && point.dischargeWh > 1"
									:x="point.x"
									:width="point.width"
									y="13"
									height="10"
									class="discharge"
								/>
							</template>
							<rect v-bind="selection" y="0" height="24" class="selection" />
						</svg>
					</div>
				</div>
			</template>
			<div class="x-axis small" aria-hidden="true">
				<span v-for="time in ticks" :key="time">{{ dateTime(time) }}</span>
			</div>
		</div>
		<div
			v-if="tooltipPosition"
			ref="tooltip"
			class="chart-tooltip small border rounded shadow p-2"
			:style="tooltipPosition"
			aria-hidden="true"
		>
			<slot name="inspection" :devices="shownDevices" />
		</div>
	</div>
</template>
<script lang="ts">
import { defineComponent, type PropType } from "vue";
import { resolveColors } from "@/colors";
import formatter from "@/mixins/formatter";
import { CURRENCY } from "@/types/evcc";
import { is12hFormat } from "@/units";
import { socTimeline, type EnergyDevice, type EnergyForecastSlot } from "./energyIntelligence";

const layers = ["price", "soc", "charge", "discharge", "grid"] as const;

export default defineComponent({
	mixins: [formatter],
	props: {
		devices: { type: Array as PropType<EnergyDevice[]>, required: true },
		slots: { type: Array as PropType<EnergyForecastSlot[]>, required: true },
		low: { type: Array as PropType<number[]>, default: undefined },
		high: { type: Array as PropType<number[]>, default: undefined },
		selectedIndex: { type: Number, default: 0 },
		currency: { type: String as PropType<CURRENCY>, default: CURRENCY.EUR },
	},
	emits: ["inspect"],
	data: () => ({
		layers,
		visible: { price: true, soc: true, charge: true, discharge: true, grid: true },
		hiddenDevices: [] as string[],
		tooltipPosition: undefined as { left: string; top: string; width: string } | undefined,
	}),
	computed: {
		shownDevices() {
			return this.devices.filter((device) => !this.hiddenDevices.includes(device.key));
		},
		socDevices() {
			return this.shownDevices.filter((device) => device.capacityKWh > 0);
		},
		hasGridData() {
			return this.slots.every(
				(slot) => slot.gridChargeMinWh != null && slot.gridChargeMaxWh != null
			);
		},
		gridSlots() {
			return this.slots.filter(
				(slot) =>
					slot.gridChargeMinWh != null &&
					slot.gridChargeMaxWh != null &&
					slot.gridChargeMaxWh > 1
			);
		},
		deviceColors() {
			return resolveColors(this.devices.map((device) => device.key));
		},
		envelopeColor() {
			const device = this.devices.find((device) => device.kind === "battery");
			return device ? this.deviceColors[device.key] : undefined;
		},
		start() {
			return new Date(this.slots[0]?.start ?? 0).getTime();
		},
		end() {
			return new Date(this.slots.at(-1)?.end ?? 0).getTime();
		},
		ticks() {
			return [this.start, (this.start + this.end) / 2, this.end];
		},
		selectedSlot() {
			return this.slots[this.selectedIndex];
		},
		selectedEnd() {
			return this.x(this.selectedSlot?.end ?? this.slots[0]!.start);
		},
		selection() {
			const slot = this.selectedSlot;
			return slot
				? { x: this.x(slot.start), width: this.x(slot.end) - this.x(slot.start) }
				: { x: 0, width: 0 };
		},
		inspectionLabel() {
			const slot = this.selectedSlot;
			return `${this.$t("forecast.energy.chartHint")} ${slot ? `${this.dateTime(new Date(slot.start).getTime())} – ${this.dateTime(new Date(slot.end).getTime())}` : ""}`;
		},
		priceMin() {
			return Math.min(0, ...this.slots.map((slot) => slot.gridPrice).filter(Number.isFinite));
		},
		hasPrices() {
			return this.slots.some((slot) => Number.isFinite(slot.gridPrice));
		},
		priceMax() {
			return Math.max(0, ...this.slots.map((slot) => slot.gridPrice).filter(Number.isFinite));
		},
		pricePoints() {
			const lines: string[] = [];
			let previousEnd: string | undefined;
			for (const slot of this.slots) {
				if (!Number.isFinite(slot.gridPrice)) {
					previousEnd = undefined;
					continue;
				}
				const points = `${this.x(slot.start)},${this.priceY(slot.gridPrice)} ${this.x(slot.end)},${this.priceY(slot.gridPrice)}`;
				if (previousEnd === slot.start) lines[lines.length - 1] += ` ${points}`;
				else lines.push(points);
				previousEnd = slot.end;
			}
			return lines;
		},
		envelope() {
			const device = this.devices.find((device) => device.kind === "battery");
			if (
				!device ||
				!this.socDevices.includes(device) ||
				this.low?.length !== this.slots.length ||
				this.high?.length !== this.slots.length ||
				![...this.low, ...this.high].every(Number.isFinite)
			)
				return undefined;
			const atEnds = (values: number[]) => [
				...(device.initialSoc != null && Number.isFinite(device.initialSoc)
					? [{ time: this.slots[0]!.start, soc: device.initialSoc }]
					: []),
				...values.map((soc, index) => ({ time: this.slots[index]!.end, soc })),
			];
			return `${this.socPoints(atEnds(this.low))} ${this.socPoints(atEnds(this.high).reverse())}`;
		},
	},
	methods: {
		socTimeline,
		toggleDevice(key: string) {
			this.hiddenDevices = this.hiddenDevices.includes(key)
				? this.hiddenDevices.filter((item) => item !== key)
				: [...this.hiddenDevices, key];
		},
		x(time: string) {
			return (
				((new Date(time).getTime() - this.start) / Math.max(1, this.end - this.start)) * 665
			);
		},
		socY(soc: number) {
			return 120 - soc * 1.2;
		},
		socPoints(values: { time: string; soc: number }[]) {
			return values.map((point) => `${this.x(point.time)},${this.socY(point.soc)}`).join(" ");
		},
		priceY(price: number) {
			return 75 - ((price - this.priceMin) / (this.priceMax - this.priceMin || 1)) * 70;
		},
		priceLabel(price: number) {
			return this.fmtPricePerKWh(price, this.currency);
		},
		activities(device: EnergyDevice) {
			return device.plan.flatMap((point) => {
				const slot = this.slots.find((slot) => slot.start === point.start);
				return slot
					? [
							{
								...point,
								x: this.x(slot.start),
								width: this.x(slot.end) - this.x(slot.start),
							},
						]
					: [];
			});
		},
		inspectPointer(event: PointerEvent) {
			const svg = event.target instanceof Element ? event.target.closest("svg") : null;
			if (!svg) return;
			const bounds = svg.getBoundingClientRect();
			if (!bounds.width) return;
			const time =
				this.start +
				Math.max(0, Math.min(1, (event.clientX - bounds.left) / bounds.width)) *
					(this.end - this.start);
			const index = this.slots.findIndex(
				(slot) =>
					time >= new Date(slot.start).getTime() && time < new Date(slot.end).getTime()
			);
			if (index >= 0) this.$emit("inspect", index);
			else if (time === this.end) this.$emit("inspect", this.slots.length - 1);
			else {
				this.tooltipPosition = undefined;
				return;
			}
			const width = Math.min(340, window.innerWidth - 16);
			const height =
				(this.$refs["tooltip"] as HTMLElement | undefined)?.getBoundingClientRect()
					.height ?? 220;
			this.tooltipPosition = {
				left: `${Math.max(8, Math.min(window.innerWidth - width - 8, event.clientX + 16))}px`,
				top: `${Math.max(8, Math.min(window.innerHeight - height - 8, event.clientY - height - 16))}px`,
				width: `${width}px`,
			};
		},
		inspectKey(event: KeyboardEvent) {
			if (event.key === "Escape") this.tooltipPosition = undefined;
			const next = {
				ArrowLeft: this.selectedIndex - 1,
				ArrowRight: this.selectedIndex + 1,
				Home: 0,
				End: this.slots.length - 1,
			}[event.key];
			if (next == null) return;
			this.tooltipPosition = undefined;
			event.preventDefault();
			this.$emit("inspect", Math.max(0, Math.min(this.slots.length - 1, next)));
		},
		dateTime(time: number) {
			return new Date(time).toLocaleString(this.$i18n.locale, {
				weekday: "short",
				hour: "2-digit",
				minute: "2-digit",
				hour12: is12hFormat(),
			});
		},
	},
});
</script>
<style scoped>
.legend-toggle {
	display: inline-flex;
	align-items: center;
	gap: 0.4rem;
	cursor: pointer;
	padding: 0.35rem 0.5rem;
	border: 1px solid var(--bs-border-color);
	border-radius: 0.4rem;
}
.legend-mark,
.device-mark {
	display: inline-block;
	width: 0.8rem;
	height: 0.6rem;
	flex-shrink: 0;
}
.device-toggle {
	text-align: left;
	color: var(--evcc-default-text);
}
.device-toggle[aria-pressed="false"] {
	opacity: 0.55;
	text-decoration: line-through;
}
.timeline {
	border-radius: 0.25rem;
}
.timeline:focus-visible {
	outline: 2px solid var(--bs-primary);
	outline-offset: 4px;
}
.plot-row {
	display: grid;
	grid-template-columns: 4.5rem minmax(0, 1fr);
}
svg {
	display: block;
	width: 100%;
	min-width: 0;
	cursor: crosshair;
	touch-action: pan-y;
}
.price-row svg,
.price-row .y-axis {
	height: 80px;
}
.soc-row svg,
.soc-row .y-axis {
	height: 120px;
}
.activity-row svg {
	height: 24px;
}
.y-axis {
	display: flex;
	flex-direction: column;
	justify-content: space-between;
	line-height: 1.1;
	padding-right: 0.4rem;
}
.x-axis {
	margin: 0.7rem 0 0 4.5rem;
	display: flex;
	justify-content: space-between;
	gap: 0.5rem;
}
.x-axis span {
	max-width: 33%;
}
.x-axis span:nth-child(2) {
	text-align: center;
}
.x-axis span:last-child {
	text-align: right;
}
.grid-line {
	stroke: var(--bs-border-color);
}
.soc-line,
.price-line {
	fill: none;
	stroke-width: 2.5;
	vector-effect: non-scaling-stroke;
}
.price-line {
	stroke: var(--evcc-price);
}
.soc-envelope {
	opacity: 0.12;
}
.selection {
	fill: var(--evcc-default-text);
	fill-opacity: 0.09;
	stroke: var(--evcc-default-text);
	stroke-opacity: 0.25;
	vector-effect: non-scaling-stroke;
}
.cursor {
	stroke: var(--evcc-default-text);
	stroke-dasharray: 4;
	vector-effect: non-scaling-stroke;
}
.activity-track {
	fill: var(--bs-tertiary-bg);
}
.charge {
	fill: var(--evcc-battery);
	background: var(--evcc-battery);
}
.discharge {
	fill: var(--evcc-grid);
	background: var(--evcc-grid);
}
.price {
	background: var(--evcc-price);
}
.soc {
	background: var(--bs-secondary-color);
}
.grid {
	fill: var(--evcc-orange);
	background: var(--evcc-orange);
}
.grid-possible {
	fill: var(--evcc-orange);
	fill-opacity: 0.15;
	stroke: var(--evcc-orange);
	stroke-dasharray: 3 2;
	vector-effect: non-scaling-stroke;
}
.charge,
.discharge {
	shape-rendering: crispEdges;
}
.chart-tooltip {
	position: fixed;
	z-index: 1050;
	background: var(--evcc-box);
	color: var(--evcc-default-text);
	pointer-events: none;
	max-height: calc(100vh - 16px);
	overflow: hidden;
}
</style>
