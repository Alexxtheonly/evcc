<template>
	<Card edge-to-edge class="box-pull-out mb-4" data-testid="savings-ledger-card">
		<!-- Card's :title prop renders through a hardcoded text-truncate/no-wrap span
		     (Helper/Card.vue) - fine for the app's usual short titles ("Solar Production"),
		     but it silently ellipsised this card's longer, deliberately narrative title
		     ("What the system did with your money") at 390px, even with nothing else in
		     the header row. A plain heading in the body wraps normally instead. -->
		<h3 class="ledger-title" data-testid="savings-ledger-title">
			{{
				titleParts.head
			}}<!-- the last word and the icon travel together: at 390px the title wraps,
			     and left to itself the icon dropped onto a line of its own below it. --><span
				class="title-tail"
				>{{
					titleParts.tail
				}}<!-- the diagram's caveats (what each step means, what is estimated and
				     on what basis, every note the API sent) are one tap away rather than a
				     wall of text under the chart. A modal, not a bootstrap tooltip: the
				     content does not fit a hover tooltip on a phone. --><span
					class="info-icon"
					role="button"
					tabindex="0"
					:aria-label="$t('forecast.savingsLedger.info.title')"
					data-testid="savings-ledger-info-icon"
					@click="openInfo"
					@keydown.enter.prevent="openInfo"
					@keydown.space.prevent="openInfo"
				>
					<shopicon-regular-info size="s"></shopicon-regular-info> </span
			></span>
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

			<!-- D5: at 390px "would have cost" wraps to two lines while "you paid" and
			     "saved" don't, and with the house <br/> markup that pushed the first
			     column's figure a line below the other two - three parallel figures that
			     no longer read as a row. Each column is a full-height flex column instead,
			     its label allowed to grow, so the values sit on one line whatever wraps. -->
			<div class="row gx-2 mt-1" data-testid="savings-ledger-details">
				<div
					v-for="detail in details"
					:key="detail.key"
					:class="[detail.colClass, `text-${detail.align}`]"
				>
					<small class="detail">
						<span class="text-gray detail-label">{{ detail.label }}</span>
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
			     estimate marker is on the figures, not hidden behind a click) in one line.
			     The overspend clause is appended here rather than raised into a banner:
			     the strip above can legitimately read "saved 91 %" while Control itself
			     lost money, and nothing else on the card names that. -->
			<p class="caption text-gray" data-testid="savings-ledger-caption">
				{{ caption
				}}<span
					v-if="controlOverspendClause"
					class="text-danger"
					data-testid="savings-ledger-control-overspend"
				>
					· {{ controlOverspendClause }}</span
				>
			</p>
			<!-- ADR-011 rule 7: a measure that cannot be honestly attributed is named in
			     one line under the chart, not left to a modal. -->
			<p
				v-if="evTimingCaption"
				class="caption caption-next text-gray"
				data-testid="savings-ledger-ev-timing"
			>
				{{ evTimingCaption }}
			</p>
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
	isControlOverspend,
	type LedgerWindow,
	type SettlementHeadline,
} from "./savingsLedgerChain";
import { waterfallLayout, type WaterfallLayout } from "./savingsLedgerWaterfall";

// The one chain note whose subject matter ADR-011 rule 7 puts under the chart rather than
// behind the info control: a measure that exists but cannot be attributed. Matched on its
// stable opening rather than rendered verbatim (core/metrics/ledger_worlds.go's
// noteEVTimingUnattributed, emitted only when the site actually has a loadpoint) so the
// line appears exactly when it applies and never claims something about a site with no EV.
// The full note itself still renders in the modal, deduped with the rest.
const EV_TIMING_NOTE_PREFIX = "EV charge timing is not attributed";

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
			// monotonic request counter - see fetch(). Only the newest request may write
			// ledger/refusal/loadError/loading, so two overlapping fetches (the date
			// navigator clicked twice in quick succession) cannot resolve out of order and
			// leave period A's euros rendered under period B's periodLabel.
			fetchSeq: 0,
		};
	},
	computed: {
		// the title's last word, split off so it can carry the info icon on its own line
		// without the icon ever being orphaned - see the template.
		titleParts(): { head: string; tail: string } {
			const title = this.$t("forecast.savingsLedger.title") as string;
			const cut = title.lastIndexOf(" ");
			if (cut < 0) return { head: "", tail: title };
			return { head: title.slice(0, cut + 1), tail: title.slice(cut + 1) };
		},
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
		// ADR-011 rule 1, restated for the case the three-figure strip cannot express: on a
		// period where solar and the battery saved a great deal, the strip legitimately
		// reads "saved 91 %" while the Control step itself LOST money against its own
		// baseline. Without this clause the only trace of that is the colour of one bar.
		controlOverspendClause(): string {
			const chain = this.ledger?.chain;
			if (!chain || !isControlOverspend(chain, this.headline)) return "";
			const control = chain.contributions.find((c) => c.label === "Control");
			if (!control) return "";
			const eur = pickSettled(control.settled, this.headline);
			return this.$t("forecast.savingsLedger.controlOverspendShort", {
				// magnitude: the direction is carried by the wording ("cost") and the
				// danger colour, never by a bare minus sign
				amount: this.fmtMoney(Math.abs(eur), this.currency, true, true),
			}) as string;
		},
		// ADR-011 rule 7: rendered only when the API actually sent the EV-timing note (see
		// EV_TIMING_NOTE_PREFIX), so a site without a loadpoint is not told about a
		// non-attribution that cannot affect it.
		evTimingCaption(): string {
			const notes = this.ledger?.chain?.notes ?? [];
			if (!notes.some((n) => n.startsWith(EV_TIMING_NOTE_PREFIX))) return "";
			return this.$t("forecast.savingsLedger.evTimingShort") as string;
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
		// The `ledger` watcher above only fires once a response has landed, so between the
		// click and that response the chart area showed "Loading…" under the NEW period's
		// label while the decisions card below still showed the PREVIOUS period's ticks
		// and euro deltas - with no period label of its own to contradict them. Taking it
		// down for the duration of the fetch restores what the strip did when it still
		// lived inside this card's v-else-if="ledger" block, and (because Forecast.vue
		// mounts it under v-if) also resets SavingsLedgerDecisions' internal timeline/table
		// toggle, which used to survive a period change.
		loading(value: boolean) {
			if (value) this.$emit("update:decisions", null);
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
			// Every write below is gated on this request still being the newest one. Without
			// it, paging twice quickly leaves both requests in flight; the second one's
			// `loading = true` is a no-op (already true), so nothing re-hides the content,
			// and whichever response lands last wins - which for ordinary HTTP can be the
			// FIRST period's. The card then shows period A's chain, and the decisions card
			// period A's rows, under period B's header, and nothing ever corrects it.
			const seq = ++this.fetchSeq;
			const current = () => seq === this.fetchSeq;
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
				// a superseded request must not touch state, and must not clamp the window
				// out from under the newer one
				if (!current()) return;
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
				console.error("failed to load savings ledger", e);
				if (!current()) return;
				this.ledger = null;
				this.loadError = e instanceof Error ? e.message : String(e);
			} finally {
				// a superseded request leaves loading alone: the newer one owns it and is
				// still running, so clearing it here would drop the loading state early and
				// briefly show the previous period's content under the new period label
				if (current()) this.loading = false;
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
.title-tail {
	white-space: nowrap;
}
.detail {
	display: flex;
	flex-direction: column;
	height: 100%;
}
.detail-label {
	flex: 1 1 auto;
}
.caption {
	font-size: 0.75rem;
	margin: 0.5rem 0 0;
}
.caption-next {
	margin-top: 0.125rem;
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
