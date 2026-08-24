<template>
	<div class="savings-ledger-decisions" data-testid="savings-ledger-decisions">
		<!-- no heading of its own: this strip is mounted as its own Card in
		     views/Forecast.vue, whose header renders decisions.title. -->
		<div v-if="slots.length" class="section-head">
			<SelectGroup
				id="savingsLedgerDecisionsView"
				:options="viewOptions"
				:model-value="view"
				:aria-label="$t('forecast.savingsLedger.decisions.title')"
				data-testid="savings-ledger-decisions-view"
				@update:model-value="setView"
			/>
		</div>

		<p
			v-if="slots.length === 0"
			class="text-muted small mb-0"
			data-testid="savings-ledger-decisions-empty"
		>
			{{ $t("forecast.savingsLedger.decisions.empty") }}
		</p>

		<template v-else-if="view === 'timeline'">
			<!-- role/aria-label: the ribbon carries the card's whole message, so it must
			     announce its own summary rather than read as an empty div. The table view
			     is the per-slot detail for anyone the graphic doesn't serve. -->
			<div
				ref="chartEl"
				class="timeline"
				role="img"
				:aria-label="ariaLabel"
				:style="{ height: `${chartHeight}px` }"
				data-testid="savings-ledger-decisions-timeline"
			></div>

			<LegendList :legends="legends" class="mt-2" />

			<p class="caption text-gray mt-2" data-testid="savings-ledger-decisions-caption">
				{{ caption
				}}<span
					v-if="netClause"
					:class="netClass"
					data-testid="savings-ledger-decisions-net"
				>
					· {{ netClause }}</span
				>
			</p>
			<!-- the slot-local caveat is shown only where a slot-local figure is: with no
			     priced override on screen it qualifies nothing. -->
			<p
				v-if="summary.divergedPriced > 0"
				class="caption text-gray"
				data-testid="savings-ledger-decisions-delta-note"
			>
				{{ $t("forecast.savingsLedger.decisions.deltaNote") }}
			</p>
		</template>

		<div v-else data-testid="savings-ledger-decisions-table-wrap">
			<!-- a period can hold 96 rows a day; without a cap the table pushes every
			     card below it off the screen -->
			<div class="tbl-scroll">
				<table class="tbl" data-testid="savings-ledger-decisions-table">
					<thead>
						<tr>
							<th>{{ $t("forecast.savingsLedger.decisions.table.time") }}</th>
							<th>{{ $t("forecast.savingsLedger.decisions.table.suggested") }}</th>
							<th>{{ $t("forecast.savingsLedger.decisions.table.applied") }}</th>
							<th class="num">
								{{ $t("forecast.savingsLedger.decisions.table.delta") }}
							</th>
							<th>{{ $t("forecast.savingsLedger.decisions.table.basis") }}</th>
						</tr>
					</thead>
					<tbody>
						<tr
							v-for="slot in slots"
							:key="slot.row.ts"
							:data-testid="`savings-ledger-decision-row-${slot.row.ts}`"
							:data-state="slot.state"
						>
							<td>{{ fmtDayTime(new Date(slot.row.ts)) }}</td>
							<td>{{ suggestedLabel(slot) }}</td>
							<td>{{ modeLabel(slot.row.appliedMode) }}</td>
							<td class="num" :class="{ 'text-danger': isCost(slot) }">
								{{ deltaText(slot) }}
							</td>
							<td
								:title="
									slot.row.healthOk
										? ''
										: $t('forecast.savingsLedger.decisions.healthShort')
								"
							>
								{{ slot.row.healthOk ? "" : "⚠" }}
							</td>
						</tr>
					</tbody>
				</table>
			</div>
			<p class="caption text-gray mt-2">
				{{ $t("forecast.savingsLedger.decisions.deltaNote") }}
			</p>
		</div>
	</div>
</template>

<script lang="ts">
import { defineComponent, type PropType } from "vue";
import formatter from "@/mixins/formatter";
// NOT ./chartMixin: that one is for the three horizontally-scrolling, mutually
// scroll-synced forecast charts. This ribbon is standalone and fits the card's width,
// like the waterfall above it, so it uses the plain echarts lifecycle mixin.
import echartsChart from "@/mixins/echartsChart";
import {
	FONT_FAMILY,
	tooltipStyle,
	tooltipTable,
	forecastXAxes,
	markPointLabel,
	type MarkPointLabelItem,
	type TooltipRow,
} from "./echarts";
import colors, { lighterColor, setAlpha } from "@/colors";
import escapeHtml from "@/utils/escapeHtml";
import SelectGroup from "../Helper/SelectGroup.vue";
import LegendList from "../Sessions/LegendList.vue";
import type { Legend } from "../Sessions/types";
import type { CURRENCY } from "@/types/evcc";
import type { LedgerDecisionRow } from "./savingsLedger.types";
import {
	decisionSlots,
	decisionSummary,
	deltaDirection,
	modesPresent,
	hasSuggestion,
	modeLabelKey,
	normalizeMode,
	reasonLabelKey,
	axisStepHours,
	laneRuns,
	moneyDirection,
	overrideRuns,
	badgedRuns,
	SLOT_MS,
	type DecisionSlot,
	type DeltaDirection,
	type LaneRun,
	type DecisionSummary,
} from "./savingsLedgerDecisions";

// Chart box, in px. Bound as an inline style rather than left in the stylesheet so the
// lane geometry below is derived from the same number the chart is drawn at.
const CHART_HEIGHT = 172;
// the top of the plotting area is left empty on purpose: the value badges sit above
// the ribbon, the way every other forecast chart labels its peak
const GRID_TOP = 6;
// two lines of x-axis label (hour, and weekday under midnight)
const GRID_BOTTOM = 42;
// room for the two lane names, which are drawn as graphic text left of the plot
const GRID_LEFT = 70;
const GRID_RIGHT = 8;

// Lane geometry as fractions of the y axis (0 at the bottom), stacked bottom-up:
// pedestal, applied lane, gutter, seam, gutter, suggested lane. They sum to less than
// 1 - the remainder is the gap above the top lane. The gutters are what keep the seam
// reading as a mark of its own rather than as a coloured edge of the lane above it.
const Y_MAX = 1;

// a contribution's effect on the bill as a direction rather than a sign - the same two
// glyphs SavingsLedgerWaterfall.vue labels its bars with
const GLYPH_DOWN = "↓";
const GLYPH_UP = "↑";

// the rendered width of a value badge, in px: 14px bold Montserrat plus
// markPointLabel's [5, 15] padding, measured on "↓ EUR 1.07"
const BADGE_WIDTH = 96;
const LANE_HEIGHT = 0.3;
const SEAM_GUTTER = 0.03;
const SEAM_HEIGHT = 0.08;
const APPLIED_Y0 = 0.02;
const APPLIED_Y1 = APPLIED_Y0 + LANE_HEIGHT;
const SEAM_Y0 = APPLIED_Y1 + SEAM_GUTTER;
const SEAM_Y1 = SEAM_Y0 + SEAM_HEIGHT;
const SUGGESTED_Y0 = SEAM_Y1 + SEAM_GUTTER;
const SUGGESTED_Y1 = SUGGESTED_Y0 + LANE_HEIGHT;

export default defineComponent({
	name: "SavingsLedgerDecisions",
	components: { SelectGroup, LegendList },
	mixins: [formatter, echartsChart],
	props: {
		decisions: { type: Array as PropType<LedgerDecisionRow[]>, default: () => [] },
		currency: { type: String as PropType<CURRENCY> },
	},
	data() {
		return {
			view: "timeline" as "timeline" | "table",
			// the rendered chart width, so badge spacing can be reasoned about in pixels;
			// seeded with the desktop card width until the chart reports its own
			chartPx: 900,
		};
	},
	computed: {
		chartHeight(): number {
			return CHART_HEIGHT;
		},
		slots(): DecisionSlot[] {
			return decisionSlots(this.decisions);
		},
		summary(): DecisionSummary {
			return decisionSummary(this.slots);
		},
		viewOptions() {
			return [
				{
					value: "timeline",
					name: this.$t("forecast.savingsLedger.decisions.timelineToggle"),
				},
				{ value: "table", name: this.$t("forecast.savingsLedger.decisions.tableToggle") },
			];
		},
		legends(): Legend[] {
			const t = (key: string) => this.$t(key) as string;
			const list: Legend[] = modesPresent(this.slots).map((mode) => ({
				label: t(modeLabelKey(mode) as string),
				color: this.modeColor(mode),
				value: "",
				type: "area" as const,
			}));

			// only claim a state the ribbon actually draws
			if (this.slots.some((s) => s.state === "unrecorded")) {
				list.push({
					label: t("forecast.savingsLedger.decisions.noSuggestion"),
					color: this.noModeColor,
					value: "",
					type: "area",
				});
			}
			for (const direction of ["saved", "cost", "neutral", "unknown"] as const) {
				if (
					!this.slots.some(
						(s) => s.state === "diverged" && this.direction(s) === direction
					)
				)
					continue;
				list.push({
					label: t(`forecast.savingsLedger.decisions.legendDiverged.${direction}`),
					color: this.seamColor(direction),
					value: "",
					type: "area",
				});
			}
			return list;
		},
		// One clause per fact, in the order a reader needs them: how much was recorded,
		// what the control loop did with it, and what was missing. Composed rather than
		// templated whole so a period with no suggestions doesn't print a sentence about
		// overrides that never existed.
		caption(): string {
			const s = this.summary;
			const clauses = [this.recordedSpan, this.plural("summarySlots", s.slots)];

			// every slot is accounted for: followed, overridden, or without a suggestion
			// at all. A clause that covers only part of the period next to a headline
			// count of the whole one is how a caption starts lying by omission.
			if (s.diverged > 0) clauses.push(this.plural("summaryDiverged", s.diverged));
			if (s.followed > 0) clauses.push(this.plural("summaryFollowed", s.followed));

			if (s.suggested < s.slots) {
				clauses.push(this.plural("summaryNoSuggestion", s.slots - s.suggested));
			}
			if (s.unhealthy > 0) {
				clauses.push(this.plural("summaryUnhealthy", s.unhealthy));
			}
			return clauses.join(" · ");
		},
		// The decisions can cover a shorter span than the period the card above is showing
		// - control_slots only goes back as far as the feature does. The ribbon's axis
		// carries times of day, not dates, so the span is named in words as well.
		recordedSpan(): string {
			const slots = this.slots;
			if (!slots.length) return "";
			const last = slots[slots.length - 1]!;
			return this.$t("forecast.savingsLedger.decisions.recordedSpan", {
				from: this.fmtDayTime(new Date(slots[0]!.row.ts)),
				to: this.fmtDayTime(new Date(last.tsMs + SLOT_MS)),
			}) as string;
		},
		// The money clause is split out of the caption so it can carry its own colour: an
		// override that cost money must not read in the same tone as one that saved it.
		netClause(): string {
			const s = this.summary;
			if (s.diverged === 0) return "";
			if (s.netDeltaEur === null) {
				return this.$t("forecast.savingsLedger.decisions.summaryNetUnknown") as string;
			}
			// the figure covers the PRICED overrides, which can be a subset of the
			// overrides the clause before it counted - so it names its own count rather
			// than borrowing that one (ADR-011 rule 4: report coverage, never imply it)
			const amount = this.fmtMoney(Math.abs(s.netDeltaEur), this.currency, true, true);
			const key = {
				cost: "summaryNetCost",
				saved: "summaryNetSaved",
				neutral: "summaryNetNeutral",
				unknown: "summaryNetUnknown",
			}[moneyDirection(s.netDeltaEur)];
			return this.plural(key, s.divergedPriced, { amount });
		},
		netClass(): string {
			return moneyDirection(this.summary.netDeltaEur) === "cost" ? "text-danger" : "";
		},
		ariaLabel(): string {
			const parts = [
				this.$t("forecast.savingsLedger.decisions.chartAria") as string,
				this.caption,
				this.netClause,
				this.$t("forecast.savingsLedger.decisions.chartAriaTable") as string,
			];
			return parts.filter(Boolean).join(". ");
		},
		noModeColor(): string {
			// faint enough to read as "nothing here" rather than as a fifth mode, but
			// still a band - a hole in the ribbon would read as missing rendering.
			return setAlpha(colors.muted, "40") || "";
		},
		chartOption(): Record<string, unknown> {
			const slots = this.slots;
			// echartsChart's deep watcher evaluates this getter regardless of the v-if
			// that gates the chart element, so an empty period must return an option
			// rather than index into nothing
			if (!slots.length) return {};
			const start = slots[0]!.tsMs;
			const end = slots[slots.length - 1]!.tsMs + SLOT_MS;

			return {
				animation: false,
				textStyle: { fontFamily: FONT_FAMILY },
				grid: {
					top: GRID_TOP,
					bottom: GRID_BOTTOM,
					left: GRID_LEFT,
					right: GRID_RIGHT,
					borderWidth: 0,
				},
				graphic: this.laneLabels(),
				tooltip: {
					trigger: "axis",
					axisPointer: {
						type: "line",
						lineStyle: { color: colors.muted || "", width: 1, type: "solid" },
					},
					// same box as every other forecast chart
					...tooltipStyle(colors.text || ""),
					formatter: (params: { dataIndex: number }[]) =>
						this.tooltipHtml(slots[params[0]?.dataIndex ?? -1]),
				},
				// hour labels plus the dashed day separators every other forecast chart
				// draws - the family resemblance is what makes this readable at a glance.
				xAxis: forecastXAxes(
					start,
					end,
					this.hourShort,
					this.weekdayShort,
					axisStepHours(end - start)
				),
				yAxis: {
					type: "value",
					min: 0,
					max: Y_MAX,
					show: false,
				},
				series: [
					{
						// The ribbon itself is drawn as markArea rectangles, one per RUN of
						// same-valued slots (laneRuns): a bar per slot leaves a sub-pixel gap
						// between neighbours that turns a day of one mode into a comb. This
						// series exists only to carry them and to give the axis tooltip one
						// data point per slot to snap to - it is invisible by design.
						name: "slots",
						type: "bar",
						barCategoryGap: "0%",
						itemStyle: { color: "transparent" },
						data: slots.map((slot) => [slot.tsMs + SLOT_MS / 2, Y_MAX]),
						markArea: {
							silent: true,
							animation: false,
							data: this.laneAreas(),
						},
						// clip:false - the badges sit in the headroom above the ribbon,
						// which markPointLabel's own clip:true would cut in half
						markPoint: {
							...markPointLabel("", this.badges(), new Date(start), new Date(end)),
							clip: false,
						},
					},
				],
			};
		},
	},
	watch: {
		// the chart element only exists in the timeline view; the mixin re-inits from
		// chartOption changes, which switching views does not produce
		view() {
			this.$nextTick(() => this.initChart());
		},
	},
	methods: {
		// Singular by key, not by vue-i18n's "a | b" plural form: $t(key, plural, opts)
		// takes OPTIONS third, not named values, so a plural message keeps {count} (the
		// plural machinery supplies it) and silently drops every other placeholder - that
		// is how "the 7 priced overrides saved within their own slots" lost its amount.
		// $tc, which does take named values, is not installed by this app's i18n build.
		// So: a "<key>One" message wins at count 1 where one exists, and $t(key, named)
		// stays the single interpolation path.
		plural(key: string, count: number, params: Record<string, unknown> = {}): string {
			const base = `forecast.savingsLedger.decisions.${key}`;
			const one = `${base}One`;
			return this.$t(count === 1 && this.$te(one) ? one : base, {
				count,
				...params,
			}) as string;
		},
		setView(value: string) {
			this.view = value === "table" ? "table" : "timeline";
		},
		onChartInit() {
			this.readChartWidth();
		},
		resize() {
			this.chart?.resize();
			this.readChartWidth();
		},
		readChartWidth() {
			const width = this.chart?.getWidth() ?? 0;
			if (width > 0) this.chartPx = width;
		},
		direction(slot: DecisionSlot) {
			return deltaDirection(slot.row);
		},
		isCost(slot: DecisionSlot): boolean {
			return slot.state === "diverged" && this.direction(slot) === "cost";
		},
		// Modes are coloured neutrally on purpose: green and red are reserved for the
		// seam, where they mean money. A mode is a state, not an outcome.
		modeColor(mode: string | undefined): string {
			if (mode == null) return this.noModeColor;
			switch (normalizeMode(mode)) {
				case "normal":
					return colors.muted || "";
				case "hold":
					return colors.temperature || "";
				case "charge":
					return colors.price || "";
				case "holdcharge":
					return lighterColor(colors.price) || "";
				default:
					return this.noModeColor;
			}
		},
		seamColor(direction: DeltaDirection): string {
			if (direction === "cost") return colors.danger || "";
			if (direction === "saved") return colors.self || "";
			// a computed wash and a missing figure are different facts, and neither is a
			// saving: solid grey for "made no difference", faded for "not priced"
			return setAlpha(colors.muted, direction === "neutral" ? "cc" : "66") || "";
		},
		// The house badge every other forecast chart puts on its peak, here on the
		// override runs that moved the most money. Direction, not sign: down took money
		// off the bill, up added to it - the same convention the waterfall uses, so
		// "EUR 1.07" on a green badge can never be read as a loss.
		badges(): MarkPointLabelItem[] {
			const slots = this.slots;
			if (!slots.length) return [];
			const start = slots[0]!.tsMs;
			const span = slots[slots.length - 1]!.tsMs + SLOT_MS - start;
			// a badge is BADGE_WIDTH px wide whatever the viewport, so the minimum
			// separation is that width converted into time - 12 % of a day is 108 px on a
			// desktop card and 37 px on a phone, and two badges overlap at the second
			const plotPx = Math.max(1, this.chartPx - GRID_LEFT - GRID_RIGHT);
			const minGapMs = (span * BADGE_WIDTH) / plotPx;
			return badgedRuns(overrideRuns(slots), minGapMs).map((run) => {
				const delta = run.deltaEur as number;
				const cost = moneyDirection(delta) === "cost";
				const money = this.fmtMoney(Math.abs(delta), this.currency, true, true);
				const mid = (run.start + run.end) / 2;
				const label: MarkPointLabelItem["label"] = {
					backgroundColor: cost ? colors.danger || "" : colors.self || "",
				};
				// markPointLabel nudges a badge in the first 5 % of the range right so the
				// canvas edge can't cut it; clip:false means the last 5 % needs the mirror
				if (mid > start + span * 0.95) label.offset = [-BADGE_WIDTH / 3, -2];
				return {
					coord: [mid, SUGGESTED_Y1] as [number, number],
					value: `${cost ? GLYPH_UP : GLYPH_DOWN} ${money}`,
					label,
				};
			});
		},
		// The three ribbons, as markArea rectangles in data coordinates: what was
		// applied, what was suggested, and the seam between them wherever the two
		// differ. Runs are merged, so a missing slot leaves a real gap and a day of one
		// mode is one rectangle rather than 96 abutting ones.
		laneAreas(): Record<string, unknown>[][] {
			const band = (
				runs: LaneRun<string>[],
				y0: number,
				y1: number
			): Record<string, unknown>[][] =>
				runs.map((run) => [
					{ xAxis: run.start, yAxis: y0, itemStyle: { color: run.value } },
					{ xAxis: run.end, yAxis: y1 },
				]);

			return [
				...band(
					laneRuns(this.slots, (slot) => this.modeColor(slot.row.appliedMode)),
					APPLIED_Y0,
					APPLIED_Y1
				),
				...band(
					laneRuns(this.slots, (slot) =>
						hasSuggestion(slot.row)
							? this.modeColor(slot.row.suggestedMode)
							: this.noModeColor
					),
					SUGGESTED_Y0,
					SUGGESTED_Y1
				),
				...band(
					laneRuns(this.slots, (slot) =>
						slot.state === "diverged" ? this.seamColor(this.direction(slot)) : null
					),
					SEAM_Y0,
					SEAM_Y1
				),
			];
		},
		// Drawn as graphic text rather than axis labels: the y axis carries fractions of
		// a lane stack, not two categories, so there is no tick to hang these on.
		laneLabels(): Record<string, unknown>[] {
			const plotHeight = CHART_HEIGHT - GRID_TOP - GRID_BOTTOM;
			const centre = (fraction: number) => GRID_TOP + plotHeight * (1 - fraction / Y_MAX) - 6;
			const label = (key: string, fraction: number) => ({
				type: "text",
				// left: 0 clipped the first glyph against the canvas edge
				left: 2,
				top: centre(fraction),
				silent: true,
				style: {
					text: this.$t(`forecast.savingsLedger.decisions.lane.${key}`) as string,
					fill: colors.muted || "",
					fontFamily: FONT_FAMILY,
					fontSize: 11,
					fontWeight: "bold",
				},
			});
			return [
				label("suggested", (SUGGESTED_Y0 + SUGGESTED_Y1) / 2),
				label("applied", (APPLIED_Y0 + APPLIED_Y1) / 2),
			];
		},
		suggestedLabel(slot: DecisionSlot): string {
			if (!hasSuggestion(slot.row)) {
				return this.$t("forecast.savingsLedger.decisions.noSuggestion") as string;
			}
			return this.modeLabel(slot.row.suggestedMode);
		},
		// An absent suggestion is the only absence the card can show: a stored applied
		// "unknown" means evcc held no override, which on a site with a battery is
		// normal operation, and normalizeMode folds it there before it is labelled.
		modeLabel(mode: string | undefined): string {
			const key = modeLabelKey(normalizeMode(mode));
			return key ? (this.$t(key) as string) : normalizeMode(mode);
		},
		reasonLabel(reason?: string): string {
			const key = reasonLabelKey(reason);
			return key ? (this.$t(key) as string) : (reason ?? "");
		},
		deltaText(slot: DecisionSlot): string {
			if (slot.state !== "diverged") return "—";
			if (slot.row.slotFlowDeltaEur == null) {
				return this.$t("forecast.savingsLedger.decisions.deltaUnknown") as string;
			}
			return this.fmtMoney(slot.row.slotFlowDeltaEur, this.currency, true, true);
		},
		tooltipHtml(slot: DecisionSlot | undefined): string {
			if (!slot) return "";
			const t = (key: string) =>
				escapeHtml(this.$t(`forecast.savingsLedger.decisions.${key}`) as string);
			const value = (text: string) => escapeHtml(text);
			const rows: TooltipRow[] = [
				{
					name: this.$t("forecast.savingsLedger.decisions.suggested") as string,
					values: [value(this.suggestedLabel(slot))],
				},
				{
					name: this.$t("forecast.savingsLedger.decisions.applied") as string,
					values: [value(this.modeLabel(slot.row.appliedMode))],
				},
			];

			// not gated on "diverged": a run that was vetoed before it produced a
			// suggestion leaves a reason on a row this card reads as unrecorded, and
			// dropping it would hide the only account of why nothing was suggested
			if (slot.row.vetoReason) {
				rows.push({
					name: this.$t("forecast.savingsLedger.decisions.reason") as string,
					values: [value(this.reasonLabel(slot.row.vetoReason))],
				});
			}
			if (slot.state === "diverged") {
				rows.push({
					name: this.$t("forecast.savingsLedger.decisions.delta") as string,
					values: [value(this.deltaText(slot))],
					total: true,
				});
			}
			if (!slot.row.healthOk) rows.push({ values: [t("healthShort")] });
			if (slot.row.modeChanged) rows.push({ values: [t("modeChangedShort")] });

			return tooltipTable(escapeHtml(this.fmtDayTime(new Date(slot.row.ts))), rows);
		},
	},
});
</script>

<style scoped>
.section-head {
	display: flex;
	justify-content: flex-end;
	margin-bottom: 0.5rem;
}

/* height is bound inline from CHART_HEIGHT - see its comment */
.timeline {
	width: 100%;
}

.tbl-scroll {
	max-height: 22rem;
	overflow-y: auto;
	overflow-x: auto;
}

.caption {
	font-size: 0.75rem;
	line-height: 1.4;
	margin-bottom: 0;
}

table.tbl {
	width: 100%;
	border-collapse: collapse;
	font-size: 0.78125rem;
}
table.tbl th,
table.tbl td {
	text-align: left;
	padding: 0.375rem 0.5rem;
	border-bottom: 1px solid var(--evcc-box-border);
	white-space: nowrap;
}
table.tbl th {
	position: sticky;
	top: 0;
	background: var(--evcc-box);
	font-size: 0.6875rem;
	text-transform: uppercase;
	letter-spacing: 0.06em;
	color: var(--evcc-gray);
	font-weight: 700;
}
table.tbl td.num {
	text-align: right;
	font-variant-numeric: tabular-nums;
}
</style>
