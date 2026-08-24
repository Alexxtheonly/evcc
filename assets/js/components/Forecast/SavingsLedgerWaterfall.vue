<template>
	<!-- role/aria-label: the diagram is the card's primary content, so it must announce
	     its own start and end figures rather than reading as an empty div. -->
	<div
		ref="chartEl"
		class="waterfall"
		role="img"
		:aria-label="ariaLabel"
		:style="{ height: `${chartHeight}px` }"
		data-testid="savings-ledger-waterfall"
	></div>
</template>

<script lang="ts">
import { defineComponent, type PropType } from "vue";
import { FONT_FAMILY, tooltipStyle, forecastGrid, forecastYAxis } from "./echarts";
import colors, { batteryColor, lighterColor, setAlpha } from "@/colors";
import escapeHtml from "@/utils/escapeHtml";
import formatter from "@/mixins/formatter";
// NOT ./chartMixin: that one is for the three horizontally-scrolling, time-axis,
// mutually-scroll-synced forecast charts. This is a categorical chart that must fit the
// card's width with no horizontal scroll, so it uses the plain echarts lifecycle mixin.
import echartsChart from "@/mixins/echartsChart";
import type { CURRENCY } from "@/types/evcc";
import type { LedgerChain } from "./savingsLedger.types";
import { ZERO_EPSILON_EUR, type SettlementHeadline } from "./savingsLedgerChain";
import {
	waterfallLayout,
	waterfallAxis,
	plotBase,
	plotLevels,
	plotSpan,
	minSpan,
	type WaterfallColumn,
	type WaterfallLayout,
} from "./savingsLedgerWaterfall";

// Chart box, in px. Bound as an inline style rather than left in the stylesheet so the
// plotting-area height below is derived from the same numbers the chart is actually
// drawn at - minSpan() converts a pixel minimum into value units and silently lies if
// these drift from the CSS.
const CHART_HEIGHT = 220;
const GRID_TOP = 28;
const GRID_BOTTOM = 48;
const PLOT_HEIGHT = CHART_HEIGHT - GRID_TOP - GRID_BOTTOM;
// wide viewports would otherwise draw five fat slabs
const BAR_MAX_WIDTH = 64;

// A contribution's effect on the bill, as a direction rather than a sign: the strip under
// the chart says "saved EUR 6.06", so a bare "-EUR 5.88" on the bar 40px above it can be
// read as a loss. Down = this measure took money off the bill, up = it added to it.
const GLYPH_DOWN = "↓";
const GLYPH_UP = "↑";

export default defineComponent({
	name: "SavingsLedgerWaterfall",
	mixins: [formatter, echartsChart],
	props: {
		chain: { type: Object as PropType<LedgerChain>, required: true },
		headline: { type: String as PropType<SettlementHeadline>, required: true },
		currency: { type: String as PropType<CURRENCY> },
	},
	computed: {
		chartHeight(): number {
			return CHART_HEIGHT;
		},
		layout(): WaterfallLayout {
			return waterfallLayout(this.chain, this.headline);
		},
		ariaLabel(): string {
			const money = (v: number) => this.fmtMoney(v, this.currency, true, true);
			return this.$t("forecast.savingsLedger.chartAria", {
				start: money(this.layout.w0),
				end: money(this.layout.paid),
			}) as string;
		},
		chartOption(): Record<string, unknown> {
			const layout = this.layout;
			const cols = layout.columns;
			const muted = colors.muted || "";
			const origin = layout.origin;
			const axis = waterfallAxis(layout);
			const floor = minSpan(axis.max, PLOT_HEIGHT);

			return {
				animationDuration: 0,
				// the headline toggle swaps every figure in place - a short update
				// animation makes that legible instead of a jump cut (PriceChart.vue)
				animationDurationUpdate: 300,
				textStyle: { fontFamily: FONT_FAMILY },
				grid: {
					...forecastGrid(),
					top: GRID_TOP,
					bottom: GRID_BOTTOM,
					left: 36,
					right: 10,
				},
				tooltip: {
					trigger: "item",
					// same box as every other forecast chart (solid text-colour panel,
					// background-colour type), not the plain grey one this chart used to
					// draw. confine keeps it inside the card on a phone.
					...tooltipStyle(colors.text || ""),
					formatter: (p: { dataIndex: number }) => this.tooltipHtml(cols[p.dataIndex]),
				},
				xAxis: {
					type: "category",
					data: cols.map((c) => c.key),
					axisLine: { show: false },
					axisTick: { show: false },
					axisLabel: {
						interval: 0,
						hideOverlap: false,
						margin: 8,
						formatter: (key: string) => this.axisLabel(key),
						rich: {
							n: {
								fontSize: 11,
								lineHeight: 14,
								color: muted,
								fontFamily: FONT_FAMILY,
							},
							e: {
								fontSize: 9,
								lineHeight: 12,
								color: muted,
								fontFamily: FONT_FAMILY,
								fontStyle: "italic",
							},
						},
					},
				},
				// min stays 0: the chart is plotted in a system translated by layout.origin
				// (see plotBase), which is zero in every normal period and negative exactly
				// when a level dips below zero - so nothing is ever clipped and the axis
				// labels still read real euros via the formatter below.
				yAxis: forecastYAxis({
					max: axis.max,
					interval: axis.interval,
					axisLabel: {
						color: muted,
						// whole euros: the ticks are chosen as whole units (waterfallAxis),
						// and cents on a gridline are noise next to the bars' own labels.
						formatter: (value: number) =>
							this.fmtMoney(value + origin, this.currency, false, true),
					},
				}),
				series: [
					{
						// invisible pedestal that lifts each floating bar to its base
						name: "base",
						type: "bar",
						stack: "waterfall",
						silent: true,
						z: 2,
						barWidth: "56%",
						barMaxWidth: BAR_MAX_WIDTH,
						itemStyle: { color: "transparent" },
						tooltip: { show: false },
						data: cols.map((c) => plotBase(c, layout)),
					},
					{
						name: "value",
						type: "bar",
						stack: "waterfall",
						z: 2,
						barWidth: "56%",
						barMaxWidth: BAR_MAX_WIDTH,
						label: {
							show: true,
							position: "top",
							fontFamily: FONT_FAMILY,
							fontSize: 11,
							fontWeight: "bold",
							formatter: (p: { dataIndex: number }) =>
								this.columnValueLabel(cols[p.dataIndex]),
						},
						data: cols.map((c) => ({
							value: plotSpan(c, floor),
							itemStyle: this.itemStyle(c),
							// per item, not per series: the label must never inherit the
							// bar's own colour (a dim battery green on a dark card is
							// barely legible), and an overspend keeps its danger colour.
							label: { color: this.labelColor(c) },
						})),
					},
					{
						// the waterfall's connectors: one horizontal rule per running level,
						// so the eye follows the descent from bar to bar. step:"end" holds
						// each level until the next category, then steps to the new one -
						// and every one of those vertical steps coincides exactly with the
						// bar that caused it, so a lower z hides them behind the bars and
						// only the horizontal links between bars are visible.
						name: "connector",
						type: "line",
						step: "end",
						symbol: "none",
						silent: true,
						z: 1,
						animation: false,
						lineStyle: { color: setAlpha(colors.muted, "66") || muted, width: 1 },
						tooltip: { show: false },
						data: plotLevels(layout),
					},
				],
			};
		},
	},
	methods: {
		column(key: string): WaterfallColumn | undefined {
			return this.layout.columns.find((c) => c.key === key);
		},
		// ADR-011 rule 7: the estimate marker lives IN the axis label, not in a footnote.
		axisLabel(key: string): string {
			const name = this.$t(`forecast.savingsLedger.axis.${key}`) as string;
			const line = `{n|${name}}`;
			if (!this.column(key)?.estimated) return line;
			return `${line}\n{e|${this.$t("forecast.savingsLedger.estimated")}}`;
		},
		// Money as an effect on the bill. A contribution is POSITIVE when it SAVED money
		// (ledger_worlds.go). The two end columns are levels, printed plain.
		columnValueLabel(column: WaterfallColumn | undefined): string {
			if (!column) return "";
			if (column.total) return this.fmtMoney(column.eur, this.currency, true, true);
			// |eur| <= ZERO_EPSILON_EUR already means "nothing moved" - print a clean zero
			// rather than a direction, which would claim the bill moved.
			if (column.zero) return this.fmtMoney(0, this.currency, true, true);
			return this.billEffect(column.eur);
		},
		// a contribution, rendered as which way it moved the bill: a cost can never be
		// mistaken for a saving just because its magnitude is printed positive
		billEffect(eur: number): string {
			if (Math.abs(eur) <= ZERO_EPSILON_EUR) {
				return this.fmtMoney(0, this.currency, true, true);
			}
			const money = this.fmtMoney(Math.abs(eur), this.currency, true, true);
			return `${eur > 0 ? GLYPH_DOWN : GLYPH_UP} ${money}`;
		},
		columnColor(column: WaterfallColumn): string {
			if (column.overspend) return colors.danger || "";
			switch (column.key) {
				case "baseline":
					// the quietest element on the chart: it is the reference every other
					// bar is measured against, not a measure of its own. colors.grid is
					// near-black in the light theme and would dominate.
					return lighterColor(colors.muted) || colors.muted || "";
				case "pv":
					return colors.self || "";
				case "battery":
					return batteryColor(0);
				case "control":
					return batteryColor(1);
				default:
					return colors.price || "";
			}
		},
		// full-contrast text in both themes, whatever the bar underneath it is coloured
		labelColor(column: WaterfallColumn): string {
			if (column.overspend) return colors.danger || "";
			// currentColor rather than a hardcoded hex: the SVG renderer resolves it
			// against the card's own text colour, so the label stays legible even if the
			// CSS custom property read in colors.ts came back empty.
			return colors.text || "currentColor";
		},
		// estimated bars carry a dashed outline as well as their axis label, so the
		// distinction survives a greyscale filter and doesn't rest on colour alone. The
		// outline is deliberately thin: at 1.5px it swallowed the whole fill of a small
		// bar, which then read as a dotted hairline rather than a bar.
		// (itemStyle.decal is deliberately not used - the decal/aria machinery isn't
		// registered in echarts.ts.)
		itemStyle(column: WaterfallColumn): Record<string, unknown> {
			const style: Record<string, unknown> = {
				color: this.columnColor(column),
				borderRadius: 3,
			};
			if (column.estimated) {
				style["borderType"] = "dashed";
				style["borderColor"] = colors.text || "";
				style["borderWidth"] = 1;
			}
			return style;
		},
		// Deliberately short: the column, its figure, what it means, and whether it is
		// measured or estimated. The battery provenance strings (capacitySource/etaSource/
		// floorSource) live in the info modal, in full - repeating them here turned the
		// tooltip into a paragraph that covered the bar it was describing.
		tooltipHtml(column: WaterfallColumn | undefined): string {
			if (!column) return "";
			const t = (key: string, params?: Record<string, unknown>) =>
				escapeHtml(this.$t(key, params ?? {}) as string);
			const lines = [
				`<div class="fw-bold">${t(`forecast.savingsLedger.chain.${column.key}.label`)}</div>`,
				`<div class="fw-bold tabular">${escapeHtml(this.columnValueLabel(column))}</div>`,
				`<div class="fw-normal">${t(`forecast.savingsLedger.chain.${column.key}.sub`)}</div>`,
			];

			// a zero-magnitude column gets no basis tag: there is no figure to qualify
			if (!column.total && !column.zero) {
				const key = column.estimated ? "estimated" : "measured";
				lines.splice(
					2,
					0,
					`<div class="fw-normal">${t(`forecast.savingsLedger.${key}`)}</div>`
				);
			}

			// routing/timing is surfaced here rather than as its own column: they sum to
			// Control, and only under the perSlot headline are they a coherent pair - see
			// WaterfallKey's doc comment in savingsLedgerWaterfall.ts for why they must
			// never be shown beside a periodAverage Control figure.
			if (column.key === "control" && this.chain.control && this.headline === "perSlot") {
				const money = (v: number) => escapeHtml(this.billEffect(v));
				lines.push(
					`<div class="fw-normal">${t("forecast.savingsLedger.chain.routing.label")}: ${money(this.chain.control.routing)}</div>`,
					`<div class="fw-normal">${t("forecast.savingsLedger.chain.timing.label")}: ${money(this.chain.control.timing)}</div>`
				);
			}

			return `<div class="text-start" style="max-width: 15rem; white-space: normal">${lines.join("")}</div>`;
		},
	},
});
</script>

<style scoped>
/* height is bound inline from CHART_HEIGHT - see its comment */
.waterfall {
	width: 100%;
}
</style>
