<template>
	<Card class="box-pull-out mb-4 energy-plan" data-testid="energy-plan-card">
		<div class="d-flex flex-wrap justify-content-between gap-2 align-items-start">
			<h3 class="evcc-card-title fw-normal mb-0">{{ $t("forecast.energy.title") }}</h3>
			<button type="button" class="btn btn-sm btn-outline-secondary" @click="openSettings">
				{{ $t("forecast.energy.configure") }}
			</button>
		</div>
		<div class="d-flex flex-wrap align-items-center gap-2 mt-2 mb-3 small meta">
			<BatteryControlStatus
				v-if="hasBattery"
				compact
				:decision="decision"
				:applied-mode="appliedMode"
				:automatic="automatic"
				:offline="offline"
				:currency="currency"
			/>
			<span v-else>{{
				$t(automatic ? "forecast.energy.automatic" : "forecast.energy.advisory")
			}}</span>
			<span v-if="insights?.updated" class="text-muted">{{
				$t("forecast.energy.updated", { time: clock(insights.updated) })
			}}</span>
		</div>
		<p v-if="status === 'unavailable'" role="status" class="alert alert-warning">
			{{ $t("forecast.energy.status.unavailable") }}<span v-if="reason">: {{ reason }}</span>
		</p>
		<div class="headline mb-3" data-testid="energy-plan-headline">
			<p
				v-for="(sentence, index) in headlineSentences"
				:key="index"
				:class="index === 0 ? 'headline-lead' : 'headline-more'"
			>
				{{ sentence }}
			</p>
			<p v-if="!automatic" class="small text-muted mb-0">
				{{ $t("forecast.energy.previewNote") }}
			</p>
		</div>
		<ul
			v-if="bandLines.length"
			class="band-status list-unstyled small mb-3"
			data-testid="energy-plan-band"
		>
			<li v-for="line in bandLines" :key="line.key">
				{{ line.text }}
				<span class="d-block text-muted">{{ line.reference }}</span>
			</li>
		</ul>
		<template v-if="slots.length">
			<EnergyPlanChart
				:devices="devices"
				:slots="slots"
				:low="insights?.scenarios?.batterySocLow"
				:high="insights?.scenarios?.batterySocHigh"
				:currency="currency"
				:device-color-overrides="deviceColors"
				:history="history"
				:history-hours="historyHours"
				:history-error="historyError"
				@update:history-hours="setHistoryHours"
			/>
			<h4 class="fs-6 mt-4 mb-2">{{ $t("forecast.energy.plan.daysTitle") }}</h4>
			<div class="days mb-3" data-testid="energy-plan-days">
				<section
					v-for="day in days"
					:key="day.key"
					class="day-tile rounded p-3"
					:aria-label="dayLabel(day)"
				>
					<div class="d-flex flex-wrap justify-content-between gap-1 mb-2">
						<strong>{{ dayLabel(day) }}</strong>
						<span v-if="day.partial" class="small text-muted">{{
							interval(day.start, day.end, false)
						}}</span>
					</div>
					<dl class="day-values mb-0">
						<dt>{{ $t("forecast.energy.home") }}</dt>
						<dd>
							{{ energy(day.home.value) }}
							<small class="d-block">{{ range(day.home) }}</small>
						</dd>
						<dt>{{ $t("forecast.energy.solar") }}</dt>
						<dd>
							{{ energy(day.solar.value) }}
							<small class="d-block">{{ range(day.solar) }}</small>
						</dd>
						<dt>{{ $t("forecast.energy.plan.gridImport") }}</dt>
						<dd>
							{{
								day.gridImportKWh != null
									? kwh(day.gridImportKWh)
									: $t("forecast.energy.plan.unavailable")
							}}
						</dd>
						<template v-if="day.vehicleChargeKWh > 0.05">
							<dt>{{ $t("forecast.energy.plan.vehicleCharging") }}</dt>
							<dd>{{ kwh(day.vehicleChargeKWh) }}</dd>
						</template>
						<template v-for="battery in day.batteries" :key="battery.key">
							<dt>{{ battery.title }}</dt>
							<dd>
								{{
									$t("forecast.energy.plan.throughput", {
										charge: kwh(battery.chargeKWh),
										discharge: kwh(battery.dischargeKWh),
									})
								}}
								<small class="d-block">{{
									battery.minSoc != null && battery.maxSoc != null
										? $t("forecast.energy.plan.socRange", {
												min: percent(battery.minSoc),
												max: percent(battery.maxSoc),
											})
										: $t("forecast.energy.unknownLevel")
								}}</small>
							</dd>
						</template>
					</dl>
				</section>
			</div>
		</template>
		<h4 class="fs-6 mt-4 mb-1">{{ $t("forecast.energy.plan.detailsTitle") }}</h4>
		<details v-if="slots.length" class="plan-details" data-testid="energy-plan-schedule">
			<summary>{{ $t("forecast.energy.fullSchedule") }}</summary>
			<div class="table-responsive mt-2">
				<table class="table table-sm schedule mb-0">
					<thead>
						<tr>
							<th scope="col">{{ $t("forecast.energy.plan.when") }}</th>
							<th scope="col">{{ $t("forecast.energy.action") }}</th>
							<th scope="col" class="text-end">
								{{ $t("forecast.energy.plan.levelAfter") }}
							</th>
						</tr>
					</thead>
					<tbody v-for="item in schedule" :key="item.device.key">
						<tr class="device-row">
							<th scope="rowgroup" colspan="3">
								{{ item.device.title }}
								<small
									v-for="note in item.notes"
									:key="note"
									class="d-block fw-normal"
									>{{ note }}</small
								>
							</th>
						</tr>
						<tr v-for="row in item.rows" :key="row.start">
							<td>{{ interval(row.start, row.end, true) }}</td>
							<td>{{ transferAction(row) }}</td>
							<td class="text-end">
								{{ row.soc != null ? percent(row.soc) : "–" }}
							</td>
						</tr>
						<tr v-if="!item.rows.length">
							<td colspan="3">
								{{
									$t(
										item.device.plan.length
											? "forecast.energy.idle"
											: "forecast.energy.noPlan"
									)
								}}
							</td>
						</tr>
					</tbody>
					<tbody>
						<tr class="device-row">
							<th scope="rowgroup" colspan="3">
								{{ $t("forecast.energy.gridChargingTotal") }}
							</th>
						</tr>
						<tr v-for="block in gridSchedule" :key="block.start">
							<td>{{ interval(block.start, block.end, true) }}</td>
							<td colspan="2">
								{{
									$t(
										block.definite
											? "forecast.energy.plan.gridDefinite"
											: "forecast.energy.plan.gridPossible",
										{
											low: kwh(block.minWh / 1000),
											high: kwh(block.maxWh / 1000),
										}
									)
								}}
							</td>
						</tr>
						<tr v-if="!gridSchedule.length">
							<td colspan="3">
								{{
									$t(
										hasGridData
											? "forecast.energy.noGridCharging"
											: "forecast.energy.gridChargingUnavailable"
									)
								}}
							</td>
						</tr>
					</tbody>
				</table>
			</div>
		</details>
		<details v-if="hasBattery" class="plan-details">
			<summary>{{ $t("forecast.energy.control.title") }}</summary>
			<BatteryControlStatus
				:decision="decision"
				:applied-mode="appliedMode"
				:automatic="automatic"
				:offline="offline"
				:currency="currency"
			/>
			<p class="small text-muted">{{ $t("forecast.energy.control.planNote") }}</p>
		</details>
		<details
			class="plan-details"
			data-testid="energy-solar-shading"
			@toggle="shadingOpen = ($event.target as HTMLDetailsElement).open"
		>
			<summary>{{ $t("forecast.energy.shading.title") }}</summary>
			<SolarShadingDetails v-if="shadingOpen" />
		</details>
		<details
			class="plan-details"
			data-testid="energy-heating-degree"
			@toggle="heatingOpen = ($event.target as HTMLDetailsElement).open"
		>
			<summary>{{ $t("forecast.energy.heating.title") }}</summary>
			<HeatingDegreeDetails v-if="heatingOpen" />
		</details>
		<details class="plan-details quality-details">
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
			<p class="small" data-testid="energy-chart-notes">
				{{ $t("forecast.energy.plan.gridHint") }}
				{{ $t("forecast.energy.plan.ribbonHint") }}
			</p>
			<p class="small" data-testid="energy-settlement-label">
				{{ $t("forecast.energy.simulation") }}
			</p>
			<p v-if="slots.length" class="small">
				{{
					$t("forecast.energy.plan.horizonTotals", {
						range: interval(slots[0]!.start, slots.at(-1)!.end, true),
						home: kwh(totals.home),
						solar: kwh(totals.solar),
					})
				}}
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
			<p class="small mt-3 mb-0">
				<router-link :to="{ path: '/optimize', query: { tab: 'diagnostics' } }">{{
					$t("forecast.energy.plan.diagnosticsLink")
				}}</router-link>
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
import api from "@/api";
import settings, { type PlanHistoryHours } from "@/settings";
import Card from "../Helper/Card.vue";
import EnergySettings from "./EnergySettings.vue";
import EnergyPlanChart from "./EnergyPlanChart.vue";
import BatteryControlStatus from "./BatteryControlStatus.vue";
import SolarShadingDetails from "./SolarShadingDetails.vue";
import HeatingDegreeDetails from "./HeatingDegreeDetails.vue";
import formatter from "@/mixins/formatter";
import { is12hFormat } from "@/units";
import {
	forecastTotals,
	planWindows,
	type EnergyBand,
	type EnergyDevice,
	type EnergyInsights,
	type PlanHistory,
} from "./energyIntelligence";
import { gridChargeBlocks } from "./planChartModel";
import { SLOT_MS } from "./planHistoryModel";
import {
	daySummaries,
	planHeadline,
	type DaySummary,
	type EnergyRange,
	type HeadlineItem,
} from "./energyPlanSummary";
import {
	CURRENCY,
	type BatteryMeter,
	type DeviceColors,
	type OptimizerHealth,
	type OptimizerDecision,
	type BATTERY_MODE,
} from "@/types/evcc";

export default defineComponent({
	components: {
		Card,
		EnergySettings,
		EnergyPlanChart,
		BatteryControlStatus,
		SolarShadingDetails,
		HeatingDegreeDetails,
	},
	emits: ["settingsClosed"],
	mixins: [formatter],
	props: {
		insights: { type: Object as PropType<EnergyInsights>, default: undefined },
		health: { type: Object as PropType<OptimizerHealth>, default: undefined },
		decision: { type: Object as PropType<OptimizerDecision>, default: undefined },
		appliedMode: {
			type: [String, Number] as PropType<BATTERY_MODE | number>,
			default: undefined,
		},
		offline: Boolean,
		automatic: Boolean,
		currency: { type: String as PropType<CURRENCY>, default: CURRENCY.EUR },
		configuredBatteries: {
			type: Array as PropType<Pick<BatteryMeter, "name" | "title">[]>,
			default: () => [],
		},
		deviceColors: { type: Object as PropType<DeviceColors>, default: () => ({}) },
		// the Plan tab is showing; measured history is only fetched then
		active: Boolean,
	},
	data() {
		return {
			history: undefined as PlanHistory | undefined,
			historyError: false,
			historySeq: 0,
			historyTimer: 0,
			shadingOpen: false,
			heatingOpen: false,
		};
	},
	computed: {
		historyHours(): PlanHistoryHours {
			return settings.planHistoryHours;
		},
		// the window to fetch, 0 while nothing should be fetched
		historyRequest(): number {
			return this.active && this.slots.length ? this.historyHours : 0;
		},
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
		hasBattery() {
			return !!(this.batteries.length || this.configuredBatteries.length || this.decision);
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
		totals() {
			return forecastTotals(this.slots);
		},
		headline(): HeadlineItem[] {
			return planHeadline(this.insights);
		},
		bandLines(): { key: string; text: string; reference: string }[] {
			return this.devices.flatMap((device) =>
				device.band ? [{ key: device.key, ...this.bandText(device, device.band) }] : []
			);
		},
		headlineSentences(): string[] {
			return this.headline.map((item) => this.sentence(item));
		},
		days(): DaySummary[] {
			return daySummaries(this.slots, this.devices);
		},
		hasGridData() {
			return (
				this.slots.length > 0 &&
				this.slots.every(
					(slot) => slot.gridChargeMinWh != null && slot.gridChargeMaxWh != null
				)
			);
		},
		gridSchedule() {
			return gridChargeBlocks(this.slots).map((block) => ({
				...block,
				start: new Date(block.start).toISOString(),
				end: new Date(block.end).toISOString(),
			}));
		},
		schedule() {
			return this.devices.map((device) => {
				const ends = new Map(this.slots.map((slot) => [slot.end, slot.start]));
				const rows = planWindows(device, this.slots).map((window) => {
					const point = device.plan.find((p) => p.start === ends.get(window.end));
					return {
						...window,
						soc:
							device.capacityKWh > 0 &&
							point?.soc != null &&
							Number.isFinite(point.soc)
								? point.soc
								: undefined,
					};
				});
				return { device, rows, notes: this.deviceNotes(device) };
			});
		},
	},
	watch: {
		historyRequest: {
			immediate: true,
			handler(hours: number) {
				window.clearTimeout(this.historyTimer);
				if (!hours) return;
				this.fetchHistory();
				this.scheduleHistory();
			},
		},
	},
	beforeUnmount() {
		window.clearTimeout(this.historyTimer);
		this.historySeq++;
	},
	methods: {
		setHistoryHours(hours: PlanHistoryHours) {
			settings.planHistoryHours = hours;
		},
		async fetchHistory() {
			const seq = ++this.historySeq;
			const hours = this.historyRequest;
			try {
				// answered quietly: a missing database is shown beside the chart, not raised
				const response = await api.get<PlanHistory>("energyplan/history", {
					params: { hours },
					validateStatus: () => true,
				});
				if (seq !== this.historySeq) return;
				const ok = response.status === 200 && Array.isArray(response.data?.forecast);
				this.history = ok ? response.data : undefined;
				this.historyError = !ok;
			} catch {
				if (seq !== this.historySeq) return;
				this.history = undefined;
				this.historyError = true;
			}
		},
		// measured slots are written just after each boundary: refresh once per slot,
		// not on every websocket update
		scheduleHistory() {
			const now = Date.now();
			const next = Math.floor(now / SLOT_MS) * SLOT_MS + SLOT_MS + 45_000;
			this.historyTimer = window.setTimeout(() => {
				this.fetchHistory();
				this.scheduleHistory();
			}, next - now);
		},
		openSettings(event: Event) {
			(this.$refs["settings"] as unknown as InstanceType<typeof EnergySettings>).open(event);
		},
		clock(value: string, weekday = false) {
			return new Date(value).toLocaleString(this.$i18n.locale, {
				weekday: weekday ? "short" : undefined,
				hour: "2-digit",
				minute: "2-digit",
				hour12: is12hFormat(),
			});
		},
		sameDay(a: string, b: string) {
			return new Date(a).toDateString() === new Date(b).toDateString();
		},
		// Times on the plan's first day read without a weekday; later days name it.
		point(time: string) {
			return this.clock(time, !this.sameDay(time, this.slots[0]?.start ?? time));
		},
		interval(start: string, end: string, weekday: boolean) {
			const startText = weekday ? this.point(start) : this.clock(start);
			const endText = this.clock(end, weekday && !this.sameDay(end, start));
			return `${startText} – ${endText}`;
		},
		// The horizon end is not a decision time; name it as the end of the plan.
		when(start: string, end: string) {
			const fromNow = start === this.slots[0]?.start;
			if (end === this.slots.at(-1)?.end)
				return fromNow
					? this.$t("forecast.energy.plan.whenUntilPlanEnd")
					: this.$t("forecast.energy.plan.whenFromToPlanEnd", {
							start: this.point(start),
						});
			if (fromNow) return this.$t("forecast.energy.plan.whenUntil", { end: this.point(end) });
			return this.$t("forecast.energy.plan.whenRange", {
				range: this.interval(start, end, true),
			});
		},
		sentence(item: HeadlineItem): string {
			const t = (key: string, args: Record<string, unknown> = {}) =>
				this.$t(`forecast.energy.plan.headline.${key}`, args);
			switch (item.kind) {
				case "unavailable":
					return t("unavailable");
				case "idle":
					return t("idle");
				case "batteryCharge": {
					const args = {
						device: item.device,
						energy: this.kwh(item.energyWh / 1000),
						when: this.when(item.start, item.end),
					};
					const base = t(
						item.source === "solar" ? "batteryChargeSolar" : "batteryCharge",
						args
					);
					return item.next
						? `${base} ${t("batteryThen", {
								when: this.when(item.next.start, item.next.end),
								energy: this.kwh(item.next.energyWh / 1000),
							})}`
						: base;
				}
				case "batteryDischarge": {
					const args = {
						device: item.device,
						energy: this.kwh(item.energyWh / 1000),
						when: this.when(item.start, item.end),
					};
					return item.price != null
						? t("batteryDischargePrice", {
								...args,
								price: this.unitPrice(item.price),
							})
						: t("batteryDischarge", args);
				}
				case "vehicleCharge": {
					const args = {
						device: item.device,
						energy: this.kwh(item.energyWh / 1000),
						when: this.when(item.start, item.end),
					};
					const key = item.endSoc != null ? "vehicleChargeSoc" : "vehicleCharge";
					const text = t(key, {
						...args,
						soc: item.endSoc != null ? this.percent(item.endSoc) : "",
					});
					return item.expected ? `${text} ${t("expected")}` : text;
				}
				case "gridCharge": {
					const energy =
						item.maxWh - item.minWh > 50
							? this.$t("forecast.energy.plan.energyRange", {
									low: this.kwh(item.minWh / 1000),
									high: this.kwh(item.maxWh / 1000),
								})
							: this.kwh(item.minWh / 1000);
					const args = { energy, when: this.when(item.start, item.end) };
					return item.price != null
						? t("gridChargePrice", {
								...args,
								price: this.unitPrice(item.price),
							})
						: t("gridCharge", args);
				}
			}
		},
		bandText(device: EnergyDevice, band: EnergyBand) {
			const reference = band.reference.price;
			const args = {
				device: device.title,
				top: this.percent(band.topSoc),
				soc: this.levelText(device.initialSoc),
				reference: reference != null ? this.unitPrice(reference) : "",
				time: band.nextStart ? this.point(band.nextStart) : "",
				price: band.nextPrice != null ? this.unitPrice(band.nextPrice) : "",
			};
			let source = this.$t("forecast.energy.band.referenceMissing", {
				days: band.reference.days,
			});
			if (reference != null)
				source =
					band.reference.source === "fixed"
						? this.$t("forecast.energy.band.referenceFixed", args)
						: this.$t("forecast.energy.band.referenceMedian", {
								...args,
								median:
									band.reference.median != null
										? this.unitPrice(band.reference.median)
										: "",
								days: band.reference.days,
								margin: this.number(band.reference.margin, 0),
							});
			return {
				text: this.$t(`forecast.energy.band.status.${band.status}`, args),
				reference: source,
			};
		},
		dayLabel(day: DaySummary) {
			const date = new Date(day.start);
			const relative = this.relativeDayName(date);
			const dayDate = date.toLocaleDateString(this.$i18n.locale, {
				weekday: relative ? undefined : "long",
				day: "numeric",
				month: "short",
			});
			if (!relative) return dayDate;
			return `${relative.charAt(0).toLocaleUpperCase(this.$i18n.locale)}${relative.slice(1)}, ${dayDate}`;
		},
		deviceNotes(device: EnergyDevice) {
			const notes: string[] = [];
			if (device.capacityKWh > 0)
				notes.push(
					this.$t("forecast.energy.levelJourney", {
						start: this.levelText(device.initialSoc),
						end: this.levelText(device.plan.at(-1)?.soc),
					})
				);
			else notes.push(this.$t("forecast.energy.energyOnly"));
			if (device.kind === "expectedVehicle") notes.push(this.$t("forecast.energy.expected"));
			if (device.arrival)
				notes.push(
					this.$t("forecast.energy.arrival", { time: this.clock(device.arrival, true) })
				);
			if (device.departure)
				notes.push(
					this.$t("forecast.energy.departure", {
						time: this.clock(device.departure, true),
					})
				);
			return notes;
		},
		number(value: number, digits: number) {
			return value.toLocaleString(this.$i18n.locale, {
				minimumFractionDigits: digits,
				maximumFractionDigits: digits,
			});
		},
		kwh(value: number) {
			return `${this.number(value, 1)}\u00a0kWh`;
		},
		unitPrice(value: number) {
			return this.fmtPricePerKWh(value, this.currency).replace(" ", "\u00a0");
		},
		energy(value: number | undefined) {
			return value != null ? this.kwh(value) : this.$t("forecast.energy.plan.unavailable");
		},
		range(value: EnergyRange) {
			if (value.low == null || value.high == null)
				return this.$t("forecast.energy.plan.rangeUnavailable");
			return this.$t("forecast.energy.plan.range", {
				low: this.number(value.low, 1),
				high: this.number(value.high, 1),
			});
		},
		percent(value: number) {
			return `${value.toLocaleString(this.$i18n.locale, { maximumFractionDigits: 0 })}\u00a0%`;
		},
		levelText(value: number | undefined) {
			if (value == null || !Number.isFinite(value))
				return this.$t("forecast.energy.unknownLevel");
			return this.percent(value);
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
		transferAction(window: { charging: boolean; energyWh: number }) {
			return this.$t(
				window.charging ? "forecast.energy.charging" : "forecast.energy.discharging",
				{ energy: `${this.number(window.energyWh / 1000, 2)}\u00a0kWh` }
			);
		},
	},
});
</script>
<style scoped>
.energy-plan {
	overflow-wrap: anywhere;
}
.meta {
	color: var(--evcc-default-text);
}
.headline p {
	margin-bottom: 0.35rem;
}
.headline-lead {
	font-size: 1.125rem;
	font-weight: 600;
	line-height: 1.4;
}
.headline-more {
	line-height: 1.45;
}
.days {
	display: grid;
	grid-template-columns: repeat(auto-fit, minmax(min(100%, 17rem), 1fr));
	gap: 0.75rem;
}
.day-tile {
	background: var(--bs-tertiary-bg);
	color: var(--evcc-default-text);
	overflow-wrap: normal;
}
.day-values {
	display: grid;
	grid-template-columns: auto auto;
	justify-content: space-between;
	gap: 0.4rem 1rem;
	font-size: 0.875rem;
}
.day-values dt {
	font-weight: normal;
}
.day-values dd {
	margin: 0;
	text-align: right;
	font-variant-numeric: tabular-nums;
	font-weight: 600;
	white-space: nowrap;
}
.day-values small {
	font-weight: normal;
	color: var(--bs-secondary-color);
}
.plan-details {
	border-top: 1px solid var(--bs-border-color);
	padding: 0.6rem 0;
}
.plan-details:last-of-type {
	border-bottom: 1px solid var(--bs-border-color);
}
.schedule {
	font-size: 0.875rem;
	--bs-table-bg: transparent;
	color: var(--evcc-default-text);
}
.schedule .device-row th {
	padding-top: 0.75rem;
	border-bottom-color: var(--bs-border-color);
}
.schedule td {
	font-variant-numeric: tabular-nums;
}
summary {
	cursor: pointer;
}
</style>
