<template>
	<Card edge-to-edge class="box-pull-out mb-4" data-testid="savings-ledger-card">
		<!-- not Card's :title prop: that renders through a hardcoded text-truncate span,
		     which ellipsises this card's longer title at 390px. A plain heading wraps. -->
		<h3 class="evcc-card-title fw-normal m-0" data-testid="savings-ledger-title">
			{{ title }}<!-- a non-breaking space keeps the icon on the last word's line at
			     390px in every language, which splitting the translated title on its last
			     space did not. -->&nbsp;<span
				class="info-icon"
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

			<!-- labels and values are two passes over the same list, so .row's own wrap puts
			     every label on one grid line and every value on the next: the figures stay
			     aligned when a label OR a value wraps. Bottom-aligning them in a full-height
			     flex column only holds while every value is one line tall. -->
			<div class="row gx-2 mt-1" data-testid="savings-ledger-details">
				<div
					v-for="detail in details"
					:key="detail.key"
					:class="[detail.colClass, `text-${detail.align}`]"
				>
					<small class="text-gray">{{ detail.label }}</small>
				</div>
				<div
					v-for="detail in details"
					:key="`value-${detail.key}`"
					:class="[detail.colClass, `text-${detail.align}`]"
				>
					<small
						class="fw-bold"
						:class="detail.valueClass"
						:data-testid="`savings-ledger-detail-${detail.key}`"
						>{{ detail.value }}</small
					>
				</div>
			</div>

			<!-- coverage and the estimate marker belong on the figures, not behind a click.
			     The strip above is entirely the chain's, over the chain's slots: when those
			     are materially fewer than the period's, name what the period actually cost
			     rather than letting "you paid" be read as the total. -->
			<p
				v-if="diagramSubsetWarning"
				class="caption text-warning"
				data-testid="savings-ledger-diagram-subset"
			>
				{{ diagramSubsetWarning }}
			</p>
			<p class="caption text-gray" data-testid="savings-ledger-caption">
				{{ caption
				}}<span
					v-if="controlOverspendClause"
					class="text-danger"
					data-testid="savings-ledger-control-overspend"
				>
					· {{ controlOverspendClause }}</span
				><!-- same slot, deliberately NOT the danger colour: a figure inside the
				     measurement noise is not a loss, and colouring it like one is the claim
				     this clause exists to withdraw. --><span
					v-if="controlNoiseClause"
					data-testid="savings-ledger-control-inside-noise"
				>
					· {{ controlNoiseClause }}</span
				>
			</p>
			<!-- a measure that cannot be honestly attributed is named under the chart, in one
			     line, rather than left to a modal. -->
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
	EV_TIMING_NOTE,
	type SettlementHeadline,
} from "./savingsLedgerChain";
import { waterfallLayout, type WaterfallLayout } from "./savingsLedgerWaterfall";

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
	// the decisions strip is its own card in Forecast.vue but shares this card's single
	// response, emitted upward rather than fetched again: one request per period change.
	emits: ["update:decisions"],
	data() {
		return {
			win: defaultWindow(new Date()),
			headline: "periodAverage" as SettlementHeadline,
			loading: false,
			ledger: null as SavingsLedger | null,
			refusal: null as string | null,
			loadError: null as string | null,
			// one retry each per explicit navigation, reset wherever this.win is
			// reassigned by a user action, so an empty clamped period cannot retry
			// forever.
			//
			// SEPARATE flags, deliberately: the two clamps can be SEQUENTIAL (a window
			// starting before both the tariff history and the battery), and sharing one
			// flag lets the first clamp consume the only attempt so the second never
			// runs. They cannot loop into each other - clampWindowToEarliest strictly
			// advances `from` and returns null when it cannot.
			hasClampedTariff: false,
			hasClampedChain: false,
			// monotonic request counter: only the newest request may write
			// ledger/refusal/loadError/loading, so two overlapping fetches cannot resolve
			// out of order and leave period A's euros under period B's label.
			fetchSeq: 0,
		};
	},
	computed: {
		title(): string {
			return this.$t("forecast.savingsLedger.title") as string;
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
		// Without a chain there is no baseline to compare against, so only the one measured
		// figure is shown, never a fabricated pair.
		details(): LedgerDetail[] {
			if (!this.ledger) return [];
			const wf = this.waterfall;
			const paid = wf ? wf.paid : pickSettled(this.ledger.realised.settled, this.headline);
			const colClass = wf ? "col-4" : "col-12";

			const items: LedgerDetail[] = [];
			if (wf) {
				items.push({
					key: "baseline",
					label: this.$t("forecast.savingsLedger.details.wouldHaveCost") as string,
					value: this.money(wf.w0),
					align: "start",
					colClass,
					valueClass: "text-primary",
				});
			}
			items.push({
				key: "paid",
				label: this.$t("forecast.savingsLedger.details.youPaid") as string,
				value: this.money(paid),
				align: wf ? "center" : "start",
				colClass,
				valueClass: "text-primary",
			});
			if (wf) {
				// a period that came out worse than the baseline says so: the sign is carried
				// by the label and the danger colour, the magnitude is never clamped at zero.
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
					value: `${this.money(Math.abs(wf.saved))}${pct}`,
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
		// Coverage, in the order the figures above it come from. Every number in the
		// waterfall and the strip is the CHAIN's, so where the two diverge the chain's has
		// to come first: the realised one reads as reassurance for figures it never covered.
		caption(): string {
			if (!this.ledger) return "";
			const parts: string[] = [];
			if (this.ledger.chain?.batteryPhysics) {
				parts.push(this.$t("forecast.savingsLedger.estimatedCaption") as string);
			}
			const chain = this.ledger.chain?.coverage;
			if (this.chainCoveragePct && chain) {
				parts.push(
					this.$t("forecast.savingsLedger.coverageDiagramShort", {
						pct: this.chainCoveragePct,
						valid: chain.validSlots,
						total: chain.totalSlots,
					}) as string
				);
			}
			parts.push(
				this.$t("forecast.savingsLedger.coverageShort", {
					pct: this.fmtPercentage(this.ledger.realised.coverage.fraction * 100, 1),
					valid: this.ledger.realised.coverage.validSlots,
					total: this.ledger.realised.coverage.totalSlots,
				}) as string
			);
			return parts.join(" · ");
		},
		// When the diagram covers materially fewer slots than the period, "you paid" above
		// it is the chain's W3 over its own subset, not the period's bill. Named here rather
		// than implied wrongly above.
		diagramSubsetWarning(): string {
			const divergence = this.chainCoverageDivergence;
			// MATERIALLY fewer: coverageDivergence reports any difference at all, and a
			// single dropped read is not a reason for a standing warning whose two euro
			// figures differ by cents. Two percentage points of the period is the line;
			// below it the two are the same statement, and the caption carries both anyway.
			if (
				!this.ledger?.chain ||
				!divergence ||
				divergence.headlineFraction - divergence.chainFraction <= 0.02
			) {
				return "";
			}
			return this.$t("forecast.savingsLedger.diagramSubsetWarning", {
				pct: this.chainCoveragePct,
				amount: this.fmtMoney(
					pickSettled(this.ledger.realised.settled, this.headline),
					this.currency,
					true,
					true
				),
			}) as string;
		},
		// the case the three-figure strip cannot express: where solar and the battery saved
		// a great deal the strip legitimately reads "saved 91 %" while the Control step
		// itself LOST money. Without this clause the only trace is the colour of one bar.
		controlOverspendClause(): string {
			const control = this.waterfall?.control;
			if (!control?.overspend) return "";
			return this.$t("forecast.savingsLedger.controlOverspendShort", {
				// magnitude only: the direction is carried by the wording and the danger
				// colour, never by a bare minus sign
				amount: this.money(Math.abs(control.eur)),
			}) as string;
		},
		// the counterpart to the clause above, for when it must NOT fire. A Control figure
		// inside the period's own noise floor has a magnitude but no usable direction, and a
		// false "the controller cost you money" is this card's most expensive wrong answer.
		// Still drawn and printed, in gray, never in the danger colour.
		controlNoiseClause(): string {
			const wf = this.waterfall;
			if (!wf?.control?.insideNoise) return "";
			return this.$t("forecast.savingsLedger.controlInsideNoiseShort", {
				amount: this.money(Math.abs(wf.control.eur)),
				band: this.money(wf.band),
			}) as string;
		},
		// rendered only when the API actually sent the EV-timing note, so a site without a
		// loadpoint is not told about a non-attribution that cannot affect it.
		evTimingCaption(): string {
			const notes = this.ledger?.chain?.notes ?? [];
			if (!notes.includes(EV_TIMING_NOTE)) return "";
			return this.$t("forecast.savingsLedger.evTimingShort") as string;
		},
		// Every caveat the API sends must be rendered, never dropped. realised.note and
		// chain.notes[0] open with the same invoice caveat but are not string-equal, so a
		// Set-based dedupe renders it twice; deduped by containment instead, realised.note
		// first so the longer form is the one kept whole. Deliberately no separator or
		// sentence splitting: that makes the backend's punctuation decide what renders.
		notes(): string[] {
			if (!this.ledger) return [];
			const all = [this.ledger.realised.note, ...(this.ledger.chain?.notes ?? [])];
			const kept: string[] = [];
			for (const note of all) {
				if (kept.some((k) => k.startsWith(note))) continue;
				kept.push(note);
			}
			return kept;
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
		// Watching `ledger` rather than emitting from fetch() means a refusal or an error
		// nulls it, emits null, and takes that card down with it.
		ledger(value: SavingsLedger | null) {
			// chain.batteryPhysics is present exactly when the site has a battery.
			// control_slots is written whenever the optimizer runs, battery or not, so a
			// PV-and-loadpoint site records rows too, but every one of them is
			// "unknown"/"unknown": a battery mode for a battery that does not exist. No
			// decisions to replay, so the card does not appear.
			//
			// An absent chain is deliberately NOT the same case: that is chainUnavailable,
			// a site that HAS a battery whose physics could not be derived. Its rows are
			// real decisions, worth showing with every delta honestly nil.
			const batteryLess = !!value?.chain && !value.chain.batteryPhysics;
			this.$emit("update:decisions", value && !batteryLess ? value.decisions : null);
		},
		// The `ledger` watcher above only fires once a response has landed, so without this
		// the decisions card keeps the PREVIOUS period's ticks and euro deltas under the new
		// period's label, with no label of its own to contradict them.
		loading(value: boolean) {
			if (value) this.$emit("update:decisions", null);
		},
	},
	mounted() {
		// data() has already put the default window in place, so this does not race the
		// deep win watcher above
		this.fetch();
	},
	methods: {
		money(v: number): string {
			return this.fmtMoney(v, this.currency, true, true);
		},
		openInfo() {
			(
				this.$refs["infoModal"] as InstanceType<typeof SavingsLedgerInfoModal> | undefined
			)?.open();
		},
		setHeadline(value: string | number | boolean | null) {
			this.headline = value === "perSlot" ? "perSlot" : "periodAverage";
		},
		page(dir: 1 | -1) {
			this.hasClampedTariff = this.hasClampedChain = false;
			this.win = shiftWindow(this.win, dir, new Date());
		},
		jumpToPresent() {
			this.hasClampedTariff = this.hasClampedChain = false;
			this.win = defaultWindow(new Date());
		},
		async fetch() {
			// Every write below is gated on this request still being the newest one. Paging
			// twice quickly leaves both in flight, and without the gate whichever response
			// lands last wins - which can be the first period's, under the second's header.
			const seq = ++this.fetchSeq;
			const current = () => seq === this.fetchSeq;
			this.loading = true;
			this.refusal = null;
			this.loadError = null;
			try {
				const res = await api.get<SavingsLedger | SavingsLedgerErrorBody>("savingsledger", {
					params: { from: this.win.from.toISOString(), to: this.win.to.toISOString() },
					// 400/422 are expected refusals rather than app errors: render them inline
					// instead of letting api.ts's interceptor raise a toast
					validateStatus: (status: number) => [200, 400, 422].includes(status),
				});
				// a superseded request must not touch state, and must not clamp the window
				// out from under the newer one
				if (!current()) return;
				if (res.status === 200) {
					const ledger = res.data as SavingsLedger;

					// the request succeeded, but the chain may only be computable over part
					// of the window and every figure this card draws comes from the chain.
					// Narrow the DEFAULT view to where the diagram can be drawn, under the
					// same guards the refusal clamp below uses. When narrowing would not
					// honestly help the divergent period renders as-is, with
					// diagramSubsetWarning naming the real figure.
					if (this.isAtPresent && !this.hasClampedChain && ledger.chainEarliest) {
						const clamped = clampWindowToEarliest(this.win, ledger.chainEarliest);
						if (clamped) {
							this.hasClampedChain = true;
							this.win = clamped;
							return;
						}
					}

					this.ledger = ledger;
				} else {
					const body = res.data as SavingsLedgerErrorBody | undefined;

					// Auto-narrow ONLY the default/present view: a user who explicitly paged
					// into the past and hit a genuine "before any data exists" refusal must
					// see that refusal, never a silent redirect to a period they did not ask
					// for. One attempt per navigation, so it cannot retry forever.
					if (this.isAtPresent && !this.hasClampedTariff && body?.earliest) {
						const clamped = clampWindowToEarliest(this.win, body.earliest);
						if (clamped) {
							this.hasClampedTariff = true;
							// don't also set refusal/ledger here: the deep watcher on win
							// fires fetch() again for the clamped window, so calling it a
							// second time here would double-request
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
				// still running, so clearing it here briefly shows the previous period's
				// content under the new period's label
				if (current()) this.loading = false;
			}
		},
	},
});
</script>

<style scoped>
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
