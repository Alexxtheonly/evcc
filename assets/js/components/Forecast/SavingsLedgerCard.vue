<template>
	<Card edge-to-edge class="box-pull-out mb-4" data-testid="savings-ledger-card">
		<!-- Card's :title prop renders through a hardcoded text-truncate/no-wrap span
		     (Helper/Card.vue) - fine for the app's usual short titles ("Solar Production"),
		     but it silently ellipsised this card's longer, deliberately narrative title
		     ("What the system did with your money") at 390px, even with nothing else in
		     the header row. A plain heading in the body wraps normally instead. -->
		<h3 class="ledger-title" data-testid="savings-ledger-title">
			{{ $t("forecast.savingsLedger.title") }}
		</h3>
		<div class="toolbar" data-testid="savings-ledger-toolbar">
			<div class="d-flex align-items-center gap-1" data-testid="savings-ledger-period-nav">
				<DateNavigatorButton
					prev
					:disabled="false"
					:on-click="() => page(-1)"
					data-testid="savings-ledger-period-prev"
				/>
				<button
					type="button"
					class="btn btn-sm border-0 text-truncate period-label keyboard-focus-ring"
					data-testid="savings-ledger-period-label"
					:title="$t('forecast.savingsLedger.periodJumpToPresent')"
					@click="jumpToPresent"
				>
					{{ periodLabel }}
				</button>
				<DateNavigatorButton
					next
					:disabled="isAtPresent"
					:on-click="() => page(1)"
					data-testid="savings-ledger-period-next"
				/>
			</div>
			<SelectGroup
				id="savingsLedgerHeadline"
				:options="headlineOptions"
				:model-value="headline"
				data-testid="savings-ledger-headline"
				@update:model-value="setHeadline"
			/>
		</div>

		<div v-if="loading" class="text-muted py-4" data-testid="savings-ledger-loading">
			{{ $t("forecast.savingsLedger.loading") }}
		</div>

		<div v-else-if="refusal" class="refuse py-2" data-testid="savings-ledger-refuse">
			<p class="refuse-title" data-testid="savings-ledger-refuse-title">
				{{ $t("forecast.savingsLedger.refuseTitle") }}
			</p>
			<p class="refuse-body text-muted" data-testid="savings-ledger-refuse-body">
				{{ refusal }}
			</p>
		</div>

		<div v-else-if="loadError" class="text-muted py-4" data-testid="savings-ledger-error">
			{{ loadError }}
		</div>

		<div v-else-if="ledger" data-testid="savings-ledger-content">
			<p class="coverage" data-testid="savings-ledger-coverage">
				{{
					$t("forecast.savingsLedger.coverage", {
						pct: fmtPercentage(ledger.realised.coverage.fraction * 100, 1),
						valid: ledger.realised.coverage.validSlots,
						total: ledger.realised.coverage.totalSlots,
					})
				}}
				<template v-if="chainCoverageDivergence">
					{{
						$t("forecast.savingsLedger.coverageChainDiverges", {
							pct: fmtPercentage(chainCoverageDivergence.chainFraction * 100, 1),
						})
					}}
				</template>
			</p>

			<div class="hero">
				<div class="hero-label">
					{{ $t("forecast.savingsLedger.heroLabel", { headline: headlineLabel }) }}
				</div>
				<div class="hero-value" data-testid="savings-ledger-hero-value">
					{{ fmtMoney(heroEur, currency, true, true) }}
				</div>
				<div class="hero-sub text-muted" data-testid="savings-ledger-hero-sub">
					{{ ledger.realised.note }}
					{{
						$t("forecast.savingsLedger.headlineFootnote", {
							other: fmtMoney(otherHeroEur, currency, true, true),
							otherLabel: otherHeadlineLabel,
						})
					}}
				</div>
			</div>

			<div v-if="isLoss" class="loss-banner" data-testid="savings-ledger-loss-banner">
				{{ $t("forecast.savingsLedger.lossBanner") }}
			</div>

			<SavingsLedgerChain
				v-if="ledger.chain"
				:chain="ledger.chain"
				:headline="headline"
				:currency="currency"
			/>
			<p
				v-else-if="ledger.chainUnavailable"
				class="chain-unavailable text-muted"
				data-testid="savings-ledger-chain-unavailable"
			>
				{{
					$t("forecast.savingsLedger.chainUnavailable", {
						reason: ledger.chainUnavailable,
					})
				}}
			</p>

			<div v-if="notes.length" class="notes" data-testid="savings-ledger-notes">
				<p class="notes-title">{{ $t("forecast.savingsLedger.notesTitle") }}</p>
				<ul class="notes-list">
					<li
						v-for="(note, i) in notes"
						:key="i"
						:data-testid="`savings-ledger-note-${i}`"
					>
						{{ note }}
					</li>
				</ul>
			</div>

			<SavingsLedgerDecisions :decisions="ledger.decisions" :currency="currency" />
		</div>
	</Card>
</template>

<script lang="ts">
import { defineComponent, type PropType } from "vue";
import formatter from "@/mixins/formatter";
import api from "@/api";
import type { CURRENCY } from "@/types/evcc";
import Card from "../Helper/Card.vue";
import SelectGroup from "../Helper/SelectGroup.vue";
import DateNavigatorButton from "../Sessions/DateNavigatorButton.vue";
import SavingsLedgerChain from "./SavingsLedgerChain.vue";
import SavingsLedgerDecisions from "./SavingsLedgerDecisions.vue";
import type { SavingsLedger, SavingsLedgerErrorBody } from "./savingsLedger.types";
import {
	defaultWindow,
	shiftWindow,
	isWindowAtPresent,
	pickSettled,
	isControlOverspend,
	coverageDivergence,
	type LedgerWindow,
	type SettlementHeadline,
} from "./savingsLedgerChain";

// ?from=&to= (RFC3339) seeds/reseeds the period, deliberately NOT aligned or clamped
// the way paging (page()/jumpToPresent()) always is - this is the sanctioned way to
// reach a request shape normal UI interaction never produces (an unaligned slot
// boundary, an over-long range, a period before the tariffs table starts) for
// verification, without hand-editing any code. Returns null (leave the current window
// untouched) when from/to aren't both present and valid, so callers no-op on any other
// query-string change. A module-level function, not a component method, so data()
// below can call it before `this` has a Methods type to call into (Vue's data()/
// methods() typings are mutually circular otherwise).
function windowFromQuery(query: Record<string, unknown> | undefined): LedgerWindow | null {
	const q = query ?? {};
	const fromRaw = typeof q["from"] === "string" ? (q["from"] as string) : undefined;
	const toRaw = typeof q["to"] === "string" ? (q["to"] as string) : undefined;
	if (!fromRaw || !toRaw) return null;
	const from = new Date(fromRaw);
	const to = new Date(toRaw);
	if (Number.isNaN(from.getTime()) || Number.isNaN(to.getTime())) {
		console.warn("savings ledger: ignoring invalid ?from=/?to= query parameters");
		return null;
	}
	return { from, to };
}

export default defineComponent({
	name: "SavingsLedgerCard",
	components: {
		Card,
		SelectGroup,
		DateNavigatorButton,
		SavingsLedgerChain,
		SavingsLedgerDecisions,
	},
	mixins: [formatter],
	props: {
		currency: { type: String as PropType<CURRENCY> },
	},
	data() {
		return {
			// seeded from ?from=/?to= (see windowFromQuery above) when present so the
			// deep watcher below doesn't fire during creation - a reassignment here
			// would duplicate the fetch mounted() already issues (see the D2 fix note
			// on the "$route.query" watcher).
			win: windowFromQuery(this.$route?.query) ?? (defaultWindow(new Date()) as LedgerWindow),
			headline: "periodAverage" as SettlementHeadline,
			loading: false,
			ledger: null as SavingsLedger | null,
			refusal: null as string | null,
			loadError: null as string | null,
		};
	},
	computed: {
		headlineOptions() {
			return [
				{
					value: "periodAverage",
					name: this.$t("forecast.savingsLedger.headline.periodAverage"),
				},
				{ value: "perSlot", name: this.$t("forecast.savingsLedger.headline.perSlot") },
			];
		},
		headlineLabel(): string {
			return this.$t(`forecast.savingsLedger.headline.${this.headline}`) as string;
		},
		otherHeadline(): SettlementHeadline {
			return this.headline === "perSlot" ? "periodAverage" : "perSlot";
		},
		otherHeadlineLabel(): string {
			return this.$t(`forecast.savingsLedger.headline.${this.otherHeadline}`) as string;
		},
		isAtPresent(): boolean {
			return isWindowAtPresent(this.win, new Date());
		},
		periodLabel(): string {
			const fmt = new Intl.DateTimeFormat(this.$i18n?.locale, {
				day: "numeric",
				month: "short",
			});
			const to = new Date(this.win.to.getTime() - 1);
			return `${fmt.format(this.win.from)} – ${fmt.format(to)}`;
		},
		heroEur(): number {
			return this.ledger ? pickSettled(this.ledger.realised.settled, this.headline) : 0;
		},
		otherHeroEur(): number {
			return this.ledger ? pickSettled(this.ledger.realised.settled, this.otherHeadline) : 0;
		},
		isLoss(): boolean {
			return this.ledger?.chain
				? isControlOverspend(this.ledger.chain, this.headline)
				: false;
		},
		chainCoverageDivergence() {
			if (!this.ledger?.chain) return null;
			return coverageDivergence(this.ledger.realised.coverage, this.ledger.chain.coverage);
		},
		// ADR-011 rule 7: every caveat the API sends must be rendered, never dropped -
		// realised.note is always present, chain.notes only when the chain computed.
		// realised.note and chain.notes[0] are both noteInvoiceComparability
		// (core/metrics/ledger_worlds.go/ledger_settlement.go) - deduped so the same
		// sentence never renders twice, not a sign either side dropped anything.
		notes(): string[] {
			if (!this.ledger) return [];
			const all = [this.ledger.realised.note, ...(this.ledger.chain?.notes ?? [])];
			return [...new Set(all)];
		},
	},
	watch: {
		win: {
			handler() {
				this.fetch();
			},
			deep: true,
		},
		// D2: a hash-fragment-only URL change (e.g. following a shared link, or
		// browser back/forward over one) is a same-document navigation - the component
		// is never remounted, so without this watcher the once-only query parse below
		// used to be the only time the URL was ever consulted, and the card kept
		// showing the previous period's figures while periodLabel (driven straight off
		// win) moved on. Mirrors what page()/jumpToPresent() already do: replace
		// this.win wholesale and let the watcher above issue the fetch.
		"$route.query": {
			handler(query: Record<string, unknown>) {
				const win = windowFromQuery(query);
				// guard on actual value change, not just object identity: other query
				// params on this route (or vue-router handing back a fresh object on an
				// unrelated push) must not re-trigger a fetch for the same period.
				if (
					win &&
					(win.from.getTime() !== this.win.from.getTime() ||
						win.to.getTime() !== this.win.to.getTime())
				) {
					this.win = win;
				}
			},
		},
	},
	mounted() {
		// the single initial fetch. win is already correct by the time we get here -
		// seeded from the query in data() below - so this doesn't race the deep win
		// watcher above. It used to: created() reassigned win a second time here,
		// which fired that watcher's fetch() AND this one, so every URL-seeded load
		// issued two identical requests.
		this.fetch();
	},
	methods: {
		setHeadline(value: string | number | boolean | null) {
			this.headline = value === "perSlot" ? "perSlot" : "periodAverage";
		},
		page(dir: 1 | -1) {
			this.win = shiftWindow(this.win, dir, new Date());
		},
		jumpToPresent() {
			this.win = defaultWindow(new Date());
		},
		async fetch() {
			this.loading = true;
			this.refusal = null;
			this.loadError = null;
			try {
				const res = await api.get<SavingsLedger | SavingsLedgerErrorBody>("savingsledger", {
					params: { from: this.win.from.toISOString(), to: this.win.to.toISOString() },
					// 400/422 are expected refusals (ADR-011 rule 4), not app errors - render
					// them inline instead of letting api.ts's interceptor raise a toast.
					validateStatus: (status: number) => [200, 400, 422].includes(status),
				});
				if (res.status === 200) {
					this.ledger = res.data as SavingsLedger;
				} else {
					this.ledger = null;
					this.refusal = (res.data as SavingsLedgerErrorBody)?.error || res.statusText;
				}
			} catch (e) {
				this.ledger = null;
				this.loadError = e instanceof Error ? e.message : String(e);
				console.error("failed to load savings ledger", e);
			} finally {
				this.loading = false;
			}
		},
	},
});
</script>

<style scoped>
/* matches .evcc-card-title (Helper/Card.vue) minus its text-truncate/no-wrap - see the
   template comment on why this card doesn't use Card's :title prop */
.ledger-title {
	font-size: 1.25rem;
	font-weight: 400;
	line-height: 1.5rem;
	margin: 0;
	color: var(--evcc-default-text);
}
.toolbar {
	display: flex;
	align-items: center;
	justify-content: space-between;
	flex-wrap: wrap;
	gap: 0.5rem;
	margin-top: 0.5rem;
}
.period-label {
	color: inherit;
	max-width: 10em;
}
.coverage {
	font-size: 0.75rem;
	color: var(--evcc-gray);
	margin: 0.25rem 0 0;
}
.hero {
	margin: 1rem 0 0.25rem;
}
.hero-label {
	font-size: 0.75rem;
	color: var(--evcc-gray);
	margin-bottom: 2px;
}
.hero-value {
	font-size: 2.75rem;
	font-weight: 700;
	line-height: 1;
	letter-spacing: -0.025em;
}
.hero-sub {
	font-size: 0.8125rem;
	margin-top: 0.5rem;
	max-width: 52ch;
}
.loss-banner {
	display: flex;
	gap: 0.5rem;
	align-items: flex-start;
	margin-top: 0.75rem;
	background: color-mix(in srgb, var(--evcc-red) 12%, transparent);
	border: 1px solid color-mix(in srgb, var(--evcc-red) 35%, transparent);
	border-radius: 12px;
	padding: 0.625rem 0.75rem;
	font-size: 0.78125rem;
	line-height: 1.5;
}
.chain-unavailable {
	margin-top: 1rem;
	font-size: 0.8125rem;
}
.notes {
	margin-top: 1rem;
	padding: 0.6875rem 0.8125rem;
	border-radius: 12px;
	background: var(--evcc-box);
	border: 1px solid var(--evcc-box-border);
}
.notes-title {
	font-size: 0.6875rem;
	letter-spacing: 0.06em;
	text-transform: uppercase;
	font-weight: 700;
	color: var(--evcc-gray);
	margin: 0 0 0.375rem;
}
.notes-list {
	margin: 0;
	padding-left: 1.1rem;
	font-size: 0.75rem;
	color: var(--evcc-gray);
	line-height: 1.5;
}
.notes-list li + li {
	margin-top: 0.25rem;
}
.refuse-title {
	font-weight: 700;
	font-size: 0.875rem;
	margin-top: 0.5rem;
}
.refuse-body {
	font-size: 0.8125rem;
	margin-top: 0.375rem;
	line-height: 1.55;
}
</style>
