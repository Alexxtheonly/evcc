<template>
	<div ref="chartEl" class="waterfall" data-testid="savings-ledger-waterfall"></div>
</template>

<script lang="ts">
import { defineComponent, type PropType } from "vue";
import { FONT_FAMILY, tooltipStyle, forecastGrid, forecastYAxis } from "./echarts";
import colors, { batteryColor } from "@/colors";
import escapeHtml from "@/utils/escapeHtml";
import formatter from "@/mixins/formatter";
// NOT ./chartMixin: that one is for the three horizontally-scrolling, time-axis,
// mutually-scroll-synced forecast charts. This is a categorical chart that must fit the
// card's width with no horizontal scroll, so it uses the plain echarts lifecycle mixin.
import echartsChart from "@/mixins/echartsChart";
import type { CURRENCY } from "@/types/evcc";
import type { LedgerChain } from "./savingsLedger.types";
import type { SettlementHeadline } from "./savingsLedgerChain";
import {
	waterfallLayout,
	plotBase,
	type WaterfallColumn,
	type WaterfallLayout,
} from "./savingsLedgerWaterfall";

export default defineComponent({
	name: "SavingsLedgerWaterfall",
	mixins: [formatter, echartsChart],
	props: {
		chain: { type: Object as PropType<LedgerChain>, required: true },
		headline: { type: String as PropType<SettlementHeadline>, required: true },
		currency: { type: String as PropType<CURRENCY> },
	},
	computed: {
		layout(): WaterfallLayout {
			return waterfallLayout(this.chain, this.headline);
		},
		chartOption(): Record<string, unknown> {
			const layout = this.layout;
			const cols = layout.columns;
			const muted = colors.muted || "";
			const origin = layout.origin;

			return {
				animationDuration: 0,
				// the headline toggle swaps every figure in place - a short update
				// animation makes that legible instead of a jump cut (PriceChart.vue)
				animationDurationUpdate: 300,
				textStyle: { fontFamily: FONT_FAMILY },
				grid: { ...forecastGrid(), top: 28, bottom: 44, left: 48, right: 10 },
				tooltip: {
					trigger: "item",
					...tooltipStyle(colors.grid || ""),
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
					splitNumber: 3,
					max: (value: { max: number }) => (value.max > 0 ? value.max * 1.15 : 1),
					axisLabel: {
						color: muted,
						formatter: (value: number) =>
							this.fmtMoney(value + origin, this.currency, true, true),
					},
				}),
				series: [
					{
						// invisible pedestal that lifts each floating bar to its base
						name: "base",
						type: "bar",
						stack: "waterfall",
						silent: true,
						barWidth: "56%",
						itemStyle: { color: "transparent" },
						tooltip: { show: false },
						data: cols.map((c) => plotBase(c, layout)),
					},
					{
						name: "value",
						type: "bar",
						stack: "waterfall",
						barWidth: "56%",
						label: {
							show: true,
							position: "top",
							fontFamily: FONT_FAMILY,
							fontSize: 11,
							fontWeight: "bold",
							color: colors.text || "",
							formatter: (p: { dataIndex: number }) =>
								this.columnValueLabel(cols[p.dataIndex]),
						},
						data: cols.map((c) => ({ value: c.span, itemStyle: this.itemStyle(c) })),
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
		// (ledger_worlds.go), so the bill moves by -eur: a saving reads "-3.95 EUR", an
		// overspend "+0.41 EUR". The two end columns are levels, printed plain.
		columnValueLabel(column: WaterfallColumn | undefined): string {
			if (!column) return "";
			if (column.total) return this.fmtMoney(column.eur, this.currency, true, true);
			// |eur| <= ZERO_EPSILON_EUR already means "nothing moved" - print a clean zero
			// rather than the signed remainder, which would round to "-0.00 EUR".
			if (column.zero) return this.fmtMoney(0, this.currency, true, true);
			return this.billEffect(column.eur);
		},
		// a contribution, rendered as what it did to the bill: signed, so a cost can never
		// be mistaken for a saving just because its magnitude is printed positive
		billEffect(eur: number): string {
			const delta = -eur;
			const money = this.fmtMoney(delta, this.currency, true, true);
			return delta > 0 ? `+${money}` : money;
		},
		columnColor(column: WaterfallColumn): string {
			if (column.overspend) return colors.danger || "";
			switch (column.key) {
				case "baseline":
					return colors.grid || "";
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
		// estimated bars carry a dashed outline as well as their axis label, so the
		// distinction survives a greyscale filter and doesn't rest on colour alone.
		// (itemStyle.decal is deliberately not used - the decal/aria machinery isn't
		// registered in echarts.ts.)
		itemStyle(column: WaterfallColumn): Record<string, unknown> {
			const style: Record<string, unknown> = { color: this.columnColor(column) };
			if (column.estimated) {
				style["borderType"] = "dashed";
				style["borderColor"] = colors.text || "";
				style["borderWidth"] = 1.5;
			}
			return style;
		},
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
			// chainSegments' doc comment in savingsLedgerChain.ts for why they must never
			// be shown beside a periodAverage Control figure.
			if (column.key === "control" && this.chain.control && this.headline === "perSlot") {
				const money = (v: number) => escapeHtml(this.billEffect(v));
				lines.push(
					`<div class="fw-normal">${t("forecast.savingsLedger.chain.routing.label")}: ${money(this.chain.control.routing)}</div>`,
					`<div class="fw-normal">${t("forecast.savingsLedger.chain.timing.label")}: ${money(this.chain.control.timing)}</div>`
				);
			}

			// ADR-011 rule 7 again: the real provenance strings from batteryPhysics, not a
			// fabricated numeric error bar (the API has none).
			const phys = this.chain.batteryPhysics;
			if (column.estimated && phys) {
				lines.push(
					`<div class="fw-normal">${t("forecast.savingsLedger.physicsShort", {
						capacity: phys.capacitySource,
						eta: phys.etaSource,
						floor: phys.floorSource,
					})}</div>`
				);
			}

			return `<div class="text-start" style="max-width: 17rem; white-space: normal">${lines.join("")}</div>`;
		},
	},
});
</script>

<style scoped>
.waterfall {
	width: 100%;
	height: 200px;
}
</style>
