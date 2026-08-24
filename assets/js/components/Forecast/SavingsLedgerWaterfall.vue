<template>
	<!-- the diagram is the card's primary content, so it announces its own start and
	     end figures rather than reading as an empty div -->
	<div
		ref="chartEl"
		class="waterfall"
		role="img"
		:aria-label="ariaLabel"
		:style="{ height: `${CHART_HEIGHT}px` }"
		data-testid="savings-ledger-waterfall"
	></div>
</template>

<script lang="ts">
import { defineComponent, type PropType } from "vue";
import { FONT_FAMILY, tooltipStyle, forecastGrid, forecastYAxis } from "./echarts";
import colors, { batteryColor, lighterColor, setAlpha } from "@/colors";
import escapeHtml from "@/utils/escapeHtml";
import formatter from "@/mixins/formatter";
// NOT ./chartMixin: that one is for the scroll-synced, time-axis forecast charts. This
// is a categorical chart that must fit the card's width with no horizontal scroll.
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
// plotting-area height below comes from the numbers the chart is actually drawn at:
// minSpan() converts a pixel minimum into value units and silently lies if they drift.
const CHART_HEIGHT = 220;
const GRID_TOP = 28;
const GRID_BOTTOM = 48;
// exported for the no-clipping invariant asserted in savingsLedgerWaterfall.test.ts, see
// AXIS_HEADROOM's doc comment for what it protects
export const PLOT_HEIGHT = CHART_HEIGHT - GRID_TOP - GRID_BOTTOM;
// wide viewports would otherwise draw five fat slabs
const BAR_MAX_WIDTH = 64;
// below this rendered height an "estimated" bar is drawn solid instead of dash-outlined,
// see itemStyle()
const DASHED_OUTLINE_MIN_PX = 14;

// A contribution's effect on the bill as a direction rather than a sign: beside a strip
// reading "saved EUR 6.06", a bare "-EUR 5.88" on the bar above it reads as a loss.
// Down = took money off the bill, up = added to it.
const GLYPH_DOWN = "↓";
const GLYPH_UP = "↑";

export default defineComponent({
	name: "SavingsLedgerWaterfall",
	mixins: [formatter, echartsChart],
	props: {
		chain: { type: Object as PropType<LedgerChain>, required: true },
		headline: {
			type: String as PropType<SettlementHeadline>,
			required: true,
		},
		currency: { type: String as PropType<CURRENCY> },
	},
	data() {
		// exposed to the template so the drawn height and PLOT_HEIGHT come from one number
		return { CHART_HEIGHT };
	},
	computed: {
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
				// the headline toggle swaps every figure in place; animating that
				// makes it legible instead of a jump cut
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
					// same box as every other forecast chart, confined to the card
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
				// (plotBase), so nothing is clipped and the formatter below still reads euros
				yAxis: forecastYAxis({
					max: axis.max,
					interval: axis.interval,
					axisLabel: {
						color: muted,
						// whole euros: waterfallAxis chooses whole-unit ticks. EXCEPT
						// when origin is non-zero, where the ticks are offset by it and
						// rounding prints a gridline at "EUR 1" that sits at EUR 0.80.
						// The honest figure wins over the round one.
						formatter: (value: number) =>
							this.fmtMoney(value + origin, this.currency, origin !== 0, true),
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
							itemStyle: this.itemStyle(
								c,
								axis.max > 0 ? (plotSpan(c, floor) / axis.max) * PLOT_HEIGHT : 0
							),
							// per item, not per series: the label must never inherit the
							// bar's colour, and an overspend keeps its danger colour
							label: { color: this.labelColor(c) },
						})),
					},
					{
						// one horizontal rule per running level. Every vertical step of
						// step:"end" coincides with the bar that caused it, so a lower z
						// hides them and only the horizontal links stay visible.
						name: "connector",
						type: "line",
						step: "end",
						symbol: "none",
						silent: true,
						z: 1,
						animation: false,
						lineStyle: {
							color: setAlpha(colors.muted, "66") || muted,
							width: 1,
						},
						tooltip: { show: false },
						data: plotLevels(layout),
					},
				],
			};
		},
	},
	methods: {
		// the estimate marker lives IN the axis label, never in a footnote
		axisLabel(key: string): string {
			const name = this.$t(`forecast.savingsLedger.axis.${key}`) as string;
			const line = `{n|${name}}`;
			if (!this.layout.columns.find((c) => c.key === key)?.estimated) return line;
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
			// The overspend test above is band-gated because a figure inside the period's
			// measurement noise is not evidence of a direction. That is symmetric: a
			// saturated palette colour claims a saving as loudly as the danger colour
			// claims a loss, so neutral BOTH ways. Only the semantic colour goes.
			if (column.insideNoise) return colors.muted || "";
			switch (column.key) {
				case "baseline":
					// the quietest element on the chart: the reference every other bar
					// is measured against, not a measure of its own
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
			// currentColor rather than a hardcoded hex, so the label stays legible even if
			// the CSS custom property read in colors.ts came back empty
			return colors.text || "currentColor";
		},
		// estimated bars carry a dashed outline as well as their axis label, so the
		// distinction survives a greyscale filter. (itemStyle.decal is deliberately not
		// used: the decal/aria machinery isn't registered in echarts.ts.)
		//
		// heightPx gates the outline: on a short bar a 1px dash on all four sides leaves
		// almost no fill and the bar reads as a dotted rule. Drawing it solid below the
		// threshold is safe because the "estimated" marker lives in the axis label; the
		// outline is a redundant second channel, and one that destroys the bar is worse
		// than none.
		itemStyle(column: WaterfallColumn, heightPx: number): Record<string, unknown> {
			const style: Record<string, unknown> = {
				color: this.columnColor(column),
				borderRadius: 3,
			};
			if (column.estimated && heightPx >= DASHED_OUTLINE_MIN_PX) {
				style["borderType"] = "dashed";
				style["borderColor"] = colors.text || "";
				style["borderWidth"] = 1;
			}
			return style;
		},
		// Deliberately short. The battery provenance strings live in the info modal, in
		// full: repeating them here turns the tooltip into a paragraph covering the bar.
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

			// a figure inside the period's measured noise floor has a magnitude but no
			// usable direction. The number stays exactly as computed; this only stops the
			// tooltip reading as though the sign meant something.
			if (column.insideNoise && !column.zero) {
				lines.splice(
					2,
					0,
					`<div class="fw-normal">${t("forecast.savingsLedger.insideNoise", {
						band: this.fmtMoney(this.layout.band, this.currency, true, true),
					})}</div>`
				);
			}

			// routing/timing is surfaced here rather than as its own column: they sum to
			// Control and are only a coherent pair under the perSlot headline, see
			// WaterfallKey's doc comment for why.
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
