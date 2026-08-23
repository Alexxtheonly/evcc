<template>
	<div class="savings-ledger-decisions" data-testid="savings-ledger-decisions">
		<div class="section-head">
			<h4 class="section-title">{{ $t("forecast.savingsLedger.decisions.title") }}</h4>
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

					<template v-if="selected.row.appliedMode !== selected.row.suggestedMode">
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
					<div
						v-else
						class="dc-line text-muted small"
						data-testid="savings-ledger-decision-no-veto"
					>
						{{ $t("forecast.savingsLedger.decisions.noVeto") }}
					</div>

					<div class="dc-line">
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
									slot.row.appliedMode === slot.row.suggestedMode
										? "—"
										: modeLabel(slot.row.suggestedMode)
								}}
							</td>
							<td
								class="num"
								:class="{ 'text-loss': (slot.row.slotFlowDeltaEur ?? 0) > 0 }"
							>
								{{
									slot.row.slotFlowDeltaEur != null
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
import {
	decisionSlots,
	unhealthyVetoCount,
	modeLabelKey,
	reasonLabelKey,
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
		isSelected(slot: DecisionSlot): boolean {
			return this.selected?.row.ts === slot.row.ts;
		},
		// D5: a tick's meaning (steady / vetoed-cost / vetoed-saved / vetoed-unknown) was
		// carried only by height and colour, with :title exposing nothing but a
		// timestamp - the accessible name. This builds the same distinction the visual
		// legend shows, plus the euro delta where one exists, as the button's real name.
		tickAriaLabel(slot: DecisionSlot): string {
			const time = this.fmtDayTime(new Date(slot.row.ts));
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
		modeLabel(mode: string): string {
			const key = modeLabelKey(mode);
			return key ? (this.$t(key) as string) : mode;
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
	justify-content: space-between;
	align-items: baseline;
	gap: 0.5rem;
	margin-top: 1.25rem;
}
.section-title {
	font-size: 0.9375rem;
	font-weight: 700;
	margin: 0;
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
.tick--vetoed-saved {
	height: 24px;
	background: var(--evcc-darker-green);
}
.tick--vetoed-cost {
	height: 24px;
	background: var(--evcc-red);
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
