<template>
	<p v-if="readonly" class="text-muted small mb-3" data-testid="repeating-plan-readonly-hint">
		{{ $t("main.chargingPlan.adaptivePlanReadonly") }}
		<button
			v-if="vehicle?.adaptivePlanLearning"
			type="button"
			class="btn btn-link p-0 align-baseline"
			data-testid="repeating-plan-vehicle-settings-link"
			@click="openVehicleSettings"
		>
			{{ $t("main.chargingPlan.adaptivePlanReadonlyLink") }}
		</button>
	</p>
	<div v-for="(plan, index) in plans" :key="index" data-testid="plan-entry">
		<div>
			<ChargingPlanRepeatingSettings
				:showHeader="index === 0"
				:number="index + 2"
				class="mb-4"
				:formIdPrefix="formIdPrefix"
				v-bind="plan"
				:rangePerSoc="rangePerSoc"
				:readonly="readonly"
				@updated="updatePlan(index, $event)"
				@removed="removePlan(index)"
			/>
		</div>
	</div>
	<div v-if="!readonly" class="d-flex align-items-center pb-4">
		<button
			type="button"
			class="d-flex btn btn-sm btn-outline-secondary border-0 align-items-center gap-2 evcc-gray"
			data-testid="repeating-plan-add"
			tabindex="0"
			@click="addPlan"
		>
			<shopicon-regular-plus size="s" class="flex-shrink-0"></shopicon-regular-plus>
			{{ $t("main.chargingPlan.addRepeatingPlan") }}
		</button>
	</div>
</template>

<script lang="ts">
import Modal from "bootstrap/js/dist/modal";
import PlanRepeatingSettings from "./PlanRepeatingSettings.vue";
import deepEqual from "@/utils/deepEqual";
import formatter from "@/mixins/formatter";
import { defineComponent, type PropType } from "vue";
import type { RepeatingPlan, Vehicle } from "@/types/evcc";

const DEFAULT_WEEKDAYS = [1, 2, 3, 4, 5];
const DEFAULT_TARGET_TIME = "07:00";
const DEFAULT_TARGET_SOC = 80;

export default defineComponent({
	name: "ChargingPlansRepeatingSettings",
	components: {
		ChargingPlanRepeatingSettings: PlanRepeatingSettings,
	},
	mixins: [formatter],
	props: {
		id: [Number, String],
		rangePerSoc: Number,
		plans: { type: Array as PropType<RepeatingPlan[]>, default: () => [] },
		// true while these are the vehicle's learned plans, shown for visibility only:
		// no repeating plan can ever be created, changed or removed through this list
		readonly: Boolean,
		vehicle: Object as PropType<Vehicle>,
	},
	emits: ["updated"],
	computed: {
		formIdPrefix() {
			return `chargingplan-lp${this.id}`;
		},
	},
	methods: {
		deepEqual,
		addPlan(): void {
			if (this.readonly) return;

			const newPlan = {
				weekdays: DEFAULT_WEEKDAYS,
				time: DEFAULT_TARGET_TIME,
				soc: DEFAULT_TARGET_SOC,
				active: false,
				tz: this.timezone(),
			};

			// update the plan without storing non-applied changes from other plans
			const plans = [...this.plans]; // clone array
			plans.push(newPlan);
			this.updatePlans(plans);
		},
		updatePlan(index: number, plan: RepeatingPlan): void {
			if (this.readonly) return;

			const plans = [...this.plans]; // clone array
			plans.splice(index, 1, plan);
			this.updatePlans(plans);
		},
		updatePlans(plans: RepeatingPlan[]): void {
			this.$emit("updated", plans);
		},
		removePlan(index: number): void {
			if (this.readonly) return;

			const plans = [...this.plans]; // clone array
			plans.splice(index, 1);
			this.updatePlans(plans);
		},
		openVehicleSettings(): void {
			const el = document.getElementById("vehicleSettingsModal");
			if (el) Modal.getOrCreateInstance(el).show();
		},
	},
});
</script>

<style scoped>
.btn-outline-secondary {
	margin-left: -0.5rem;
}
</style>
