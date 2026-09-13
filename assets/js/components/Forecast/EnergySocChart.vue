<template>
	<div class="energy-soc">
		<div class="chart-area">
			<div class="y-axis small" aria-hidden="true">
				<span>100 %</span><span>50 %</span><span>0 %</span>
			</div>
			<svg
				viewBox="0 0 665 135"
				preserveAspectRatio="none"
				role="img"
				:aria-label="$t('forecast.energy.socChart', { name: device.title })"
			>
				<title>{{ device.title }} · {{ $t("forecast.energy.chargeLevel") }}</title>
				<g v-for="soc in [0, 50, 100]" :key="soc">
					<line x1="0" x2="665" :y1="y(soc)" :y2="y(soc)" class="grid-line" />
				</g>
				<polygon v-if="envelope" :points="envelope" class="soc-envelope" />
				<polyline v-if="timeline.length" :points="points(timeline)" class="soc-line" />
				<line
					v-if="selectedEnd"
					:x1="x(selectedEnd)"
					:x2="x(selectedEnd)"
					y1="0"
					y2="135"
					class="soc-cursor"
				/>
			</svg>
		</div>
		<div class="x-axis small" aria-hidden="true">
			<span v-for="time in ticks" :key="time">{{ dateTime(time) }}</span>
		</div>
		<div class="small d-flex flex-wrap gap-3">
			<span><span class="legend-line" />{{ $t("forecast.energy.plannedLevel") }}</span>
			<span v-if="envelope"
				><span class="legend-band" />{{ $t("forecast.energy.envelope") }}</span
			>
		</div>
	</div>
</template>
<script lang="ts">
import { defineComponent, type PropType } from "vue";
import { is12hFormat } from "@/units";
import { socTimeline, type EnergyDevice, type EnergyForecastSlot } from "./energyIntelligence";

export default defineComponent({
	props: {
		device: { type: Object as PropType<EnergyDevice>, required: true },
		slots: { type: Array as PropType<EnergyForecastSlot[]>, required: true },
		low: { type: Array as PropType<number[]>, default: undefined },
		high: { type: Array as PropType<number[]>, default: undefined },
		selectedEnd: String,
	},
	computed: {
		timeline() {
			return socTimeline(this.device, this.slots);
		},
		start() {
			return new Date(this.slots[0]!.start).getTime();
		},
		end() {
			return new Date(this.slots[this.slots.length - 1]!.end).getTime();
		},
		ticks() {
			return [this.start, (this.start + this.end) / 2, this.end].map((time) =>
				new Date(time).toISOString()
			);
		},
		envelope() {
			if (this.low?.length !== this.slots.length || this.high?.length !== this.slots.length)
				return undefined;
			const atEnds = (values: number[]) => [
				...(this.device.initialSoc != null && Number.isFinite(this.device.initialSoc)
					? [{ time: this.slots[0]!.start, soc: this.device.initialSoc }]
					: []),
				...values.map((soc, index) => ({ time: this.slots[index]!.end, soc })),
			];
			return `${this.points(atEnds(this.low!))} ${this.points(atEnds(this.high!).reverse())}`;
		},
	},
	methods: {
		x(time: string) {
			return (
				((new Date(time).getTime() - this.start) / Math.max(1, this.end - this.start)) * 665
			);
		},
		y(soc: number) {
			return 135 - soc * 1.35;
		},
		points(values: { time: string; soc: number }[]) {
			return values.map((point) => `${this.x(point.time)},${this.y(point.soc)}`).join(" ");
		},
		dateTime(time: string) {
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
svg {
	display: block;
	width: 100%;
	height: 135px;
	min-width: 0;
	overflow: visible;
}
.chart-area {
	display: grid;
	grid-template-columns: 3rem minmax(0, 1fr);
	padding-top: 0.5rem;
}
.y-axis {
	display: flex;
	flex-direction: column;
	justify-content: space-between;
	height: 135px;
}
.y-axis span:first-child {
	transform: translateY(-50%);
}
.y-axis span:last-child {
	transform: translateY(50%);
}
.x-axis {
	margin: 0.7rem 0 0.7rem 3rem;
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
.soc-line {
	fill: none;
	stroke: var(--evcc-battery);
	stroke-width: 3;
	vector-effect: non-scaling-stroke;
}
.soc-envelope {
	fill: var(--evcc-battery);
	opacity: 0.18;
}
.soc-cursor {
	stroke: var(--evcc-default-text);
	stroke-dasharray: 4;
}
.energy-soc .small {
	color: var(--evcc-default-text);
}
.legend-line,
.legend-band {
	display: inline-block;
	width: 1rem;
	margin-right: 0.4rem;
	vertical-align: middle;
}
.legend-line {
	border-top: 3px solid var(--evcc-battery);
}
.legend-band {
	height: 0.7rem;
	background: var(--evcc-battery);
	opacity: 0.3;
}
</style>
