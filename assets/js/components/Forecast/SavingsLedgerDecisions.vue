<template>
	<div class="savings-ledger-decisions" data-testid="savings-ledger-decisions">
		<!-- no heading of its own: this strip is mounted as its own Card in
		     views/Forecast.vue, whose header renders decisions.title. -->
		<div class="section-head">
			<!-- D3: the card above this one is headed with the requested PERIOD, but only
			     slots with a recorded control decision appear here - typically the last
			     few hours of a multi-day period. Naming the span the ticks actually cover
			     is the honest alternative to padding the strip out with fabricated slots. -->
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
						>{{ $t("forecast.savingsLedger.decisions.table.delta") }} ≤ 0</span
					>
					<span
						><i class="lg tick--vetoed-cost"></i
						>{{ $t("forecast.savingsLedger.decisions.table.delta") }} &gt; 0</span
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

					<!-- D6: "did the controller override the suggestion" is the outcome, not
					     a raw string comparison - a legacy row carrying "unknown" against
					     "normal" is the same mode twice, not a veto worth a row of its own. -->
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
					<!-- D3: "the optimizer said nothing" is not "the optimizer agreed" - the
					     same conflation N4 removed from the tick and the table cell, left
					     behind in the panel that reads as authoritative. 35 of 59 rows on
					     this site's own strip were no-suggestion rows, every one of them
					     told "Nothing was vetoed in this slot." -->
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

					<!-- D6: the slot-local delta prices a veto against the suggestion it
					     rejected, so on a steady slot there is nothing for it to measure.
					     It used to render regardless, printing whatever the backend put in
					     slotFlowDeltaEur - and while applied/suggested differed only as
					     strings ("unknown" vs "normal") that was a fabricated "the veto was
					     worth EUR 0.00" on every such slot. Shown only where a veto exists;
					     absent-but-vetoed still says so, in its own words, below. -->
					<div v-if="isVeto(selected.outcome)" class="dc-line">
						<div class="dc-k">{{ $t("forecast.savingsLedger.decisions.delta") }}</div>
						<div class="dc-v" data-testid="savings-ledger-decision-delta">
							<span
								v-if="selected.row.slotFlowDeltaEur != null"
								:class="{ 'text-loss': selected.row.slotFlowDeltaEur > 0 }"
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
							<td>
								{{
									slot.outcome === "no-suggestion"
										? $t(
												"forecast.savingsLedger.decisions.outcome.noSuggestion"
											)
										: slot.outcome === "steady"
											? "—"
											: modeLabel(slot.row.suggestedMode)
								}}
							</td>
							<!-- D6, same rule as the detail panel above: the delta prices a veto
							     against the suggestion it rejected, so a steady slot has nothing for
							     it to measure and must not print one. Keyed on the outcome, not on
							     nullness - a legacy row carrying applied "unknown" against suggested
							     "normal" IS steady (the same mode spelled two ways), and used to
							     render "€0.00" here beside a suggested column already saying
							     "—": a priced veto next to "there was no veto". -->
							<td
								class="num"
								:class="{ 'text-loss': slot.outcome === 'vetoed-cost' }"
							>
								{{
									isVeto(slot.outcome) && slot.row.slotFlowDeltaEur != null
										? fmtMoney(slot.row.slotFlowDeltaEur, currency, true, true)
										: "—"
								}}
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
		// D3: first slot start to last slot END (a row is a slot start, so the span runs
		// one slot past it) - the real extent of the strip, not the card's period.
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
		// D5: a tick's meaning (steady / vetoed-cost / vetoed-saved / vetoed-unknown) was
		// carried only by height and colour, with :title exposing nothing but a
		// timestamp - the accessible name. This builds the same distinction the visual
		// legend shows, plus the euro delta where one exists, as the button's real name.
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
		// D6: an absent/"unknown" mode is folded to normal by modeLabelKey's own
		// normalizeMode - the wire word never reaches the screen. A genuinely
		// unrecognised mode (one this UI predates) is still shown verbatim rather than
		// disguised as something it isn't.
		modeLabel(mode?: string): string {
			const key = modeLabelKey(mode ?? "");
			return key ? (this.$t(key) as string) : (mode ?? "");
		},
		reasonLabel(reason?: string): string {
			const key = reasonLabelKey(reason);
			return key ? (this.$t(key) as string) : (reason ?? "");
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
/* D1: "nothing was vetoed" is two thirds of a typical strip, and it used to inherit
   .tick's --evcc-box-border - which in dark mode IS the card background (#151630) and in
   light mode is the page grey on a white card. 35 of 52 slots therefore rendered as
   nothing at all, and the legend's own swatch was blank too because no .tick--steady rule
   existed anywhere. Plain grey, at a third of a vetoed bar's height: clearly present in
   both themes, and subordinate to the green/red ones by both size and saturation. */
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
/* N4: "no optimizer run produced a suggestion for this slot" is its own state, not a
   quieter kind of agreement - a hollow tick, so it reads as present-but-empty rather than
   as either a veto or a match.
   D8: hollow alone did not survive the size it renders at. A 1px inset ring on a 5x8 box
   leaves a 3x6 hole, which measured 54% of .tick--steady's ink with only 18 of 40 pixels
   differing - a slightly paler solid block, on 59% of this site's strip. Shortening it to
   3px puts it on the channel this strip already uses for significance (24px veto, 8px
   steady) and takes it to 28% of the ink with 30 of 40 pixels differing. The legend
   swatch is unaffected: .legend .lg's own height outranks this rule, so the swatch stays
   14x9 and keeps the hollow ring that distinguishes it there. */
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
.text-loss {
	color: var(--evcc-red);
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
