<template>
	<Card class="box-pull-out mb-4" data-testid="energy-plan-card">
		<div class="d-flex flex-wrap justify-content-between gap-2 align-items-start">
			<h3 class="evcc-card-title fw-normal m-0">{{ $t("forecast.energy.title") }}</h3>
			<span class="badge rounded-pill text-bg-secondary">{{
				$t(automatic ? "forecast.energy.automatic" : "forecast.energy.advisory")
			}}</span>
		</div>
		<p class="mt-3 mb-1" role="status" :class="{ 'text-warning': status !== 'ready' }">
			{{ $t(`forecast.energy.status.${status}`) }}<span v-if="reason">: {{ reason }}</span>
		</p>
		<p v-if="insights?.updated" class="small text-muted">
			{{ $t("forecast.energy.updated", { time: dateTime(insights.updated) }) }}
		</p>
		<p v-if="insights?.profile" class="small mb-2" data-testid="energy-profile-coverage">
			{{
				$t("forecast.energy.coverage", {
					buckets: insights.profile.coveredBuckets,
					samples: insights.profile.samples,
					rejected: insights.profile.rejectedSamples,
				})
			}}
			<span v-if="insights.profile.reason"> · {{ insights.profile.reason }}</span>
		</p>
		<p class="small text-muted" data-testid="energy-settlement-label">
			{{ $t("forecast.energy.simulation") }}
		</p>
		<template v-if="slots.length">
			<div class="row g-3 my-1">
				<div class="col-sm-6">
					<div class="text-muted">{{ $t("forecast.energy.home") }}</div>
					<strong>{{ kwh(totals.home) }}</strong>
					<div class="small text-muted">
						{{ kwh(totals.homeLow) }} – {{ kwh(totals.homeHigh) }}
					</div>
				</div>
				<div class="col-sm-6">
					<div class="text-muted">{{ $t("forecast.energy.solar") }}</div>
					<strong>{{ kwh(totals.solar) }}</strong>
					<div class="small text-muted">
						{{ kwh(totals.solarLow) }} – {{ kwh(totals.solarHigh) }}
					</div>
				</div>
			</div>
			<p class="small text-muted mt-2">
				{{ dateTime(slots[0]!.start) }} – {{ dateTime(slots[slots.length - 1]!.end) }} ·
				{{ $t("forecast.energy.envelope") }}
			</p>
			<template v-for="device in batteries" :key="device.key">
				<h4 class="fs-6 mt-4">
					{{ device.title }} · {{ $t("forecast.energy.chargeLevel") }}
				</h4>
				<div class="d-flex justify-content-between small text-muted">
					<span>100 %</span><span>{{ $t("forecast.energy.envelope") }}</span>
				</div>
				<svg
					viewBox="0 0 1000 200"
					class="soc-chart"
					role="img"
					:aria-label="$t('forecast.energy.socChart', { name: device.title })"
					preserveAspectRatio="none"
				>
					<path d="M0 1H1000M0 100H1000M0 199H1000" class="soc-grid" />
					<polygon
						v-if="socEnvelope && device.key === batteries[0]?.key"
						:points="socEnvelope"
						class="soc-envelope"
					/>
					<polyline
						:points="socPoints(device.plan.map((point) => point.soc))"
						class="soc-line"
					/>
					<line :x1="cursorX" :x2="cursorX" y1="0" y2="200" class="soc-cursor" />
				</svg>
				<div class="small text-muted">0 %</div>
			</template>
			<label for="energy-plan-time" class="form-label mt-3"
				>{{ $t("forecast.energy.inspect") }}:
				{{ selectedSlot ? dateTime(selectedSlot.start) : "" }}</label
			>
			<input
				id="energy-plan-time"
				v-model.number="slotIndex"
				type="range"
				class="form-range"
				min="0"
				:max="slots.length - 1"
				step="1"
			/>
			<p v-if="selectedSlot" class="small">
				{{
					$t("forecast.energy.slotContext", {
						price: money(selectedSlot.gridPrice),
						home: kwh(selectedSlot.homeWh / 1000),
						solar: kwh(selectedSlot.solarWh / 1000),
					})
				}}
			</p>
			<div class="table-responsive">
				<table class="table align-middle">
					<thead>
						<tr>
							<th>{{ $t("forecast.energy.device") }}</th>
							<th>{{ $t("forecast.energy.action") }}</th>
							<th>{{ $t("forecast.energy.chargeLevel") }}</th>
						</tr>
					</thead>
					<tbody>
						<tr v-for="device in devices" :key="device.key">
							<td>
								{{ device.title
								}}<small v-if="device.arrival" class="d-block text-muted">{{
									$t("forecast.energy.arrival", {
										time: dateTime(device.arrival),
									})
								}}</small
								><small v-if="device.departure" class="d-block text-muted">{{
									$t("forecast.energy.departure", {
										time: dateTime(device.departure),
									})
								}}</small
								><small
									v-if="device.kind === 'expectedVehicle'"
									class="d-block text-muted"
									>{{ $t("forecast.energy.expected") }}</small
								>
							</td>
							<td>{{ action(device) }}</td>
							<td>
								{{ deviceSlot(device) ? percent(deviceSlot(device)!.soc) : "—" }}
							</td>
						</tr>
					</tbody>
				</table>
			</div>
		</template>
		<details v-if="devices.length" class="mt-3">
			<summary>{{ $t("forecast.energy.transfers") }}</summary>
			<div v-for="device in devices" :key="device.key" class="mt-2">
				<strong>{{ device.title }}</strong>
				<div v-for="window in planWindows(device, slots)" :key="window.start">
					<button
						type="button"
						class="btn btn-link btn-sm p-0 text-start"
						@click="slotIndex = window.index"
					>
						{{ dateTime(window.start) }} – {{ dateTime(window.end) }} ·
						{{
							$t(
								window.charging
									? "forecast.energy.charging"
									: "forecast.energy.discharging",
								{ energy: kwh(window.energyWh / 1000) }
							)
						}}
					</button>
				</div>
			</div>
		</details>
		<template v-if="insights?.scenarios">
			<p class="small mt-2">
				{{ $t("forecast.energy.scenarios") }}: {{ insights.scenarios.status
				}}<span v-if="insights.scenarios.reason"> · {{ insights.scenarios.reason }}</span>
			</p>
			<p
				v-if="insights.scenarios.costLow != null && insights.scenarios.costHigh != null"
				class="small"
			>
				{{
					$t("forecast.energy.costRange", {
						low: money(insights.scenarios.costLow),
						high: money(insights.scenarios.costHigh),
					})
				}}
			</p>
		</template>
		<details v-if="insights?.economics?.length" class="mt-3">
			<summary>{{ $t("forecast.energy.economics") }}</summary>
			<div v-for="item in insights.economics" :key="item.name" class="small mt-2">
				<strong>{{ deviceTitle(item.name) }}</strong
				>:
				{{
					$t("forecast.energy.efficiency", {
						charge: percent(item.chargeEfficiency * 100),
						discharge: percent(item.dischargeEfficiency * 100),
						roundtrip: percent(item.chargeEfficiency * item.dischargeEfficiency * 100),
					})
				}}
				<div>
					{{ item.source }} · {{ $t("forecast.energy.wear") }}:
					{{
						item.wearPerKWh != null
							? money(item.wearPerKWh)
							: $t("forecast.energy.unconfigured")
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
		</details>
		<p
			v-for="note in insights?.arrivalNotes || []"
			:key="note.name"
			class="small text-warning mt-2"
		>
			{{ note.name }}: {{ note.reason }}
		</p>
		<EnergySettings :settings="insights?.settings" :devices="settingsDevices" />
	</Card>
</template>

<script lang="ts">
import { defineComponent, type PropType } from "vue";
import Card from "../Helper/Card.vue";
import EnergySettings from "./EnergySettings.vue";
import {
	forecastTotals,
	planWindows,
	socPoints,
	type EnergyDevice,
	type EnergyInsights,
} from "./energyIntelligence";
import type { BatteryMeter, OptimizerHealth } from "@/types/evcc";

export default defineComponent({
	components: { Card, EnergySettings },
	props: {
		insights: { type: Object as PropType<EnergyInsights>, default: undefined },
		health: { type: Object as PropType<OptimizerHealth>, default: undefined },
		automatic: Boolean,
		currency: { type: String, default: "EUR" },
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
							? [
									{
										name: device.name,
										title: device.title || device.name,
									},
								]
							: []
					)
				: this.batteries;
		},
		totals() {
			return forecastTotals(this.slots);
		},
		selectedSlot() {
			return this.slots[Math.min(this.slotIndex, this.slots.length - 1)];
		},
		cursorX() {
			return (
				(Math.min(this.slotIndex, this.slots.length - 1) /
					Math.max(1, this.slots.length - 1)) *
				1000
			);
		},
		socEnvelope() {
			const low = this.insights?.scenarios?.batterySocLow;
			const high = this.insights?.scenarios?.batterySocHigh;
			if (!low?.length || high?.length !== low.length) return undefined;
			return `${socPoints(low)} ${socPoints(high).split(" ").reverse().join(" ")}`;
		},
	},
	methods: {
		planWindows,
		socPoints,
		dateTime(value: string) {
			return new Date(value).toLocaleString(this.$i18n.locale, {
				weekday: "short",
				hour: "2-digit",
				minute: "2-digit",
			});
		},
		kwh(value: number) {
			return `${value.toLocaleString(this.$i18n.locale, { maximumFractionDigits: 2 })} kWh`;
		},
		percent(value: number) {
			return `${value.toLocaleString(this.$i18n.locale, { maximumFractionDigits: 1 })} %`;
		},
		money(value: number) {
			return new Intl.NumberFormat(this.$i18n.locale, {
				style: "currency",
				currency: this.currency,
			}).format(value);
		},
		deviceTitle(name: string) {
			return this.devices.find((device) => device.name === name)?.title ?? name;
		},
		deviceSlot(device: EnergyDevice) {
			return device.plan.find((slot) => slot.start === this.selectedSlot?.start);
		},
		action(device: EnergyDevice) {
			const slot = this.deviceSlot(device);
			if (!slot) return this.$t("forecast.energy.noPlan");
			if (
				device.arrival &&
				this.selectedSlot &&
				new Date(this.selectedSlot.start) < new Date(device.arrival)
			)
				return this.$t("forecast.energy.away");
			if (slot.chargeWh > 1)
				return this.$t("forecast.energy.charging", {
					energy: this.kwh(slot.chargeWh / 1000),
				});
			if (slot.dischargeWh > 1)
				return this.$t("forecast.energy.discharging", {
					energy: this.kwh(slot.dischargeWh / 1000),
				});
			return this.$t("forecast.energy.idle");
		},
	},
});
</script>

<style scoped>
.soc-chart {
	width: 100%;
	height: 150px;
	overflow: visible;
}
.soc-grid {
	stroke: var(--bs-border-color);
	stroke-width: 1;
	fill: none;
}
.soc-line {
	fill: none;
	stroke: var(--evcc-battery);
	stroke-width: 3;
	vector-effect: non-scaling-stroke;
}
.soc-envelope {
	fill: var(--evcc-battery);
	opacity: 0.16;
}
.soc-cursor {
	stroke: var(--bs-body-color);
	stroke-width: 1;
	stroke-dasharray: 4;
}
th,
td {
	background: transparent;
	color: inherit;
}
summary {
	cursor: pointer;
}
</style>
