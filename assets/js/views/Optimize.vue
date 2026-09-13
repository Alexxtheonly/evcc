<template>
	<div class="container px-4 safe-area-inset">
		<TopHeader :title="$t('forecast.energy.pageTitle')" />
		<nav
			ref="sections"
			class="d-flex flex-wrap gap-2 mb-4"
			:aria-label="$t('forecast.energy.pageTitle')"
		>
			<router-link
				v-for="tab in tabs"
				:key="tab"
				:to="{ path: '/optimize', query: { ...$route.query, tab } }"
				class="btn btn-sm"
				:class="activeTab === tab ? 'btn-primary' : 'btn-outline-secondary'"
				:aria-current="activeTab === tab ? 'page' : undefined"
				>{{ $t(`forecast.energy.tabs.${tab}`) }}</router-link
			>
		</nav>
		<section v-show="activeTab === 'plan'" :aria-label="$t('forecast.energy.tabs.plan')">
			<p v-if="!optimizerEnabled" class="alert alert-info">
				{{ $t("forecast.energy.optimizerDisabled") }}
				<router-link to="/config#integrations">{{ $t("config.main.title") }}</router-link>
			</p>
			<EnergyPlanCard
				@settings-closed="restoreSettingsFocus"
				:insights="optimizerInsights"
				:health="optimizerHealth"
				:automatic="optimizerAutomatic"
				:currency="currency"
				:configured-batteries="configuredBatteries"
			/>
		</section>
		<section
			v-if="resultsVisited"
			v-show="activeTab === 'results'"
			:aria-label="$t('forecast.energy.tabs.results')"
		>
			<p>{{ $t("forecast.energy.resultsNote") }}</p>
			<SavingsLedgerCard
				:currency="currency"
				:settlement="optimizerInsights?.settings"
				:active="activeTab === 'results'"
				@update:decisions="ledgerDecisions = $event"
			/>
			<Card
				v-if="ledgerDecisions"
				:title="$t('forecast.savingsLedger.decisions.title')"
				edge-to-edge
				class="box-pull-out mb-4"
			>
				<SavingsLedgerDecisions
					:decisions="ledgerDecisions"
					:currency="currency"
					:active="activeTab === 'results'"
				/>
			</Card>
		</section>
		<section
			v-if="activeTab === 'diagnostics'"
			:aria-label="$t('forecast.energy.tabs.diagnostics')"
		>
			<Card edge-to-edge class="box-pull-out mt-4 mb-4">
				<OptimizeHeader
					:updated="evopt?.updated"
					:status="evopt?.res?.status"
					:net-cost="netCost"
					:horizon-hours="horizonHours"
					:currency="currency"
					:charging-strategies="chargingStrategies"
					:selected-strategy="optimizerChargingStrategy"
					:pending="pending"
					@optimize="optimizeNow"
					@change-strategy="changeChargingStrategy"
				/>
				<AutomaticModeStrip
					:automatic="optimizerAutomatic"
					:is-sponsor="isSponsor"
					@change="changeAutomatic"
					@learn-more="openOptimizerModal"
				/>
			</Card>
			<div class="row">
				<main class="col-12">
					<div v-if="evopt">
						<h2 class="mt-2 mb-4">Optimizer Plan</h2>

						<Card
							title="Charging Plan"
							subtitle="kW"
							edge-to-edge
							class="box-pull-out mb-4"
						>
							<ChargeChart
								:evopt="evopt"
								:battery-details="evopt.details.batteryDetails"
								:timestamp="evopt.details.timestamp[0]"
								:battery-colors="batteryColors"
								:device-colors="deviceColors"
							/>
						</Card>

						<Card
							v-if="socEntries.length"
							title="SoC Projections"
							subtitle="%"
							edge-to-edge
							class="box-pull-out mb-4"
						>
							<div
								v-for="(entry, idx) in socEntries"
								:key="entry.index"
								:class="{ 'mb-3': idx < socEntries.length - 1 }"
							>
								<SocChart
									:evopt="evopt"
									:entry="entry"
									:timestamp="evopt.details.timestamp[0]"
									:show-x-axis="idx === socEntries.length - 1"
								/>
							</div>
						</Card>

						<Card
							title="Timeline"
							:subtitle="timeSeriesSubtitle"
							edge-to-edge
							class="box-pull-out mb-4"
						>
							<TimeSeriesDataTable
								:evopt="evopt"
								mode="response"
								:battery-details="evopt.details.batteryDetails"
								:timestamps="evopt.details.timestamp"
								:currency="currency"
								:battery-colors="batteryColors"
							/>
						</Card>

						<h2 class="section-title mb-4">Optimizer Inputs</h2>

						<Card
							title="Batteries"
							:subtitle="batteryEfficiencySubtitle"
							edge-to-edge
							class="box-pull-out mb-4"
						>
							<BatteryConfigurationTable
								:batteries="evopt.req.batteries"
								:battery-details="evopt.details.batteryDetails"
								:battery-colors="batteryColors"
								:currency="currency"
							/>
						</Card>

						<Card
							title="Environment"
							:subtitle="timeSeriesSubtitle"
							edge-to-edge
							class="box-pull-out mb-4"
						>
							<TimeSeriesDataTable
								:evopt="evopt"
								mode="request"
								:battery-details="evopt.details.batteryDetails"
								:timestamps="evopt.details.timestamp"
								:currency="currency"
								:battery-colors="batteryColors"
							/>
						</Card>

						<h2 class="section-title mb-4">Raw Data</h2>

						<Card title="Request" edge-to-edge class="box-pull-out mb-4">
							<div class="position-relative">
								<pre
									class="p-3 overflow-auto"
									style="background-color: var(--evcc-gray-10)"
									>{{ formattedRequest }}</pre>
								<CopyButton :content="formattedRequest" />
							</div>
						</Card>

						<Card title="Response" edge-to-edge class="box-pull-out mb-4">
							<div class="position-relative">
								<pre
									class="p-3 overflow-auto"
									style="background-color: var(--evcc-gray-10)"
									>{{ formattedResponse }}</pre>
								<CopyButton :content="formattedResponse" />
							</div>
						</Card>
					</div>
					<div v-else>
						<p>{{ $t("forecast.energy.noDiagnostics") }}</p>
					</div>
				</main>
			</div>
		</section>
	</div>
</template>

<script lang="ts">
import { defineComponent } from "vue";
import Header from "../components/Top/Header.vue";
import Card from "../components/Helper/Card.vue";
import OptimizeHeader from "../components/Optimize/OptimizeHeader.vue";
import AutomaticModeStrip from "../components/Optimize/AutomaticModeStrip.vue";
import EnergyPlanCard from "../components/Forecast/EnergyPlanCard.vue";
import SavingsLedgerCard from "../components/Forecast/SavingsLedgerCard.vue";
import SavingsLedgerDecisions from "../components/Forecast/SavingsLedgerDecisions.vue";
import type { LedgerDecisionRow } from "../components/Forecast/savingsLedger.types";
import { openModal } from "../configModal";
import BatteryConfigurationTable from "../components/Optimize/BatteryConfigurationTable.vue";
import SocChart, { type SocChartEntry } from "../components/Optimize/SocChart.vue";
import ChargeChart from "../components/Optimize/ChargeChart.vue";
import TimeSeriesDataTable from "../components/Optimize/TimeSeriesDataTable.vue";
import CopyButton from "../components/Optimize/CopyButton.vue";
import { formatCompactJson } from "../components/Optimize/compactJson";
import { loadpointTitle } from "../components/Optimize/chart";
import api from "../api";
import store from "../store";
import formatter from "../mixins/formatter";
import { resolveColors, deviceColorMap, batteryColor } from "../colors";
import { CURRENCY, type BatteryDetail } from "../types/evcc";

export default defineComponent({
	name: "Optimize",
	components: {
		TopHeader: Header,
		Card,
		OptimizeHeader,
		BatteryConfigurationTable,
		SocChart,
		ChargeChart,
		TimeSeriesDataTable,
		CopyButton,
		AutomaticModeStrip,
		EnergyPlanCard,
		SavingsLedgerCard,
		SavingsLedgerDecisions,
	},
	mixins: [formatter],
	data() {
		return {
			pending: false,
			tabs: ["plan", "results", "diagnostics"],
			resultsVisited: false,
			ledgerDecisions: null as LedgerDecisionRow[] | null,
		};
	},
	head() {
		return { title: this.$t("forecast.energy.pageTitle") };
	},
	computed: {
		activeTab(): string {
			const tab = this.$route.query["tab"];
			return typeof tab === "string" && this.tabs.includes(tab) ? tab : "plan";
		},
		optimizerEnabled() {
			return !!store.state.optimizer;
		},
		optimizerInsights() {
			return store.state.optimizerInsights;
		},
		optimizerHealth() {
			return store.state.optimizerHealth;
		},
		configuredBatteries() {
			return store.state.battery?.devices || [];
		},
		evopt() {
			return store.state.evopt;
		},
		currency() {
			return store.state.currency || CURRENCY.EUR;
		},
		chargingStrategies(): string[] {
			return store.state.optimizerChargingStrategies || [];
		},
		optimizerChargingStrategy(): string {
			return store.state.optimizerChargingStrategy || "";
		},
		optimizerAutomatic(): boolean {
			return !!store.state.optimizerAutomatic;
		},
		isSponsor(): boolean {
			return !!store.state.sponsor?.status?.name;
		},
		// Sign-flipped for display only (positive = paying the grid). The value itself is the
		// solver's raw objective, not a settled cost: it includes a terminal battery-value
		// credit, so it must not be rendered as currency. See OptimizeHeader's netCostDisplay.
		netCost(): number {
			return (this.evopt?.res?.objective_value || 0) * -1;
		},
		horizonHours(): number {
			const dt = this.evopt?.req?.time_series?.dt;
			if (!dt?.length) return 0;
			return Math.round(dt.reduce((sum, s) => sum + s, 0) / 3600);
		},
		batteryEfficiencySubtitle(): string {
			const etaC = this.fmtPercentage((this.evopt?.req.eta_c || 1) * 100, 1);
			const etaD = this.fmtPercentage((this.evopt?.req.eta_d || 1) * 100, 1);
			return `${etaC} charge efficiency ・ ${etaD} discharge efficiency`;
		},
		deviceColors() {
			return deviceColorMap(store.state.deviceColors);
		},
		batteryDetails(): BatteryDetail[] {
			return this.evopt?.details?.batteryDetails || [];
		},
		loadpointColorKeys(): string[] {
			return [
				...new Set(
					this.batteryDetails.filter((d) => d.type === "vehicle").map(loadpointTitle)
				),
			];
		},
		// loadpoints share the picker palette with History
		loadpointPalette() {
			return resolveColors(this.loadpointColorKeys, this.deviceColors);
		},
		// per-entry colors aligned with res.batteries: dedicated battery palette
		// for home batteries (same as battery page and history), picker palette
		// for loadpoints
		batteryColors(): string[] {
			let batteryIndex = 0;
			return this.batteryDetails.map((d) => {
				if (d.type === "battery") return batteryColor(batteryIndex++);
				const key = loadpointTitle(d);
				return this.loadpointPalette[key] || "";
			});
		},
		// loadpoints first, then batteries, matching the charging plan order
		socEntries(): SocChartEntry[] {
			const entries = this.batteryDetails.map((d, i) => ({
				index: i,
				type: d.type,
				title: d.title || d.name,
				capacity: d.capacity,
				color: this.batteryColors[i] || "",
			}));
			return [
				...entries.filter((e) => e.type === "vehicle"),
				...entries.filter((e) => e.type === "battery"),
			];
		},
		timeSeriesSubtitle(): string {
			return `${this.evopt?.req.time_series.dt.length || 0} steps ・ ${this.horizonHours} h horizon`;
		},
		formattedRequest() {
			return this.evopt?.req ? formatCompactJson(this.evopt.req) : "";
		},
		formattedResponse() {
			return this.evopt?.res ? formatCompactJson(this.evopt.res) : "";
		},
	},
	watch: {
		activeTab: {
			immediate: true,
			handler(tab: string) {
				if (tab === "results") this.resultsVisited = true;
			},
		},
		"evopt.updated"() {
			// re-enable the refresh action once a fresh optimizer run lands
			this.pending = false;
		},
	},
	methods: {
		restoreSettingsFocus() {
			if (this.activeTab !== "plan") {
				(this.$refs["sections"] as HTMLElement)
					.querySelector<HTMLElement>('[aria-current="page"]')
					?.focus();
			}
		},
		optimizeNow() {
			this.pending = true;
			api.post("optimize");
		},
		changeChargingStrategy(value: string) {
			api.post(`optimizerchargingstrategy/${value}`);
		},
		changeAutomatic(checked: boolean) {
			api.post(`config/optimizerautomatic/${checked}`);
		},
		openOptimizerModal() {
			openModal("optimizer");
		},
	},
});
</script>

<style scoped>
.section-title {
	margin-top: 4rem;
}
</style>
