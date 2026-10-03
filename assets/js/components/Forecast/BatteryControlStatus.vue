<template>
	<section class="border rounded p-3 my-3" :aria-label="$t('forecast.energy.control.title')">
		<h4 class="fs-6 mb-2">{{ $t("forecast.energy.control.title") }}</h4>
		<p class="mb-1">
			{{ $t(commandLabel, { mode: modeLabel(currentMode) }) }}
		</p>
		<p class="small mb-0">
			<span v-if="stateMessage">{{ $t(stateMessage) }}</span>
			<template v-if="fresh">
				<span v-if="pendingExpired">{{
					$t("forecast.energy.control.pendingExpired")
				}}</span>
				<span v-else-if="pending">
					{{
						$t("forecast.energy.control.pending", {
							mode: modeLabel(decision?.pendingMode),
						})
					}}
					<span v-if="remaining != null">
						·
						{{
							remaining > 0
								? $t("forecast.energy.control.eligibleIn", { time: remainingText })
								: $t("forecast.energy.control.awaitingCheck")
						}}</span
					>
				</span>
				<span v-else-if="decision?.vetoReason === 'damping'">{{
					$t("forecast.energy.control.confirming")
				}}</span>
				<span v-else-if="decision?.candidateMode != null">
					{{
						$t("forecast.energy.control.proposed", {
							mode: modeLabel(decision.candidateMode),
						})
					}}
				</span>
				<span v-if="vetoLabel" class="d-block">{{ $t(vetoLabel) }}</span>
				<span v-if="resetLabel" class="d-block">{{ $t(resetLabel) }}</span>
			</template>
		</p>
		<template v-if="fresh">
			<details v-if="economics" class="small mt-2">
				<summary>{{ $t("forecast.energy.control.economics") }}</summary>
				<p class="mt-2 mb-1">{{ $t(`forecast.energy.control.${economics.reason}`) }}</p>
				<p v-if="economics.regretGap != null && economics.materiality != null" class="mb-1">
					{{
						$t("forecast.energy.control.regretGap", {
							gap: money(economics.regretGap),
							threshold: money(economics.materiality),
						})
					}}
				</p>
				<p class="mb-0">{{ $t("forecast.energy.control.scoreNote") }}</p>
			</details>
		</template>
	</section>
</template>
<script lang="ts">
import { defineComponent, type PropType } from "vue";
import { type BATTERY_MODE, CURRENCY, type OptimizerDecision } from "@/types/evcc";
import { decodeBatteryMode } from "@/utils/batteryMode";
import { reasonLabelKey } from "./savingsLedgerDecisions";

const modeKeys: Record<string, string> = {
	normal: "forecast.optimizer.modeNormal",
	hold: "forecast.optimizer.modeHold",
	charge: "forecast.optimizer.modeCharge",
	holdcharge: "forecast.optimizer.modeHoldcharge",
};

export default defineComponent({
	props: {
		decision: { type: Object as PropType<OptimizerDecision>, default: undefined },
		appliedMode: {
			type: [String, Number] as PropType<BATTERY_MODE | number>,
			default: undefined,
		},
		automatic: Boolean,
		offline: Boolean,
		currency: { type: String as PropType<CURRENCY>, default: CURRENCY.EUR },
	},
	data: () => ({
		now: Date.now(),
		timer: undefined as ReturnType<typeof setInterval> | undefined,
	}),
	computed: {
		expires() {
			const d = this.decision;
			return d?.expiresAt
				? Date.parse(d.expiresAt)
				: Date.parse(d?.updated || "") + (d?.validFor || 0);
		},
		decisionFresh() {
			return !this.offline && this.now < this.expires;
		},
		fresh() {
			return this.automatic && this.decisionFresh;
		},
		hasDeviceCommand() {
			return this.decisionFresh && this.decision?.appliedMode != null;
		},
		commandLabel() {
			return this.hasDeviceCommand
				? "forecast.energy.control.applied"
				: "forecast.energy.control.siteMode";
		},
		currentMode() {
			if (this.offline) return undefined;
			return this.hasDeviceCommand ? this.decision?.appliedMode : this.appliedMode;
		},
		stateMessage() {
			if (this.offline) return "forecast.energy.control.offline";
			if (!this.automatic) return "forecast.energy.control.advisory";
			if (!this.decision) return "forecast.energy.control.unavailable";
			if (!this.fresh) return "forecast.energy.control.stale";
			return undefined;
		},
		pendingExpired() {
			const expiry = Date.parse(this.decision?.pendingExpiresAt || "");
			return this.pending && Number.isFinite(expiry) && this.now >= expiry;
		},
		pending() {
			return this.fresh && this.decision?.pendingMode != null;
		},
		remaining() {
			const eligible = Date.parse(this.decision?.eligibleAt || "");
			return this.pending && Number.isFinite(eligible)
				? Math.max(0, Math.ceil((eligible - this.now) / 1000))
				: null;
		},
		remainingText() {
			const seconds = this.remaining || 0;
			return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
		},
		vetoLabel() {
			return this.decision?.vetoReason !== "damping"
				? reasonLabelKey(this.decision?.vetoReason)
				: undefined;
		},
		resetLabel(): string | undefined {
			const reason = this.decision?.resetReason;
			return reason &&
				[
					"candidateChanged",
					"candidateExpired",
					"candidateUnavailable",
					"clockChanged",
				].includes(reason)
				? `forecast.energy.control.${reason}`
				: undefined;
		},
		economics() {
			return this.fresh ? this.decision?.economics : undefined;
		},
	},
	mounted() {
		this.timer = setInterval(() => {
			this.now = Date.now();
		}, 1000);
	},
	beforeUnmount() {
		clearInterval(this.timer);
	},
	methods: {
		modeLabel(value: BATTERY_MODE | number | undefined) {
			const mode = decodeBatteryMode(value);
			return this.$t(
				mode && modeKeys[mode] ? modeKeys[mode]! : "forecast.energy.control.unknownMode"
			);
		},
		money(value: number) {
			return new Intl.NumberFormat(this.$i18n.locale, {
				style: "currency",
				currency: this.currency,
				minimumFractionDigits: 2,
				maximumFractionDigits: 6,
			}).format(value);
		},
	},
});
</script>
