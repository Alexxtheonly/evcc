<template>
	<div class="solar-shading" data-testid="solar-shading">
		<p v-if="loading" class="small mt-2" role="status">
			{{ $t("forecast.energy.shading.loading") }}
		</p>
		<p v-else-if="error" class="small mt-2 text-danger" role="alert">
			{{ $t("forecast.energy.shading.loadError") }}
		</p>
		<template v-else-if="status">
			<p class="small mt-2 mb-1" data-testid="solar-shading-status">
				<span
					class="badge me-1"
					:class="status.active ? 'text-bg-primary' : 'text-bg-secondary'"
					data-testid="solar-shading-state"
					>{{
						$t(
							status.active
								? "forecast.energy.shading.active"
								: "forecast.energy.shading.notActive"
						)
					}}</span
				>{{ $t(statusLine.key, statusLine.params) }}
				<template v-if="updated">
					· {{ $t("forecast.energy.shading.updated", { time: updated }) }}</template
				>
			</p>
			<p v-if="status.available" class="small text-muted mb-2">{{ fallbackNote }}</p>
			<template v-if="grid.cells.length">
				<div
					ref="chartEl"
					class="shading-chart"
					role="img"
					:aria-label="$t('forecast.energy.shading.chartLabel')"
					data-testid="solar-shading-chart"
				></div>
				<div class="shading-legend small text-muted mt-1" aria-hidden="true">
					<span class="legend-item">
						<span>{{ $t("forecast.energy.shading.legendBelow") }}</span>
						<span class="legend-ramp" :style="{ background: legendRamp }"></span>
						<span>{{ $t("forecast.energy.shading.legendAbove") }}</span>
					</span>
					<span class="legend-item">
						<span
							class="legend-swatch legend-neighbour"
							:style="{ background: neighbourSwatch }"
						></span>
						<span>{{ $t("forecast.energy.shading.legendNeighbour") }}</span>
					</span>
					<span class="legend-item">
						<span class="legend-swatch legend-empty"></span>
						<span>{{ $t("forecast.energy.shading.legendEmpty") }}</span>
					</span>
				</div>
			</template>
			<p v-if="grid.cells.length" class="small text-muted mt-2 mb-0">
				{{ $t("forecast.energy.shading.explain") }}
			</p>
		</template>
	</div>
</template>

<script lang="ts">
import { defineComponent } from "vue";
import api from "@/api";
import colors from "@/colors";
import formatter from "@/mixins/formatter";
import echartsChart from "@/mixins/echartsChart";
import { FONT_FAMILY, tooltipStyle, tooltipTable, xAxisLabelStyle } from "./echarts";
import {
	compassKey,
	factorColor,
	shadingGrid,
	shadingStatusLine,
	type SolarShadingStatus,
} from "./solarShading";

// a blue for "more than forecast": the palette's mid blue, distinct from the
// danger hue for colour-vision deficiencies
const ABOVE = () => colors.palette[7] || "#2563EB";
const NEIGHBOUR_OPACITY = 0.45;

export default defineComponent({
	name: "SolarShadingDetails",
	mixins: [formatter, echartsChart],
	data: () => ({
		status: null as SolarShadingStatus | null,
		loading: true,
		error: false,
	}),
	computed: {
		grid() {
			return this.status
				? shadingGrid(this.status)
				: { azimuths: [], elevations: [], cells: [] };
		},
		statusLine() {
			return this.status ? shadingStatusLine(this.status) : { key: "", params: {} };
		},
		updated(): string {
			return this.status?.updated
				? this.fmtFullDateTime(new Date(this.status.updated), true)
				: "";
		},
		fallbackNote(): string {
			if (!this.status) return "";
			return this.status.fallback === "daily"
				? this.$t("forecast.energy.shading.fallbackDaily", {
						factor: this.fmtNumber(this.status.fallbackFactor, 2),
					})
				: this.$t("forecast.energy.shading.fallbackNone");
		},
		palette() {
			return {
				below: colors.danger || "#fc440f",
				above: ABOVE(),
				neutral: colors.box || "#ffffff",
			};
		},
		range() {
			const p = this.status?.parameters;
			return { min: p?.minFactor ?? 0.05, max: p?.maxFactor ?? 1.5 };
		},
		neighbourSwatch(): string {
			return factorColor(0.4, this.range, this.palette);
		},
		legendRamp(): string {
			const stops = [this.range.min, 0.5, 1, (1 + this.range.max) / 2, this.range.max];
			return `linear-gradient(to right, ${stops
				.map((f) => factorColor(f, this.range, this.palette))
				.join(", ")})`;
		},
		chartOption(): Record<string, unknown> {
			const { azimuths, elevations, cells } = this.grid;
			if (!cells.length || !this.status) return {};
			const step = this.status.parameters;
			const axisLabel = xAxisLabelStyle();
			const tooltipColor = colors.text || "#000";
			return {
				animation: false,
				textStyle: { fontFamily: FONT_FAMILY },
				grid: { top: 8, right: 8, bottom: 44, left: 40, containLabel: false },
				tooltip: {
					...tooltipStyle(tooltipColor),
					trigger: "item",
					position: "top",
					formatter: (p: { dataIndex: number }) => this.tooltip(p.dataIndex),
				},
				xAxis: {
					type: "category",
					data: azimuths.map((a) => a),
					name: this.$t("forecast.energy.shading.azimuth"),
					nameLocation: "middle",
					nameGap: 28,
					nameTextStyle: { color: colors.muted || "", fontSize: 12 },
					axisLabel: {
						...axisLabel,
						fontSize: 12,
						interval: 0,
						formatter: (value: string) => {
							const az = Number(value);
							const key = compassKey(az);
							return key ? this.$t(`forecast.energy.shading.compass.${key}`) : "";
						},
					},
					axisTick: { show: false },
					axisLine: { show: false },
					splitArea: { show: false },
				},
				yAxis: {
					type: "category",
					data: elevations.map((e) => e),
					name: this.$t("forecast.energy.shading.elevation"),
					nameLocation: "middle",
					nameGap: 30,
					nameTextStyle: { color: colors.muted || "", fontSize: 12 },
					axisLabel: {
						color: colors.muted || "",
						fontSize: 12,
						interval: (index: number) => (index * step.elevationStep) % 20 === 0,
						formatter: (value: string) => `${value}°`,
					},
					axisTick: { show: false },
					axisLine: { show: false },
					splitArea: {
						show: true,
						areaStyle: { color: ["transparent"] },
					},
					splitLine: {
						show: true,
						interval: 0,
						lineStyle: { color: colors.border || "", type: "dashed" },
					},
				},
				series: [
					{
						type: "heatmap",
						data: cells.map((c) => ({
							value: [c.x, c.y, c.bin.factor],
							itemStyle: {
								color: factorColor(c.bin.factor, this.range, this.palette),
								// borrowed from neighbouring positions: shown, but fainter
								opacity: c.bin.source === "learned" ? 1 : NEIGHBOUR_OPACITY,
								borderColor: colors.box || "",
								borderWidth: 1,
							},
						})),
						emphasis: {
							itemStyle: { borderColor: colors.text || "", borderWidth: 1 },
						},
					},
				],
			};
		},
	},
	async mounted() {
		await this.load();
	},
	methods: {
		async load() {
			this.loading = true;
			this.error = false;
			try {
				this.status = (await api.get<SolarShadingStatus>("energyplan/solarshading")).data;
			} catch {
				this.error = true;
			} finally {
				this.loading = false;
			}
		},
		tooltip(index: number): string {
			const cell = this.grid.cells[index];
			if (!cell) return "";
			const b = cell.bin;
			const deg = (v: number) => `${this.fmtNumber(v, 0)}°`;
			const head = this.$t("forecast.energy.shading.tooltipPosition", {
				azimuth: `${deg(b.azimuthFrom)}–${deg(b.azimuthTo)}`,
				elevation: `${deg(b.elevationFrom)}–${deg(b.elevationTo)}`,
			});
			return tooltipTable(head, [
				{
					name: this.$t("forecast.energy.shading.tooltipFactor"),
					values: [this.fmtNumber(b.factor, 2)],
				},
				{
					name: this.$t("forecast.energy.shading.tooltipRange"),
					values: [`${this.fmtNumber(b.low, 2)}–${this.fmtNumber(b.high, 2)}`],
				},
				{
					name: this.$t("forecast.energy.shading.tooltipEvidence"),
					values: [
						this.$t("forecast.energy.shading.tooltipSlots", {
							slots: b.slots,
							weight: this.fmtNumber(b.weight, 1),
						}),
					],
				},
				{
					name: this.$t("forecast.energy.shading.tooltipLast"),
					values: [this.fmtDayMonth(new Date(b.lastSample))],
				},
				{
					name: this.$t("forecast.energy.shading.tooltipSource"),
					values: [this.$t(`forecast.energy.shading.source.${b.source}`)],
				},
			]);
		},
	},
});
</script>

<style scoped>
.shading-chart {
	width: 100%;
	height: 220px;
}
.shading-legend {
	display: flex;
	align-items: center;
	flex-wrap: wrap;
	gap: 0.4rem;
}
.legend-item {
	display: inline-flex;
	align-items: center;
	gap: 0.4rem;
	margin-right: 0.6rem;
}
.legend-ramp {
	display: inline-block;
	width: 6rem;
	height: 0.6rem;
	border-radius: 2px;
}
.legend-swatch {
	display: inline-block;
	width: 0.8rem;
	height: 0.6rem;
	border-radius: 2px;
}
.legend-neighbour {
	opacity: 0.45;
}
.legend-empty {
	border: 1px dashed var(--bs-border-color);
}
</style>
