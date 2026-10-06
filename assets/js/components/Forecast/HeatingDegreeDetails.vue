<template>
	<div class="heating-degree" data-testid="heating-degree">
		<p v-if="loading" class="small mt-2" role="status">
			{{ $t("forecast.energy.heating.loading") }}
		</p>
		<p v-else-if="error" class="small mt-2 text-danger" role="alert">
			{{ $t("forecast.energy.heating.loadError") }}
		</p>
		<template v-else-if="status">
			<p class="small mt-2 mb-1" data-testid="heating-degree-status">
				<span
					class="badge me-1"
					:class="status.applied !== 'none' ? 'text-bg-primary' : 'text-bg-secondary'"
					data-testid="heating-degree-state"
					>{{
						$t(
							status.applied !== "none"
								? "forecast.energy.heating.correcting"
								: "forecast.energy.heating.notCorrecting"
						)
					}}</span
				>{{
					$t("forecast.energy.heating.modeLine", {
						mode: $t(`forecast.energy.heating.modes.${status.mode}`),
					})
				}}
			</p>
			<p class="small mb-2" data-testid="heating-degree-gate">
				{{ gateLine }}
			</p>
			<div class="small mb-2 fit" data-testid="heating-degree-fit">
				<p class="text-muted mb-1">
					{{
						$t("forecast.energy.heating.fitCaption", { from: windowFrom, to: windowTo })
					}}
				</p>
				<div v-for="check in checks" :key="check.key" class="fit-row">
					<span class="fit-label">{{
						$t(`forecast.energy.heating.check.${check.key}`)
					}}</span>
					<span class="fit-value">{{ checkValue(check) }}</span>
					<span class="fit-note text-muted">{{ checkThreshold(check) }}</span>
					<span
						class="fit-state"
						:class="check.met ? 'text-success' : 'text-muted'"
						:data-testid="`heating-degree-check-${check.key}`"
						>{{
							$t(
								check.met
									? "forecast.energy.heating.met"
									: "forecast.energy.heating.notMet"
							)
						}}</span
					>
				</div>
				<div class="fit-row">
					<span class="fit-label">{{ $t("forecast.energy.heating.slotR") }}</span>
					<span class="fit-value">{{ num(status.slotR, 2) }}</span>
					<span class="fit-note fit-wide text-muted">{{
						$t("forecast.energy.heating.autoGate", {
							value: fmtNumber(status.thresholds.autoMinSlotR, 1),
						})
					}}</span>
				</div>
				<div class="fit-row">
					<span class="fit-label">{{ $t("forecast.energy.heating.meanHDD") }}</span>
					<span class="fit-value">{{ num(status.meanHDD, 1, "K") }}</span>
					<span class="fit-note fit-wide text-muted">{{
						$t("forecast.energy.heating.baseTemp", {
							value: fmtNumber(status.thresholds.baseTemp, 0),
						})
					}}</span>
				</div>
			</div>
			<div v-if="status.days.length" class="table-responsive">
				<table
					class="table table-sm small heating-table mb-2"
					data-testid="heating-degree-days"
				>
					<caption class="caption-top pb-1">
						{{
							$t("forecast.energy.heating.daysCaption")
						}}
					</caption>
					<thead>
						<tr>
							<th scope="col">{{ $t("forecast.energy.heating.date") }}</th>
							<th scope="col" class="text-end">
								{{ $t("forecast.energy.heating.uncorrected") }}
							</th>
							<th scope="col" class="text-end">
								{{ $t("forecast.energy.heating.onCorrection") }}
							</th>
							<th scope="col" class="text-end">
								{{ $t("forecast.energy.heating.cap") }}
							</th>
							<th scope="col" class="text-end">
								{{ $t("forecast.energy.heating.autoCorrection") }}
							</th>
						</tr>
					</thead>
					<tbody>
						<tr v-for="day in status.days" :key="day.date">
							<td class="text-nowrap">
								{{ fmtDay(day.date)
								}}<span v-if="day.slots < 92" class="text-muted">
									({{ $t("forecast.energy.heating.partial") }})</span
								>
							</td>
							<td class="text-end text-nowrap">{{ kwh(day.uncorrectedKWh) }}</td>
							<td
								class="text-end text-nowrap"
								:data-testid="`heating-degree-on-${day.date}`"
							>
								{{ day.applied ? signed(day.correctionKWh) : "–" }}
							</td>
							<td class="text-end text-nowrap">
								{{ kwh(day.capKWh)
								}}<span
									v-if="day.applied && day.capped"
									class="ms-1 fw-semibold text-warning"
									:title="
										$t('forecast.energy.heating.cappedTitle', {
											value: signed(day.uncappedKWh),
										})
									"
									>{{ $t("forecast.energy.heating.capped") }}</span
								>
							</td>
							<td class="text-end text-nowrap">
								{{ day.autoApplied ? signed(day.autoKWh) : "–" }}
							</td>
						</tr>
					</tbody>
				</table>
			</div>
			<h6 class="small fw-bold mt-3 mb-1">{{ $t("forecast.energy.heating.shadowTitle") }}</h6>
			<p v-if="status.shadowError" class="small text-muted mb-1">
				{{ $t("forecast.energy.heating.shadowUnavailable") }}
			</p>
			<ul class="list-unstyled small mb-2" data-testid="heating-degree-shadow">
				<li v-for="score in status.shadow" :key="score.window" class="mb-1">
					<span class="fw-semibold"
						>{{ $t(`forecast.energy.heating.window.${score.window}`) }}:</span
					>
					{{ shadowLine(score) }}
				</li>
			</ul>
			<p class="small text-muted mb-0">{{ $t("forecast.energy.heating.explain") }}</p>
		</template>
	</div>
</template>

<script lang="ts">
import { defineComponent } from "vue";
import api from "@/api";
import formatter from "@/mixins/formatter";
import {
	gateChecks,
	shadowVerdict,
	type GateCheck,
	type HeatingDegreeShadowScore,
	type HeatingDegreeStatus,
} from "./heatingDegree";

export default defineComponent({
	name: "HeatingDegreeDetails",
	mixins: [formatter],
	data: () => ({
		status: null as HeatingDegreeStatus | null,
		loading: true,
		error: false,
	}),
	computed: {
		checks(): GateCheck[] {
			return this.status ? gateChecks(this.status) : [];
		},
		gateLine(): string {
			const s = this.status;
			if (!s) return "";
			if (s.open) return this.$t("forecast.energy.heating.gateOpen");
			return this.$t("forecast.energy.heating.gateShut", {
				reason: this.$t(`forecast.energy.heating.reasons.${s.reason || "error"}`),
			});
		},
		windowFrom(): string {
			return this.status?.from ? this.fmtDayMonth(new Date(this.status.from)) : "–";
		},
		windowTo(): string {
			if (!this.status?.computed) return "–";
			const d = new Date(this.status.computed);
			return `${this.fmtDayMonth(d)}, ${this.fmtHourMinute(d)}`;
		},
	},
	mounted() {
		this.load();
	},
	methods: {
		async load() {
			this.loading = true;
			this.error = false;
			try {
				this.status = (await api.get<HeatingDegreeStatus>("energyplan/heatingdegree")).data;
			} catch {
				this.error = true;
			} finally {
				this.loading = false;
			}
		},
		num(value: number | undefined, digits: number, unit?: string): string {
			if (value === undefined) return "–";
			const text = this.fmtNumber(value, digits);
			return unit ? `${text} ${unit}` : text;
		},
		kwh(value: number): string {
			return `${this.fmtNumber(value, 1)} kWh`;
		},
		signed(value: number, digits = 1): string {
			const text = `${this.fmtNumber(Math.abs(value), digits)} kWh`;
			if (Math.abs(value) < 0.5 * 10 ** -digits) return text;
			return `${value < 0 ? "−" : "+"}${text}`;
		},
		fmtDay(date: string): string {
			const [y, m, d] = date.split("-").map(Number);
			return this.fmtDayMonth(new Date(y!, m! - 1, d!));
		},
		checkValue(check: GateCheck): string {
			switch (check.key) {
				case "dailyR":
					return this.num(check.value, 2);
				case "slope":
					if (check.value === undefined || !this.status) return "–";
					return this.$t("forecast.energy.heating.slopeValue", {
						perSlot: this.fmtNumber(check.value, 1),
						perDay: this.fmtNumber(this.status.slopeKWhPerDayK ?? 0, 2),
					});
				default:
					return this.num(check.value, 0);
			}
		},
		checkThreshold(check: GateCheck): string {
			switch (check.key) {
				case "dailyR":
					return `≥ ${this.fmtNumber(check.threshold, 1)}`;
				case "slope":
					return "> 0";
				default:
					return `≥ ${this.fmtNumber(check.threshold, 0)}`;
			}
		},
		shadowLine(score: HeatingDegreeShadowScore): string {
			if (!score.ready) {
				return this.$t("forecast.energy.heating.collecting", {
					n: score.windows,
					required: score.required,
				});
			}
			return this.$t(`forecast.energy.heating.shadowScore.${shadowVerdict(score)}`, {
				on: this.fmtNumber(score.maeOnKWh ?? 0, 2),
				auto: this.fmtNumber(score.maeAutoKWh ?? 0, 2),
				delta: this.signed(score.deltaKWh ?? 0, 2),
				low: this.fmtNumber(score.ciLowKWh ?? 0, 2),
				high: this.fmtNumber(score.ciHighKWh ?? 0, 2),
				n: score.windows,
			});
		},
	},
});
</script>

<style scoped>
.heating-table {
	width: auto;
	--bs-table-bg: transparent;
	color: var(--evcc-default-text);
}
.heating-table th,
.heating-table td {
	white-space: nowrap;
	padding-right: 0.75rem;
}
.heating-table caption {
	color: var(--evcc-gray);
}
.fit {
	max-width: 40rem;
}
.fit-row {
	display: grid;
	grid-template-columns: minmax(10rem, 1fr) auto 4.5rem 6.5rem;
	column-gap: 0.75rem;
	padding: 0.2rem 0;
	border-bottom: 1px solid var(--evcc-gray-50);
}
.fit-value {
	text-align: right;
}
.fit-wide {
	grid-column: span 2;
}
@media (max-width: 575.98px) {
	.fit-row {
		grid-template-columns: 1fr auto auto;
	}
	.fit-label {
		grid-column: 1 / -1;
	}
	.fit-value {
		text-align: left;
	}
}
</style>
