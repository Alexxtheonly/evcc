<template>
	<Card edge-to-edge class="box-pull-out mb-4" data-testid="savings-ledger-card">
		<!-- Card's :title prop renders through a hardcoded text-truncate/no-wrap span
		     (Helper/Card.vue) - fine for the app's usual short titles ("Solar Production"),
		     but it silently ellipsised this card's longer, deliberately narrative title
		     ("What the system did with your money") at 390px, even with nothing else in
		     the header row. A plain heading in the body wraps normally instead. -->
		<h3 class="ledger-title" data-testid="savings-ledger-title">
			{{ $t("forecast.savingsLedger.title") }}
			<!-- the diagram's caveats (what each step means, what is estimated and on what
			     basis, every note the API sent) are one tap away rather than a wall of
			     text under the chart. A modal, not a bootstrap tooltip: the content does
			     not fit a hover tooltip on a phone. -->
			<span
				class="value-icon info-icon"
				role="button"
				tabindex="0"
				:aria-label="$t('forecast.savingsLedger.info.title')"
				data-testid="savings-ledger-info-icon"
				@click="openInfo"
				@keydown.enter.prevent="openInfo"
				@keydown.space.prevent="openInfo"
			>
				<shopicon-regular-info size="s"></shopicon-regular-info>
			</span>
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
			<SavingsLedgerWaterfall
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

			<div class="row gx-2 mt-1" data-testid="savings-ledger-details">
				<div
					v-for="detail in details"
					:key="detail.key"
					:class="[detail.colClass, `text-${detail.align}`]"
				>
					<small>
						<span class="text-gray">{{ detail.label }}</span>
						<br />
						<span
							class="fw-bold"
							:class="detail.valueClass"
							:data-testid="`savings-ledger-detail-${detail.key}`"
							>{{ detail.value }}</span
						>
					</small>
				</div>
			</div>

			<!-- ADR-011 rule 3 (coverage visible without interaction) and rule 7 (the
			     estimate marker is on the figures, not hidden behind a click) in one line. -->
			<p class="caption text-gray" data-testid="savings-ledger-caption">{{ caption }}</p>
		</div>

		<SavingsLedgerInfoModal
			ref="infoModal"
			:chain="ledger?.chain"
			:notes="notes"
			:chain-coverage-pct="chainCoveragePct"
		/>
	</Card>
</template>

<script lang="ts">
import "@h2d2/shopicons/es/regular/info";
import { defineComponent, type PropType } from "vue";
import formatter from "@/mixins/formatter";
import api from "@/api";
import type { CURRENCY } from "@/types/evcc";
import Card from "../Helper/Card.vue";
import SelectGroup from "../Helper/SelectGroup.vue";
import DateNavigatorButton from "../Sessions/DateNavigatorButton.vue";
import SavingsLedgerWaterfall from "./SavingsLedgerWaterfall.vue";
import SavingsLedgerInfoModal from "./SavingsLedgerInfoModal.vue";
import type { SavingsLedger, SavingsLedgerErrorBody } from "./savingsLedger.types";
import {
	defaultWindow,
	shiftWindow,
	isWindowAtPresent,
	pickSettled,
	coverageDivergence,
	clampWindowToEarliest,
	type LedgerWindow,
	type SettlementHeadline,
} from "./savingsLedgerChain";
import { waterfallLayout, type WaterfallLayout } from "./savingsLedgerWaterfall";

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

interface LedgerDetail {
	key: string;
	label: string;
	value: string;
	align: "start" | "center" | "end";
	colClass: string;
	valueClass: string;
}

export default defineComponent({
	name: "SavingsLedgerCard",
	components: {
		Card,
		SelectGroup,
		DateNavigatorButton,
		SavingsLedgerWaterfall,
		SavingsLedgerInfoModal,
	},
	mixins: [formatter],
	props: {
		currency: { type: String as PropType<CURRENCY> },
	},
	// the per-slot decisions strip is its own card in Forecast.vue, but its data comes
	// from this card's single GET /api/savingsledger response - emitted upward rather
	// than fetched a second time, so a period change is still exactly one request.
	emits: ["update:decisions"],
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
			// guards the auto-clamp in fetch() (see its doc comment) to at most one retry
			// per explicit navigation - reset to false everywhere this.win is reassigned
			// by an explicit user action (page(), jumpToPresent(), the $route.query
			// watcher), so a genuinely-empty clamped period doesn't retry forever, but a
			// fresh navigation always gets one clamp attempt of its own.
			hasAutoClamped: false,
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
		waterfall(): WaterfallLayout | null {
			return this.ledger?.chain ? waterfallLayout(this.ledger.chain, this.headline) : null;
		},
		// The strip under the diagram, house pattern (SolarDetails/ValueDetails): label,
		// break, bold coloured value. Without a chain there is no baseline to compare
		// against, so only the one measured figure is shown - never a fabricated pair.
		details(): LedgerDetail[] {
			if (!this.ledger) return [];
			const wf = this.waterfall;
			const paid = wf ? wf.paid : pickSettled(this.ledger.realised.settled, this.headline);
			const money = (v: number) => this.fmtMoney(v, this.currency, true, true);
			const colClass = wf ? "col-4" : "col-12";

			const items: LedgerDetail[] = [];
			if (wf) {
				items.push({
					key: "baseline",
					label: this.$t("forecast.savingsLedger.details.wouldHaveCost") as string,
					value: money(wf.w0),
					align: "start",
					colClass,
					valueClass: "text-primary",
				});
			}
			items.push({
				key: "paid",
				label: this.$t("forecast.savingsLedger.details.youPaid") as string,
				value: money(paid),
				align: wf ? "center" : "start",
				colClass,
				valueClass: "text-primary",
			});
			if (wf) {
				// ADR-011 rule 1: a period that came out worse than the baseline says so.
				// The sign is carried by the label ("cost you") and the danger colour, and
				// the magnitude is never clamped at zero.
				const loss = wf.saved < 0;
				const pct =
					wf.savedFraction != null
						? ` (${this.fmtPercentage(Math.abs(wf.savedFraction) * 100, 0)})`
						: "";
				items.push({
					key: "saved",
					label: this.$t(
						`forecast.savingsLedger.details.${loss ? "cost" : "saved"}`
					) as string,
					value: `${money(Math.abs(wf.saved))}${pct}`,
					align: "end",
					colClass,
					valueClass: loss ? "text-danger" : "text-primary",
				});
			}
			return items;
		},
		chainCoverageDivergence() {
			if (!this.ledger?.chain) return null;
			return coverageDivergence(this.ledger.realised.coverage, this.ledger.chain.coverage);
		},
		chainCoveragePct(): string {
			const divergence = this.chainCoverageDivergence;
			return divergence ? this.fmtPercentage(divergence.chainFraction * 100, 1) : "";
		},
		caption(): string {
			if (!this.ledger) return "";
			const parts: string[] = [];
			if (this.ledger.chain?.batteryPhysics) {
				parts.push(this.$t("forecast.savingsLedger.estimatedCaption") as string);
			}
			parts.push(
				this.$t("forecast.savingsLedger.coverageShort", {
					pct: this.fmtPercentage(this.ledger.realised.coverage.fraction * 100, 1),
					valid: this.ledger.realised.coverage.validSlots,
					total: this.ledger.realised.coverage.totalSlots,
				}) as string
			);
			if (this.chainCoveragePct) {
				parts.push(
					this.$t("forecast.savingsLedger.coverageChainDivergesShort", {
						pct: this.chainCoveragePct,
					}) as string
				);
			}
			return parts.join(" · ");
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
		// hand the decisions rows to Forecast.vue, which mounts them as their own card.
		// Watching `ledger` rather than emitting from fetch() keeps the fetch/refusal
		// layer below untouched: a refusal or an error nulls `ledger`, which emits null
		// and takes the decisions card down with it.
		ledger(value: SavingsLedger | null) {
			this.$emit("update:decisions", value ? value.decisions : null);
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
					this.hasAutoClamped = false;
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
		openInfo() {
			(
				this.$refs["infoModal"] as InstanceType<typeof SavingsLedgerInfoModal> | undefined
			)?.open();
		},
		setHeadline(value: string | number | boolean | null) {
			this.headline = value === "perSlot" ? "perSlot" : "periodAverage";
		},
		page(dir: 1 | -1) {
			this.hasAutoClamped = false;
			this.win = shiftWindow(this.win, dir, new Date());
		},
		jumpToPresent() {
			this.hasAutoClamped = false;
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
					const body = res.data as SavingsLedgerErrorBody | undefined;

					// Auto-narrow ONLY the default/present view (isAtPresent) - a user who
					// explicitly paged into the past and hits a genuine "before any data
					// exists" refusal must see the honest refusal, never get silently
					// redirected to a period they didn't ask for. Guarded to at most one
					// attempt per explicit navigation (hasAutoClamped, reset by
					// page()/jumpToPresent()/the $route.query watcher) so a clamp that
					// still can't produce a usable window doesn't retry forever.
					if (this.isAtPresent && !this.hasAutoClamped && body?.earliest) {
						const clamped = clampWindowToEarliest(this.win, body.earliest);
						if (clamped) {
							this.hasAutoClamped = true;
							// don't also set refusal/ledger here: the deep watcher on win
							// (below) fires fetch() again for the clamped window on its
							// own - calling fetch() a second time here would double-request.
							this.win = clamped;
							return;
						}
					}

					this.ledger = null;
					this.refusal = body?.error || res.statusText;
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
.info-icon {
	cursor: help;
	display: inline-flex;
	vertical-align: -0.3rem;
	color: var(--evcc-gray);
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
.caption {
	font-size: 0.75rem;
	margin: 0.5rem 0 0;
}
.chain-unavailable {
	margin-top: 1rem;
	font-size: 0.8125rem;
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
