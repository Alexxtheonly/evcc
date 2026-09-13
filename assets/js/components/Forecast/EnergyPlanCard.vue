<template>
	<Card class="box-pull-out mb-4 energy-plan" data-testid="energy-plan-card">
		<div class="d-flex flex-wrap justify-content-between gap-2 align-items-center">
			<div>
				<h3 class="evcc-card-title fw-normal mb-1">{{ $t("forecast.energy.title") }}</h3>
				<span class="small">{{
					$t(automatic ? "forecast.energy.automatic" : "forecast.energy.advisory")
				}}</span>
			</div>
			<button type="button" class="btn btn-outline-secondary" @click="openSettings">
				{{ $t("forecast.energy.configure") }}
			</button>
		</div>
		<p v-if="!automatic" class="small mt-2 mb-0">{{ $t("forecast.energy.previewNote") }}</p>
		<p v-if="insights?.updated" class="small text-muted mt-1 mb-3">
			{{ $t("forecast.energy.updated", { time: dateTime(insights.updated) }) }}
		</p>
		<p v-if="status === 'unavailable'" role="status" class="alert alert-warning mt-3">
			{{ $t("forecast.energy.status.unavailable") }}<span v-if="reason">: {{ reason }}</span>
		</p>
		<template v-if="slots.length">
			<p class="fw-semibold mb-2">{{ horizon }}</p>
			<div class="row g-2 mb-3">
				<div v-for="total in totalCards" :key="total.label" class="col-6">
					<div class="forecast-total rounded p-3 h-100">
						<div class="small">{{ $t(total.label) }}</div>
						<div class="fs-4 fw-semibold">{{ kwh(total.value) }}</div>
						<div class="small">
							{{
								$t("forecast.energy.estimatedRange", {
									low: kwh(total.low),
									high: kwh(total.high),
								})
							}}
						</div>
					</div>
				</div>
			</div>
			<h4 class="fs-6 mb-2">{{ $t("forecast.energy.nextActions") }}</h4>
			<div class="row g-2 mb-3">
				<div v-for="item in deviceSummaries" :key="item.device.key" class="col-md-6">
					<div class="device-summary border rounded p-3 h-100">
						<div class="fw-semibold">{{ item.device.title }}</div>
						<div v-if="item.device.capacityKWh > 0" class="small mb-2">
							{{
								$t("forecast.energy.levelJourney", {
									start: percent(item.device.initialSoc),
									end: percent(item.endSoc),
								})
							}}
						</div>
						<div v-else class="small mb-2">{{ $t("forecast.energy.energyOnly") }}</div>
						<button
							v-if="item.next"
							type="button"
							class="btn btn-link p-0 text-start"
							@click="slotIndex = item.next.index"
						>
							{{ transferAction(item.next) }}
							<span class="d-block small">{{
								interval(item.next.start, item.next.end)
							}}</span>
						</button>
						<span v-else>{{
							$t(
								item.device.plan.length
									? "forecast.energy.idle"
									: "forecast.energy.noPlan"
							)
						}}</span>
						<div v-if="item.device.kind === 'expectedVehicle'" class="small mt-2">
							{{ $t("forecast.energy.expected") }}
						</div>
						<div v-if="item.device.arrival" class="small">
							{{
								$t("forecast.energy.arrival", {
									time: dateTime(item.device.arrival),
								})
							}}
						</div>
						<div v-if="item.device.departure" class="small">
							{{
								$t("forecast.energy.departure", {
									time: dateTime(item.device.departure),
								})
							}}
						</div>
					</div>
				</div>
			</div>
			<EnergyPlanChart
				:devices="devices"
				:slots="slots"
				:selected-index="slotIndex"
				:low="insights?.scenarios?.batterySocLow"
				:high="insights?.scenarios?.batterySocHigh"
				:currency="currency"
				@inspect="slotIndex = $event"
			>
				<template #inspection="{ devices: chartDevices }">
					<template v-if="selectedSlot">
						<div class="fw-semibold mb-1">
							{{ interval(selectedSlot.start, selectedSlot.end) }}
						</div>
						<div>
							{{ $t("forecast.energy.chart.price") }}:
							{{ price(selectedSlot.gridPrice) }}
						</div>
						<div>{{ gridCharging }}</div>
						<div v-for="device in chartDevices" :key="device.key" class="mt-1">
							<strong>{{ device.title }}</strong
							>: {{ action(device) }}
							<span v-if="device.capacityKWh > 0 && deviceSlot(device)">
								·
								{{
									$t("forecast.energy.levelAtEnd", {
										soc: percent(deviceSlot(device)!.soc),
									})
								}}</span
							>
						</div>
					</template>
				</template>
			</EnergyPlanChart>
			<div v-if="selectedSlot" class="inspection border rounded p-3 mt-3">
				<div class="d-flex flex-wrap align-items-center justify-content-between gap-2 mb-2">
					<div class="fw-semibold">
						{{ $t("forecast.energy.inspect") }}:
						{{ interval(selectedSlot.start, selectedSlot.end) }}
					</div>
					<div class="d-flex gap-2">
						<button
							type="button"
							class="btn btn-outline-secondary btn-sm"
							:aria-label="$t('forecast.energy.previous')"
							:disabled="slotIndex <= 0"
							@click="slotIndex--"
						>
							‹
						</button>
						<button
							type="button"
							class="btn btn-outline-secondary btn-sm"
							:aria-label="$t('forecast.energy.next')"
							:disabled="slotIndex >= slots.length - 1"
							@click="slotIndex++"
						>
							›
						</button>
					</div>
				</div>
				<p class="small mb-2">
					{{
						$t("forecast.energy.slotContext", {
							price: price(selectedSlot.gridPrice),
							home: kwh(selectedSlot.homeWh / 1000),
							solar: kwh(selectedSlot.solarWh / 1000),
						})
					}}
				</p>
				<p class="small mb-2">{{ gridCharging }}</p>
				<div
					v-for="device in devices"
					:key="device.key"
					class="d-flex flex-wrap justify-content-between gap-1 py-2 border-top"
				>
					<span>{{ device.title }}</span>
					<span
						>{{ action(device)
						}}<small
							v-if="deviceSlot(device) && device.capacityKWh > 0"
							class="d-block text-end"
							>{{
								$t("forecast.energy.levelAtEnd", {
									soc: percent(deviceSlot(device)!.soc),
								})
							}}</small
						></span
					>
				</div>
			</div>
			<details v-if="devices.length" class="mt-3">
				<summary>{{ $t("forecast.energy.fullSchedule") }}</summary>
				<div v-for="item in deviceSummaries" :key="item.device.key" class="mt-3">
					<strong>{{ item.device.title }}</strong>
					<div v-for="window in item.windows" :key="window.start" class="my-1">
						<button
							type="button"
							class="btn btn-link btn-sm p-0 text-start"
							@click="slotIndex = window.index"
						>
							{{ interval(window.start, window.end) }} · {{ transferAction(window) }}
						</button>
					</div>
					<div v-if="!item.windows.length">
						{{
							$t(
								item.device.plan.length
									? "forecast.energy.idle"
									: "forecast.energy.noPlan"
							)
						}}
					</div>
				</div>
			</details>
		</template>
		<details class="mt-3 quality-details">
			<summary>
				{{ $t("forecast.energy.quality")
				}}<span v-if="status === 'degraded'" class="small">
					· {{ $t("forecast.energy.fallbackShort") }}</span
				>
			</summary>
			<p v-if="reason" class="small mt-2">{{ reason }}</p>
			<p v-if="insights?.profile" class="small mt-2" data-testid="energy-profile-coverage">
				{{
					$t("forecast.energy.coverage", {
						buckets: insights.profile.coveredBuckets,
						samples: insights.profile.samples,
						rejected: insights.profile.rejectedSamples,
					})
				}}<span v-if="insights.profile.reason"> · {{ insights.profile.reason }}</span>
			</p>
			<p class="small" data-testid="energy-settlement-label">
				{{ $t("forecast.energy.simulation") }}
			</p>
			<p v-if="insights?.scenarios" class="small">
				{{ $t("forecast.energy.scenarios") }}: {{ insights.scenarios.status
				}}<span v-if="insights.scenarios.reason"> · {{ insights.scenarios.reason }}</span>
			</p>
			<p
				v-if="insights?.scenarios?.costLow != null && insights.scenarios.costHigh != null"
				class="small"
			>
				{{
					$t("forecast.energy.costRange", {
						low: money(insights.scenarios.costLow),
						high: money(insights.scenarios.costHigh),
					})
				}}
			</p>
			<div v-for="item in insights?.economics || []" :key="item.name" class="small mt-3">
				<strong>{{ deviceTitle(item.name) }}</strong>
				<div>
					{{
						$t("forecast.energy.efficiency", {
							charge: percent(item.chargeEfficiency * 100),
							discharge: percent(item.dischargeEfficiency * 100),
							roundtrip: percent(
								item.chargeEfficiency * item.dischargeEfficiency * 100
							),
						})
					}}
				</div>
				<div>
					{{ item.source }} · {{ $t("forecast.energy.wear") }}:
					{{
						item.wearPerKWh != null
							? money(item.wearPerKWh)
							: $t("forecast.energy.wearUnknown")
					}}
				</div>
				<div
					v-if="
						item.candidateChargeEfficiency != null &&
						item.candidateDischargeEfficiency != null
					"
				>
					{{
						$t("forecast.energy.candidate", {
							charge: percent(item.candidateChargeEfficiency * 100),
							discharge: percent(item.candidateDischargeEfficiency * 100),
						})
					}}
				</div>
				<div v-if="item.calibrationReason">{{ item.calibrationReason }}</div>
			</div>
			<p v-for="note in insights?.arrivalNotes || []" :key="note.name" class="small mt-2">
				{{ deviceTitle(note.name) }}: {{ note.reason }}
			</p>
		</details>
		<EnergySettings
			@closed="$emit('settingsClosed')"
			ref="settings"
			:devices="settingsDevices"
			:economics="insights?.economics"
			:currency="currency"
		/>
	</Card>
</template>
<script lang="ts">
import { defineComponent, type PropType } from "vue";
import Card from "../Helper/Card.vue";
import EnergySettings from "./EnergySettings.vue";
import EnergyPlanChart from "./EnergyPlanChart.vue";
import formatter from "@/mixins/formatter";
import { is12hFormat } from "@/units";
import {
	forecastTotals,
	planWindows,
	type EnergyDevice,
	type EnergyInsights,
} from "./energyIntelligence";
import { CURRENCY, type BatteryMeter, type OptimizerHealth } from "@/types/evcc";

export default defineComponent({
	components: { Card, EnergySettings, EnergyPlanChart },
	emits: ["settingsClosed"],
	mixins: [formatter],
	props: {
		insights: { type: Object as PropType<EnergyInsights>, default: undefined },
		health: { type: Object as PropType<OptimizerHealth>, default: undefined },
		automatic: Boolean,
		currency: { type: String as PropType<CURRENCY>, default: CURRENCY.EUR },
		configuredBatteries: {
			type: Array as PropType<Pick<BatteryMeter, "name" | "title">[]>,
			default: () => [],
		},
	},
	data: () => ({ slotIndex: 0 }),
	computed: {
		status() {
			return this.insights?.status ?? (this.health?.ok ? "ready" : "unavailable");
		},
		reason() {
			return this.insights?.reason ?? this.health?.reason;
		},
		slots() {
			return this.insights?.forecast ?? [];
		},
		devices() {
			return this.insights?.devices ?? [];
		},
		batteries() {
			return this.devices.filter((device) => device.kind === "battery");
		},
		settingsDevices() {
			return this.configuredBatteries.length
				? this.configuredBatteries.flatMap((device) =>
						device.name
							? [{ name: device.name, title: device.title || device.name }]
							: []
					)
				: this.batteries;
		},
		totalCards() {
			const totals = forecastTotals(this.slots);
			return [
				{
					label: "forecast.energy.home",
					value: totals.home,
					low: totals.homeLow,
					high: totals.homeHigh,
				},
				{
					label: "forecast.energy.solar",
					value: totals.solar,
					low: totals.solarLow,
					high: totals.solarHigh,
				},
			];
		},
		horizon() {
			return this.slots.length
				? this.interval(this.slots[0]!.start, this.slots[this.slots.length - 1]!.end)
				: "";
		},
		selectedSlot() {
			return this.slots[Math.min(this.slotIndex, this.slots.length - 1)];
		},
		gridCharging() {
			const slot = this.selectedSlot;
			if (slot?.gridChargeMinWh == null || slot.gridChargeMaxWh == null)
				return this.$t("forecast.energy.gridChargingUnavailable");
			return this.$t("forecast.energy.gridChargingRange", {
				low: this.kwh(slot.gridChargeMinWh / 1000),
				high: this.kwh(slot.gridChargeMaxWh / 1000),
			});
		},
		deviceSummaries() {
			return this.devices.map((device) => {
				const windows = planWindows(device, this.slots);
				return { device, windows, next: windows[0], endSoc: device.plan.at(-1)?.soc };
			});
		},
	},
	watch: {
		slots() {
			this.slotIndex = Math.min(this.slotIndex, Math.max(0, this.slots.length - 1));
		},
	},
	methods: {
		price(value: number) {
			return Number.isFinite(value)
				? this.fmtPricePerKWh(value, this.currency)
				: this.$t("forecast.energy.priceUnavailable");
		},
		openSettings(event: Event) {
			(this.$refs["settings"] as unknown as InstanceType<typeof EnergySettings>).open(event);
		},
		dateTime(value: string) {
			return new Date(value).toLocaleString(this.$i18n.locale, {
				weekday: "short",
				hour: "2-digit",
				minute: "2-digit",
				hour12: is12hFormat(),
			});
		},
		interval(start: string, end: string) {
			return `${this.dateTime(start)} – ${this.dateTime(end)}`;
		},
		kwh(value: number) {
			return `${value.toLocaleString(this.$i18n.locale, { maximumFractionDigits: 2 })} kWh`;
		},
		percent(value: number | undefined) {
			if (value == null || !Number.isFinite(value))
				return this.$t("forecast.energy.unknownLevel");
			return `${value.toLocaleString(this.$i18n.locale, { maximumFractionDigits: 1 })} %`;
		},
		money(value: number) {
			return new Intl.NumberFormat(this.$i18n.locale, {
				style: "currency",
				currency: this.currency,
			}).format(value);
		},
		deviceTitle(name: string) {
			return (
				this.devices.find((device) => device.name === name)?.title ??
				this.settingsDevices.find((device) => device.name === name)?.title ??
				name
			);
		},
		deviceSlot(device: EnergyDevice) {
			return device.plan.find((slot) => slot.start === this.selectedSlot?.start);
		},
		transferAction(window: { charging: boolean; energyWh: number }) {
			return this.$t(
				window.charging ? "forecast.energy.charging" : "forecast.energy.discharging",
				{ energy: this.kwh(window.energyWh / 1000) }
			);
		},
		action(device: EnergyDevice) {
			const slot = this.deviceSlot(device);
			if (!slot) return this.$t("forecast.energy.noPlan");
			const time = new Date(this.selectedSlot!.start).getTime();
			if (
				(device.arrival && time < new Date(device.arrival).getTime()) ||
				(device.departure && time >= new Date(device.departure).getTime())
			)
				return this.$t("forecast.energy.away");
			if (slot.chargeWh > 1)
				return this.transferAction({ charging: true, energyWh: slot.chargeWh });
			if (slot.dischargeWh > 1)
				return this.transferAction({ charging: false, energyWh: slot.dischargeWh });
			return this.$t("forecast.energy.idle");
		},
	},
});
</script>
<style scoped>
.energy-plan {
	overflow-wrap: anywhere;
}
.energy-plan .small:not(.text-muted) {
	color: var(--evcc-default-text);
}
.forecast-total {
	background: var(--bs-tertiary-bg);
}
.device-summary,
.inspection {
	border-color: var(--bs-border-color) !important;
}
summary {
	cursor: pointer;
}
.quality-details {
	border-top: 1px solid var(--bs-border-color);
	padding-top: 1rem;
}
</style>
