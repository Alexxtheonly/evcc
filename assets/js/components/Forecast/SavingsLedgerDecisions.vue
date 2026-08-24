<template>
	<div class="savings-ledger-decisions" data-testid="savings-ledger-decisions">
		<!-- no heading of its own: mounted as its own Card in views/Forecast.vue, whose
		     header renders decisions.title -->
		<div class="section-head">
			<!-- the card above is headed with the requested PERIOD, but only slots with a
			     recorded control decision appear here. Naming the span the ticks actually
			     cover beats padding the strip out with fabricated slots. -->
			<p
				v-if="recordedSpan"
				class="span-note text-muted small mb-0"
				data-testid="savings-ledger-decisions-span"
			>
				{{ recordedSpan }}
			</p>
			<button
				v-if="slots.length"
				type="button"
				class="btn btn-sm btn-link p-0"
				data-testid="savings-ledger-decisions-table-toggle"
				@click="showTable = !showTable"
			>
				{{
					showTable
						? $t("forecast.savingsLedger.decisions.timelineToggle")
						: $t("forecast.savingsLedger.decisions.tableToggle")
				}}
			</button>
		</div>

		<p
			v-if="slots.length === 0"
			class="text-muted small"
			data-testid="savings-ledger-decisions-empty"
		>
			{{ $t("forecast.savingsLedger.decisions.empty") }}
		</p>

		<template v-else>
			<p
				v-if="unhealthyCount > 0"
				class="text-muted small"
				data-testid="savings-ledger-decisions-unhealthy"
			>
				{{
					$t("forecast.savingsLedger.decisions.unhealthyCount", { count: unhealthyCount })
				}}
			</p>

			<div v-if="!showTable" data-testid="savings-ledger-decisions-timeline-view">
				<div class="forecast-chart-scroll">
					<div class="timeline-strip" data-testid="savings-ledger-decisions-timeline">
						<button
							v-for="slot in slots"
							:key="slot.row.ts"
							type="button"
							class="tick"
							:class="[
								`tick--${slot.outcome}`,
								{ 'tick--selected': isSelected(slot) },
							]"
							:data-testid="`savings-ledger-decision-slot-${slot.row.ts}`"
							:data-outcome="slot.outcome"
							:title="fmtDayTime(new Date(slot.row.ts))"
							:aria-label="tickAriaLabel(slot)"
							:aria-pressed="isSelected(slot)"
							@click="selectedTs = slot.row.ts"
						></button>
					</div>
				</div>

				<div class="legend" data-testid="savings-ledger-decisions-legend">
					<span
						><i class="lg tick--steady"></i
						>{{ $t("forecast.savingsLedger.decisions.noVeto") }}</span
					>
					<span
						><i class="lg tick--vetoed-saved"></i
						>{{ $t("forecast.savingsLedger.decisions.legend.deltaSaved") }}</span
					>
					<span
						><i class="lg tick--vetoed-cost"></i
						>{{ $t("forecast.savingsLedger.decisions.legend.deltaCost") }}</span
					>
					<span
						><i class="lg tick--vetoed-unknown"></i
						>{{ $t("forecast.savingsLedger.decisions.deltaUnknown") }}</span
					>
					<span
						><i class="lg tick--no-suggestion"></i
						>{{ $t("forecast.savingsLedger.decisions.outcome.noSuggestion") }}</span
					>
				</div>

				<div
					v-if="selected"
					class="decision-detail"
					data-testid="savings-ledger-decision-detail"
				>
					<div class="dc-time" data-testid="savings-ledger-decision-time">
						{{ fmtDayTime(new Date(selected.row.ts)) }}
					</div>

					<div class="dc-line">
						<div class="dc-k">{{ $t("forecast.savingsLedger.decisions.applied") }}</div>
						<div class="dc-v" data-testid="savings-ledger-decision-applied">
							{{ modeLabel(selected.row.appliedMode) }}
						</div>
					</div>

					<!-- "did the controller override the suggestion" is the outcome, not a raw
					     string comparison: a legacy row carrying "unknown" against "normal" is
					     the same mode twice, not a veto. -->
					<template v-if="isVeto(selected.outcome)">
						<div class="dc-line">
							<div class="dc-k">
								{{ $t("forecast.savingsLedger.decisions.suggested") }}
							</div>
							<div class="dc-v" data-testid="savings-ledger-decision-suggested">
								{{ modeLabel(selected.row.suggestedMode) }}
							</div>
						</div>
						<div v-if="selected.row.vetoReason" class="dc-line">
							<div class="dc-k">
								{{ $t("forecast.savingsLedger.decisions.reason") }}
							</div>
							<div class="dc-v" data-testid="savings-ledger-decision-reason">
								{{ reasonLabel(selected.row.vetoReason) }}
							</div>
						</div>
					</template>
					<!-- "the optimizer said nothing" is not "the optimizer agreed", and this
					     panel is the one that reads as authoritative -->
					<div
						v-else-if="selected.outcome === 'no-suggestion'"
						class="dc-line text-muted small"
						data-testid="savings-ledger-decision-no-suggestion"
					>
						{{ $t("forecast.savingsLedger.decisions.outcome.noSuggestion") }}
					</div>
					<div
						v-else
						class="dc-line text-muted small"
						data-testid="savings-ledger-decision-no-veto"
					>
						{{ $t("forecast.savingsLedger.decisions.noVeto") }}
					</div>

					<!-- the slot-local delta prices a veto against the suggestion it rejected,
					     so on a steady slot there is nothing for it to measure and printing
					     one fabricates "the veto was worth EUR 0.00". Shown only where a veto
					     exists; absent-but-vetoed says so in its own words below. -->
					<div v-if="isVeto(selected.outcome)" class="dc-line">
						<div class="dc-k">{{ $t("forecast.savingsLedger.decisions.delta") }}</div>
						<div class="dc-v" data-testid="savings-ledger-decision-delta">
							<span
								v-if="selected.row.slotFlowDeltaEur != null"
								:class="{ 'text-danger': selected.row.slotFlowDeltaEur > 0 }"
								data-testid="savings-ledger-decision-delta-value"
								>{{
									fmtMoney(selected.row.slotFlowDeltaEur, currency, true, true)
								}}</span
							>
							<span
								v-else
								class="text-muted"
								data-testid="savings-ledger-decision-delta-unknown"
								>{{ $t("forecast.savingsLedger.decisions.deltaUnknown") }}</span
							>
							<small class="d-block text-muted">{{
								$t("forecast.savingsLedger.decisions.deltaNote")
							}}</small>
						</div>
					</div>

					<div
						v-if="!selected.row.healthOk"
						class="dc-flag"
						data-testid="savings-ledger-decision-health"
					>
						{{ $t("forecast.savingsLedger.decisions.health") }}
					</div>
					<div
						v-if="selected.row.modeChanged"
						class="dc-flag"
						data-testid="savings-ledger-decision-mode-changed"
					>
						{{ $t("forecast.savingsLedger.decisions.modeChanged") }}
					</div>
				</div>
			</div>

			<div
				v-else
				class="forecast-chart-scroll"
				data-testid="savings-ledger-decisions-table-wrap"
			>
				<table class="tbl" data-testid="savings-ledger-decisions-table">
					<thead>
						<tr>
							<th>{{ $t("forecast.savingsLedger.decisions.table.time") }}</th>
							<th>{{ $t("forecast.savingsLedger.decisions.table.applied") }}</th>
							<th>{{ $t("forecast.savingsLedger.decisions.table.suggested") }}</th>
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
							:data-outcome="slot.outcome"
						>
							<td>{{ fmtDayTime(new Date(slot.row.ts)) }}</td>
							<td>{{ modeLabel(slot.row.appliedMode) }}</td>
							<td>{{ suggestedLabel(slot) }}</td>
							<td
								class="num"
								:class="{ 'text-danger': slot.outcome === 'vetoed-cost' }"
							>
								{{ deltaLabel(slot) }}
							</td>
							<td>{{ slot.row.healthOk ? "" : "⚠" }}</td>
						</tr>
					</tbody>
				</table>
				<p class="footnote small text-muted">
					{{ $t("forecast.savingsLedger.decisions.deltaNote") }}
				</p>
			</div>
		</template>
	</div>
</template>

<script lang="ts">
import { defineComponent, type PropType } from "vue";
import formatter from "@/mixins/formatter";
import type { CURRENCY } from "@/types/evcc";
import type { LedgerDecisionRow } from "./savingsLedger.types";
import { SLOT_MINUTES } from "./savingsLedgerChain";
import {
	decisionSlots,
	isVeto,
	unhealthyVetoCount,
	modeLabelKey,
	reasonLabelKey,
	type DecisionOutcome,
	type DecisionSlot,
} from "./savingsLedgerDecisions";

// the table's placeholder for a cell with nothing to say
const EMPTY = "—";

export default defineComponent({
	name: "SavingsLedgerDecisions",
	mixins: [formatter],
	props: {
		decisions: { type: Array as PropType<LedgerDecisionRow[]>, default: () => [] },
		currency: { type: String as PropType<CURRENCY> },
	},
	data() {
		return { selectedTs: null as string | null, showTable: false };
	},
	computed: {
		slots(): DecisionSlot[] {
			return decisionSlots(this.decisions);
		},
		unhealthyCount(): number {
			return unhealthyVetoCount(this.decisions);
		},
		// first slot start to last slot END (a row is a slot start, so the span runs one
		// slot past it): the real extent of the strip, not the card's period
		recordedSpan(): string {
			if (!this.slots.length) return "";
			const first = this.slots[0]!;
			const last = this.slots[this.slots.length - 1]!;
			return this.$t("forecast.savingsLedger.decisions.recordedSpan", {
				from: this.fmtDayTime(new Date(first.tsMs)),
				to: this.fmtDayTime(new Date(last.tsMs + SLOT_MINUTES * 60 * 1000)),
			}) as string;
		},
		// defaults to the most recent slot so the detail panel is never empty when there
		// is data to show
		selected(): DecisionSlot | null {
			if (this.selectedTs) {
				const found = this.slots.find((s) => s.row.ts === this.selectedTs);
				if (found) return found;
			}
			return this.slots.length ? this.slots[this.slots.length - 1]! : null;
		},
	},
	methods: {
		isVeto(outcome: DecisionOutcome): boolean {
			return isVeto(outcome);
		},
		isSelected(slot: DecisionSlot): boolean {
			return this.selected?.row.ts === slot.row.ts;
		},
		// a tick's meaning is carried visually by height and colour, and :title exposes
		// only a timestamp. This builds the same distinction the legend shows, plus the
		// euro delta where one exists, as the button's accessible name.
		tickAriaLabel(slot: DecisionSlot): string {
			const time = this.fmtDayTime(new Date(slot.row.ts));
			if (slot.outcome === "no-suggestion") {
				return `${time}, ${this.$t("forecast.savingsLedger.decisions.outcome.noSuggestion")}`;
			}
			if (slot.outcome === "steady") {
				return `${time}, ${this.$t("forecast.savingsLedger.decisions.outcome.steady")}`;
			}
			if (slot.outcome === "vetoed-unknown") {
				return `${time}, ${this.$t("forecast.savingsLedger.decisions.outcome.vetoedUnknown")}`;
			}
			const delta = this.fmtMoney(slot.row.slotFlowDeltaEur ?? 0, this.currency, true, true);
			const key = slot.outcome === "vetoed-cost" ? "vetoedCost" : "vetoedSaved";
			return `${time}, ${this.$t(`forecast.savingsLedger.decisions.outcome.${key}`, { delta })}`;
		},
		// an absent/"unknown" mode is folded to normal by modeLabelKey's normalizeMode, so
		// the wire word never reaches the screen. A genuinely unrecognised mode is shown
		// verbatim rather than disguised as something it isn't.
		modeLabel(mode?: string): string {
			const key = modeLabelKey(mode ?? "");
			return key ? (this.$t(key) as string) : (mode ?? "");
		},
		reasonLabel(reason?: string): string {
			const key = reasonLabelKey(reason);
			return key ? (this.$t(key) as string) : (reason ?? "");
		},
		// "the optimizer said nothing" and "the optimizer agreed" are different facts and
		// neither is a rejected mode, so neither may print one.
		suggestedLabel(slot: DecisionSlot): string {
			if (slot.outcome === "no-suggestion") {
				return this.$t("forecast.savingsLedger.decisions.outcome.noSuggestion") as string;
			}
			if (slot.outcome === "steady") return EMPTY;
			return this.modeLabel(slot.row.suggestedMode);
		},
		// the slot-local delta prices a veto against the suggestion it rejected, so a
		// steady slot has nothing for it to measure. Keyed on the outcome, not on nullness:
		// a legacy row carrying applied "unknown" against suggested "normal" IS steady, the
		// same mode spelled two ways.
		deltaLabel(slot: DecisionSlot): string {
			if (!isVeto(slot.outcome) || slot.row.slotFlowDeltaEur == null) return EMPTY;
			return this.fmtMoney(slot.row.slotFlowDeltaEur, this.currency, true, true);
		},
	},
});
</script>

<style scoped>
.section-head {
	display: flex;
	flex-wrap: wrap;
	justify-content: space-between;
	align-items: baseline;
	gap: 0.5rem;
}
.span-note {
	margin-right: auto;
}

.timeline-strip {
	display: flex;
	align-items: flex-end;
	gap: 1px;
	height: 28px;
	padding-bottom: 2px;
}
.tick {
	flex: 0 0 5px;
	width: 5px;
	height: 8px;
	border: 0;
	padding: 0;
	border-radius: 1px;
	background: var(--evcc-box-border);
	cursor: pointer;
}
/* "nothing was vetoed" is two thirds of a typical strip, and .tick's own
   --evcc-box-border IS the card background in dark mode. Plain grey at a third of a
   vetoed bar's height: present in both themes, subordinate by size and saturation. */
.tick--steady {
	background: var(--evcc-gray);
}
.tick--vetoed-saved {
	height: 24px;
	background: var(--evcc-darker-green);
}
.tick--vetoed-cost {
	height: 24px;
	background: var(--evcc-red);
}
/* "no optimizer run produced a suggestion" is its own state, not a quieter kind of
   agreement: hollow, so it reads as present-but-empty rather than as a veto or a match.
   Hollow alone does not survive this size - a 1px ring on a 5x8 box is a slightly paler
   solid block next to .tick--steady - so the height also drops to 3px, putting it on the
   channel the strip already uses for significance (24px veto, 8px steady). The legend
   swatch is unaffected: .legend .lg's height outranks this rule, so it keeps the ring. */
.tick--no-suggestion {
	height: 3px;
	background: transparent;
	box-shadow: inset 0 0 0 1px var(--evcc-gray);
}
.tick--vetoed-unknown {
	height: 24px;
	background-image: repeating-linear-gradient(45deg, var(--evcc-gray) 0 2px, transparent 2px 4px);
}
.tick--selected {
	outline: 2px solid var(--evcc-default-text);
	outline-offset: 1px;
}

.legend {
	display: flex;
	flex-wrap: wrap;
	gap: 0.375rem 1rem;
	font-size: 0.71875rem;
	color: var(--evcc-gray);
	margin-top: 0.5rem;
}
.legend .lg {
	display: inline-block;
	width: 14px;
	height: 9px;
	border-radius: 2px;
	margin-right: 4px;
	vertical-align: -1px;
}

.decision-detail {
	margin-top: 0.75rem;
	background: var(--evcc-box);
	border: 1px solid var(--evcc-box-border);
	border-radius: 14px;
	padding: 0.875rem 0.9375rem;
}
.dc-time {
	font-weight: 700;
	font-size: 0.875rem;
}
.dc-line {
	margin-top: 0.625rem;
}
.dc-k {
	font-size: 0.6875rem;
	letter-spacing: 0.07em;
	text-transform: uppercase;
	color: var(--evcc-gray);
	font-weight: 700;
}
.dc-v {
	font-size: 0.8125rem;
	margin-top: 2px;
	line-height: 1.5;
}
.dc-flag {
	margin-top: 0.5rem;
	font-size: 0.75rem;
	color: var(--evcc-gray);
}

table.tbl {
	width: 100%;
	border-collapse: collapse;
	font-size: 0.78125rem;
	margin-top: 0.5rem;
}
table.tbl th,
table.tbl td {
	text-align: left;
	padding: 0.375rem 0.5rem;
	border-bottom: 1px solid var(--evcc-box-border);
	white-space: nowrap;
}
table.tbl th {
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
.footnote {
	margin-top: 0.5rem;
}
</style>
