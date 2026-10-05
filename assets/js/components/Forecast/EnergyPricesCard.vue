<template>
	<Card class="box-pull-out mb-4 energy-prices">
		<div class="d-flex flex-wrap justify-content-between align-items-center gap-2 mb-3">
			<h3 class="evcc-card-title fw-normal m-0">
				{{ $t("forecast.energyPrices.title") }}
				<button
					v-if="report"
					type="button"
					class="btn btn-link p-0 ms-1 align-baseline info-icon"
					:aria-label="$t('forecast.energyPrices.details')"
					:aria-expanded="showDetails"
					aria-controls="energyPriceDetails"
					data-testid="energy-prices-info"
					@click="showDetails = !showDetails"
				>
					<shopicon-regular-info></shopicon-regular-info>
				</button>
			</h3>
			<button
				type="button"
				class="btn btn-outline-secondary btn-sm"
				:disabled="loading"
				@click="fetchReport(requestedMonth || undefined)"
			>
				{{ $t("forecast.energyPrices.refresh") }}
			</button>
		</div>
		<div
			v-if="report"
			class="d-flex flex-wrap gap-2 align-items-center justify-content-between mb-3"
		>
			<SelectGroup
				id="energyPricePeriod"
				:options="periodOptions"
				:model-value="period"
				:aria-label="$t('forecast.energyPrices.period')"
				@update:model-value="setPeriod"
			/>
			<DateNavigator
				v-if="active"
				:day="selectedDate.getDate()"
				:month="selectedDate.getMonth() + 1"
				:year="selectedDate.getFullYear()"
				:start-date="earliestDate"
				:today="siteToday"
				:show-day="period === 'day'"
				:show-month="period === 'month'"
				:show-year="period === 'month'"
				@update-date="selectDate"
			/>
		</div>
		<p v-if="loading" role="status" class="text-muted py-3">
			{{ $t("forecast.energyPrices.loading") }}
		</p>
		<p v-else-if="error" role="status" class="alert alert-warning">{{ error }}</p>
		<template v-else-if="report && bucket">
			<p class="small text-muted mb-1" data-testid="energy-prices-status">
				{{
					$t("forecast.energyPrices.through", {
						time: through,
						timezone: zone,
					})
				}}<template v-if="partialPeriod">
					· {{ $t("forecast.energyPrices.inProgress") }}</template
				>
			</p>
			<p v-if="zoneFallback" class="small mb-1" data-testid="timezone-fallback">
				{{
					$t("forecast.energyPrices.timezoneFallback", {
						zone: report.timezone,
						browser: zone,
					})
				}}
			</p>
			<p class="small mb-3" data-testid="settlement-basis">{{ settlementLabel }}</p>
			<div
				v-if="showDetails"
				id="energyPriceDetails"
				class="price-details rounded small p-3 mb-3"
				data-testid="energy-price-details"
			>
				<ul class="mb-0 ps-3">
					<li v-for="line in detailLines" :key="line">{{ line }}</li>
				</ul>
			</div>
			<div class="row g-3 mb-3">
				<div class="col-md-6">
					<div class="price-metric rounded p-3 h-100">
						<h4 class="fs-6">{{ $t("forecast.energyPrices.gridPrice") }}</h4>
						<div class="fs-3 fw-semibold">{{ price(bucket.grid.weightedPrice) }}</div>
						<p v-if="bucket.grid.coverage.validSlots" class="small mb-1">
							{{
								$t("forecast.energyPrices.gridTotals", {
									cost: money(bucket.grid.importCost),
									energy: energy(bucket.grid.importKWh),
								})
							}}
						</p>
						<p v-if="bucket.grid.weightedPrice == null" class="small mb-1">
							{{
								$t(
									bucket.grid.coverage.validSlots
										? "forecast.energyPrices.noImport"
										: "forecast.energyPrices.noData"
								)
							}}
						</p>
						<p class="small text-muted mb-0">{{ coverage(bucket.grid.coverage) }}</p>
					</div>
				</div>
				<div class="col-md-6">
					<div class="price-metric rounded p-3 h-100">
						<h4 class="fs-6">{{ $t("forecast.energyPrices.effectivePrice") }}</h4>
						<div class="fs-3 fw-semibold">
							{{ price(bucket.consumption.effectivePrice) }}
						</div>
						<p v-if="bucket.consumption.coverage.validSlots" class="small mb-1">
							{{
								$t("forecast.energyPrices.consumptionTotals", {
									cost: money(bucket.consumption.netCost),
									energy: energy(bucket.consumption.consumptionKWh),
								})
							}}
						</p>
						<p v-if="bucket.consumption.effectivePrice == null" class="small mb-1">
							{{
								$t(
									bucket.consumption.coverage.validSlots
										? "forecast.energyPrices.noConsumption"
										: "forecast.energyPrices.noData"
								)
							}}
						</p>
						<p class="small text-muted mb-0">
							{{ coverage(bucket.consumption.coverage) }}
						</p>
					</div>
				</div>
			</div>
			<h4 class="fs-6 mt-4">{{ $t("forecast.energyPrices.comparison") }}</h4>
			<template v-if="levels">
				<div class="row g-3 mb-3">
					<div class="col-6">
						<div class="price-metric rounded p-3 h-100">
							<h5 class="fs-6 fw-normal mb-1">
								{{ $t("forecast.energyPrices.controlEffect") }}
							</h5>
							<div
								class="fs-4 fw-semibold"
								:class="changeClass(levels.control)"
								data-testid="control-effect"
							>
								{{ levels.control == null ? "–" : signedMoney(levels.control) }}
							</div>
							<p class="small text-muted mb-0">
								{{ $t("forecast.energyPrices.controlEffectBasis") }}
							</p>
						</div>
					</div>
					<div class="col-6">
						<div class="price-metric rounded p-3 h-100">
							<h5 class="fs-6 fw-normal mb-1">
								{{ $t("forecast.energyPrices.totalChange") }}
							</h5>
							<div
								class="fs-4 fw-semibold"
								:class="changeClass(levels.total)"
								data-testid="total-change"
							>
								{{ signedMoney(levels.total) }}
							</div>
							<p class="small text-muted mb-0">
								{{ $t("forecast.energyPrices.totalChangeBasis") }}
							</p>
						</div>
					</div>
				</div>
				<p
					v-if="comparisonCoverageHint"
					class="small mb-2"
					data-testid="comparison-coverage"
				>
					{{ comparisonCoverageHint }}
				</p>
				<div class="table-responsive">
					<table
						class="table table-sm align-middle price-comparison"
						data-testid="level-costs"
					>
						<caption class="small">
							{{
								coverage(worlds[0]!.coverage)
							}}
						</caption>
						<thead>
							<tr>
								<th scope="col">{{ $t("forecast.energyPrices.level") }}</th>
								<th scope="col" class="text-end">
									{{ $t("forecast.energyPrices.netCost") }}
								</th>
								<th scope="col" class="text-end">
									{{ $t("forecast.energyPrices.change") }}
								</th>
							</tr>
						</thead>
						<tbody>
							<tr v-for="level in levels.rows" :key="level.world.label">
								<th scope="row" class="fw-normal">
									{{ $t(`forecast.energyPrices.worlds.${level.world.label}`) }}
									<span class="d-block small text-muted">{{
										worldKind(level.world.label)
									}}</span>
								</th>
								<td class="text-end">
									{{ money(level.net) }}
									<span class="d-block small text-muted">{{
										price(level.world.effectivePrice)
									}}</span>
								</td>
								<td
									class="text-end"
									:class="changeClass(level.change)"
									data-testid="level-change"
								>
									{{ level.change == null ? "–" : signedMoney(level.change) }}
								</td>
							</tr>
						</tbody>
					</table>
				</div>
				<div class="table-responsive">
					<table
						class="table table-sm align-middle price-comparison"
						data-testid="level-purchases"
					>
						<thead>
							<tr>
								<th scope="col">{{ $t("forecast.energyPrices.gridPurchases") }}</th>
								<th scope="col" class="text-end">
									{{ $t("forecast.energyPrices.bought") }}
								</th>
								<th scope="col" class="text-end">
									{{ $t("forecast.energyPrices.avgPrice") }}
								</th>
							</tr>
						</thead>
						<tbody>
							<tr v-for="world in worlds" :key="world.label">
								<th scope="row" class="fw-normal">
									{{ $t(`forecast.energyPrices.worlds.${world.label}`) }}
								</th>
								<td class="text-end">{{ energy(world.importKWh) }}</td>
								<td class="text-end">{{ price(world.weightedPrice) }}</td>
							</tr>
						</tbody>
					</table>
				</div>
			</template>
			<p v-else class="text-muted">
				{{ $t("forecast.energyPrices.comparisonUnavailable")
				}}<span v-if="report.comparison.reason"
					>{{ " " }}{{ reason(report.comparison.reason) }}</span
				>
			</p>
			<details class="mt-3" data-testid="inventory-estimate">
				<summary>{{ $t("forecast.energyPrices.inventory") }}</summary>
				<p class="mt-2 mb-1 fw-semibold">{{ money(bucket.inventoryBenefit) }}</p>
				<p v-if="inventory?.reason" class="small mb-1">
					{{ inventoryDetail(inventory.reason) }}
				</p>
				<p v-else-if="bucket.inventoryBenefit == null" class="small mb-1">
					{{ reason(report.comparison.reason || "unavailable") }}
				</p>
				<p v-if="inventory?.assumptionsSource" class="small mb-1">
					{{ inventoryDetail(inventory.assumptionsSource) }}
				</p>
				<p v-if="inventory?.estimatedEndpoints" class="small mb-1">
					{{
						$t("forecast.energyPrices.estimatedEndpoints", {
							count: inventory.estimatedEndpoints,
						})
					}}
				</p>
				<p class="small mb-1">{{ $t("forecast.energyPrices.inventoryNote") }}</p>
				<p
					v-if="bucket.inventoryBenefit != null && inventory?.wearAdjustment == null"
					class="small mb-1"
				>
					{{ $t("forecast.energy.wearExcluded") }}
				</p>
			</details>
			<details v-if="evTiming" class="mt-3">
				<summary>{{ $t("forecast.energyPrices.evTiming") }}</summary>
				<template v-if="evTiming.status !== 'unavailable' && evTiming.savings != null">
					<p class="mt-2 mb-1 fw-semibold">{{ money(evTiming.savings) }}</p>
					<p class="small mb-1">
						{{
							$t("forecast.energyPrices.evComparison", {
								actual: money(evTiming.actualTariffCost),
								immediate: money(evTiming.immediateTariffCost),
							})
						}}
					</p>
				</template>
				<p v-else class="mt-2 mb-1">{{ $t("forecast.energyPrices.evUnavailable") }}</p>
				<p class="small mb-1">{{ $t("forecast.energyPrices.evNote") }}</p>
				<p class="small mb-1">
					{{
						$t("forecast.energyPrices.evCoverage", {
							included: evTiming.includedSessions,
							excluded: evTiming.excludedSessions,
						})
					}}
				</p>
				<p
					v-if="evTiming.reason && evTiming.reason !== 'tariff_only_connection_cohort'"
					class="small mb-1"
				>
					{{ reason(evTiming.reason) }}
				</p>
			</details>
		</template>
		<p v-else-if="report" class="text-muted">
			{{ $t("forecast.energyPrices.noCompletedSlots") }}
		</p>
	</Card>
</template>
<script lang="ts">
import { defineComponent, type PropType } from "vue";
import api from "@/api";
import "@h2d2/shopicons/es/regular/info";
import Card from "../Helper/Card.vue";
import SelectGroup from "../Helper/SelectGroup.vue";
import DateNavigator from "../Sessions/DateNavigator.vue";
import { CURRENCY } from "@/types/evcc";
import type { LedgerCoverage } from "./savingsLedger.types";
import type { EnergySettings } from "./energyIntelligence";
import {
	browserTimezone,
	calendarDate,
	calendarKey,
	siteCalendarDate,
	usableTimezone,
	type EnergyPriceReport,
	type EnergyPriceBucket,
	type WorldPriceMetric,
} from "./energyPrices";

export default defineComponent({
	components: { Card, SelectGroup, DateNavigator },
	props: {
		active: { type: Boolean, default: true },
		currency: { type: String as PropType<CURRENCY>, default: CURRENCY.EUR },
		settlement: { type: Object as PropType<EnergySettings>, default: undefined },
	},
	emits: ["update:window"],
	data: () => ({
		report: null as EnergyPriceReport | null,
		period: "day" as "day" | "month",
		date: "",
		loading: false,
		error: "",
		fetchSeq: 0,
		requestedMonth: "",
		showDetails: false,
	}),
	computed: {
		worlds(): WorldPriceMetric[] {
			const worlds = this.bucket?.worlds || [];
			// Without a common covered interval every level would read as a fabricated zero.
			return worlds[0]?.coverage.validSlots ? worlds : [];
		},
		// Changes are taken between cent-rounded net costs, so the displayed changes add
		// up to the displayed total exactly. This is presentation only: the API keeps
		// exact sums so days reconcile with the month; prices use unrounded values.
		levels() {
			const worlds = this.worlds;
			if (!worlds.length) return null;
			const net = worlds.map((world) => this.roundMoney(world.netCost));
			const diff = (a: number, b: number) => this.roundMoney(a - b);
			const rows = worlds.map((world, i) => ({
				world,
				net: net[i]!,
				change: i === 0 ? null : diff(net[i]!, net[i - 1]!),
			}));
			const control = rows.find((row) => row.world.label === "W3");
			return {
				rows,
				total: diff(net.at(-1)!, net[0]!),
				control: worlds.at(-2)?.label === "W2" ? (control?.change ?? null) : null,
			};
		},
		comparisonCoverageHint(): string {
			const compared = this.worlds[0]?.coverage;
			const cost = this.bucket?.consumption.coverage;
			return compared && cost && compared.validSlots !== cost.validSlots
				? this.$t("forecast.energyPrices.comparisonCovers", {
						valid: compared.validSlots,
						cost: cost.validSlots,
					})
				: "";
		},
		detailLines(): string[] {
			const t = (key: string, params?: Record<string, unknown>) =>
				this.$t(`forecast.energyPrices.${key}`, params ?? {});
			const c = this.bucket?.consumption;
			const lines = [
				this.billed ? t("intervalDetail") : t("simulationDetail"),
				t("gridFormula"),
				t("effectiveFormula"),
			];
			if (c?.coverage.validSlots) {
				lines.push(
					t("cashBreakdown", {
						imports: this.money(c.importCost),
						exports: this.money(c.exportRevenue),
						exported: this.energy(c.exportKWh),
					})
				);
			}
			if (this.report?.feedInFallbackPrice != null) {
				lines.push(
					t("feedInFallback", { price: this.price(this.report.feedInFallbackPrice) })
				);
			}
			lines.push(
				t("measurementNote"),
				t("comparisonNote"),
				t("changeNote"),
				t("comparisonCoverage"),
				t("cashOnly")
			);
			return lines;
		},
		selectedWindow() {
			const bucket = this.bucket;
			return !this.loading &&
				!this.error &&
				bucket &&
				new Date(bucket.to) > new Date(bucket.from)
				? { from: new Date(bucket.from), to: new Date(bucket.to) }
				: null;
		},
		periodOptions() {
			return ["day", "month"].map((value) => ({
				value,
				name: this.$t(`forecast.energyPrices.${value}`),
			}));
		},
		bucket(): EnergyPriceBucket | undefined {
			return this.period === "month"
				? this.report?.totals
				: (this.report?.days || []).find((day) => day.date === this.date);
		},
		zone(): string {
			return usableTimezone(this.report?.timezone) ?? browserTimezone();
		},
		zoneFallback(): boolean {
			return !!this.report && !usableTimezone(this.report.timezone);
		},
		selectedDate() {
			return calendarDate(this.date || this.report?.month || "1970-01-01");
		},
		siteToday() {
			return calendarDate(this.report?.today || siteCalendarDate(new Date(), this.zone));
		},
		earliestDate() {
			const value = this.report?.earliest;
			return value
				? calendarDate(siteCalendarDate(new Date(value), this.zone))
				: calendarDate(this.report?.month || "1970-01");
		},
		through() {
			return new Date(this.bucket!.to).toLocaleString(this.$i18n.locale, {
				timeZone: this.zone,
				day: "numeric",
				month: "short",
				hour: "2-digit",
				minute: "2-digit",
			});
		},
		partialPeriod() {
			if (!this.report || !this.bucket) return false;
			const today = this.report.today || siteCalendarDate(new Date(), this.zone);
			return this.period === "month"
				? this.report.month === today.slice(0, 7)
				: this.date === today;
		},
		inventory() {
			return this.report?.comparison.inventoryAdjusted;
		},
		evTiming() {
			return (
				this.bucket?.evTiming ??
				(this.period === "month" ? this.report?.evTiming : undefined)
			);
		},

		billed(): boolean {
			const declared =
				this.settlement?.settlementMode === "interval" && this.settlement.settlementFrom;
			return !!declared && !!this.bucket && new Date(this.bucket.from) >= new Date(declared);
		},
		settlementLabel() {
			return this.$t(
				this.billed
					? "forecast.energyPrices.intervalBasis"
					: "forecast.energyPrices.simulationBasis"
			);
		},
	},
	watch: {
		selectedWindow(value) {
			this.$emit("update:window", value);
		},
	},
	mounted() {
		this.fetchReport();
	},
	methods: {
		inventoryDetail(value: string): string {
			const keys: Record<string, string> = {
				no_priced_slots: "noPrices",
				historical_snapshot_unavailable_or_assumptions_changed: "changedAssumptions",
				unsupported_historical_economics: "unsupportedEconomics",
				missing_soc: "missingSoc",
				separate_inventory_neutral_segments_at_gaps_or_assumption_changes: "segments",
				legacy_assumed_efficiency_and_observed_limits: "legacy",
				optimizer_snapshots: "snapshots",
				mixed_snapshots_and_explicit_legacy_assumptions: "mixed",
			};
			const key = Object.hasOwn(keys, value) ? keys[value] : undefined;
			return key ? this.$t(`forecast.energy.inventoryDetails.${key}`) : this.reason(value);
		},
		reason(value: string) {
			const known = [
				"no_priced_slots",
				"battery_physics_unavailable",
				"historical_evidence_unavailable",
				"no_completed_sessions",
			];
			return this.$t(
				`forecast.energyPrices.reasons.${known.includes(value) ? value : "unavailable"}`
			);
		},
		setPeriod(value: string | number | boolean | null) {
			this.period = value === "month" ? "month" : "day";
		},
		selectDate(value: { year: number; month?: number; day?: number }) {
			const month = value.month ?? this.selectedDate.getMonth() + 1;
			const key = calendarKey(value.year, month);
			const today = this.report?.today || siteCalendarDate(new Date(), this.zone);
			if (calendarKey(value.year, month, value.day ?? 1) > today) return;
			this.date = calendarKey(value.year, month, value.day ?? 1);
			if (key !== this.report?.month) this.fetchReport(key);
		},
		async fetchReport(month?: string) {
			const seq = ++this.fetchSeq;
			this.requestedMonth = month || "";
			this.loading = true;
			this.error = "";
			this.$emit("update:window", null);
			try {
				const response = await api.get<EnergyPriceReport>("energyprices", {
					params: month ? { month } : undefined,
				});
				if (seq !== this.fetchSeq) return;
				const report = response.data;
				this.requestedMonth = report.month;
				if (!this.date.startsWith(report.month)) {
					const today =
						report.today ||
						siteCalendarDate(
							new Date(),
							usableTimezone(report.timezone) ?? browserTimezone()
						);
					this.date = today.startsWith(report.month) ? today : `${report.month}-01`;
				}
				this.report = report;
			} catch (error) {
				if (seq !== this.fetchSeq) return;
				this.error = error instanceof Error ? error.message : String(error);
			} finally {
				if (seq === this.fetchSeq) this.loading = false;
			}
		},
		roundMoney(value: number) {
			const digits =
				new Intl.NumberFormat("en", {
					style: "currency",
					currency: this.currency,
				}).resolvedOptions().maximumFractionDigits ?? 2;
			const factor = 10 ** digits;
			return Math.round(value * factor) / factor;
		},
		// House idiom: savings in primary, a cost in danger. A cost is never clamped.
		changeClass(value: number | null) {
			if (value == null || value === 0) return "";
			return value > 0 ? "text-danger" : "text-primary";
		},
		worldKind(label: WorldPriceMetric["label"]) {
			return this.$t(
				label === "W3" ? "forecast.energyPrices.measured" : "forecast.energyPrices.modeled"
			);
		},
		// A level that cost more shows a positive amount; savings are negative. Never clamped.
		signedMoney(value: number) {
			return Number.isFinite(value)
				? new Intl.NumberFormat(this.$i18n.locale, {
						style: "currency",
						currency: this.currency,
						signDisplay: "exceptZero",
					}).format(value)
				: this.$t("forecast.energyPrices.unavailable");
		},
		price(value: number | undefined) {
			return value == null || !Number.isFinite(value)
				? this.$t("forecast.energyPrices.unavailable")
				: `${this.money(value, 4)}/kWh`;
		},
		money(value: number | undefined, digits = 2) {
			return value == null || !Number.isFinite(value)
				? this.$t("forecast.energyPrices.unavailable")
				: new Intl.NumberFormat(this.$i18n.locale, {
						style: "currency",
						currency: this.currency,
						minimumFractionDigits: 2,
						maximumFractionDigits: digits,
					}).format(value);
		},
		energy(value: number) {
			return `${value.toLocaleString(this.$i18n.locale, { maximumFractionDigits: 2 })} kWh`;
		},
		coverage(value: LedgerCoverage) {
			return this.$t("forecast.energyPrices.coverage", {
				valid: value.validSlots,
				total: value.totalSlots,
				percent: (100 * value.fraction).toLocaleString(this.$i18n.locale, {
					maximumFractionDigits: 1,
				}),
			});
		},
	},
});
</script>
<style scoped>
.energy-prices {
	overflow-wrap: anywhere;
}
.price-metric,
.price-details {
	background: var(--bs-tertiary-bg);
}
.info-icon {
	color: var(--evcc-gray);
	cursor: help;
	line-height: 1;
}
.price-comparison {
	font-variant-numeric: tabular-nums;
}
.price-comparison th,
.price-comparison td {
	min-width: 4rem;
	padding: 0.65rem 0.25rem;
}
.price-comparison td {
	font-size: 0.875rem;
	white-space: nowrap;
}
.price-comparison th:first-child {
	width: 38%;
}
summary {
	cursor: pointer;
}
</style>
