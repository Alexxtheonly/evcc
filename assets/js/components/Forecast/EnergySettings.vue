<template>
	<details ref="details" class="mt-3" @toggle="opened">
		<summary>{{ $t("forecast.energy.settings") }}</summary>
		<form class="mt-3" @submit.prevent="save">
			<p class="small text-muted">{{ $t("forecast.energy.settingsNote") }}</p>
			<div v-for="setting in toggles" :key="setting.key" class="form-check form-switch mb-2">
				<input
					:id="`energy-${setting.key}`"
					v-model="draft[setting.key]"
					class="form-check-input"
					type="checkbox"
					role="switch"
				/>
				<label :for="`energy-${setting.key}`" class="form-check-label">{{
					$t(setting.label)
				}}</label>
			</div>
			<div v-for="device in loaded ? devices : []" :key="device.name" class="mb-3">
				<label :for="`energy-wear-${device.name}`" class="form-label"
					>{{ device.title }} · {{ $t("forecast.energy.wear") }}</label
				>
				<input
					:id="`energy-wear-${device.name}`"
					v-model="wear[device.name]"
					class="form-control"
					type="number"
					min="0"
					max="100"
					step="0.001"
					:placeholder="$t('forecast.energy.unconfigured')"
				/>
				<label :for="`energy-plane-${device.name}`" class="form-label mt-2">{{
					$t("forecast.energy.measurementPlane")
				}}</label>
				<select
					:id="`energy-plane-${device.name}`"
					v-model="planes[device.name]"
					class="form-select"
				>
					<option value="unknown">{{ $t("forecast.energy.planeUnknown") }}</option>
					<option value="dc">{{ $t("forecast.energy.planeDc") }}</option>
					<option value="ac">{{ $t("forecast.energy.planeAc") }}</option>
				</select>
				<p class="small text-muted mt-1">{{ $t("forecast.energy.planeNote") }}</p>
				<div class="row g-2">
					<div
						v-for="direction in efficiencyDirections"
						:key="direction.key"
						class="col-6"
					>
						<label :for="`energy-${direction.key}-${device.name}`" class="form-label">{{
							$t(direction.label)
						}}</label>
						<input
							:id="`energy-${direction.key}-${device.name}`"
							v-model="efficiencies[device.name]![direction.key]"
							class="form-control"
							type="number"
							min="0.000001"
							max="100"
							step="any"
							:placeholder="$t('forecast.energy.unconfigured')"
						/>
					</div>
				</div>
				<p class="small text-muted mt-1">{{ $t("forecast.energy.efficiencyNote") }}</p>
			</div>
			<label for="energy-settlement" class="form-label">{{
				$t("forecast.energy.settlement")
			}}</label>
			<select id="energy-settlement" v-model="draft.settlementMode" class="form-select mb-3">
				<option value="simulation">{{ $t("forecast.energy.simulationOption") }}</option>
				<option value="interval">{{ $t("forecast.energy.intervalOption") }}</option>
			</select>
			<label for="energy-settlement-from" class="form-label">{{
				$t("forecast.energy.settlementFrom")
			}}</label>
			<input
				id="energy-settlement-from"
				v-model="settlementDate"
				class="form-control mb-3"
				type="date"
				:required="draft.settlementMode === 'interval'"
			/>
			<p v-if="error" class="text-danger" role="alert">{{ error }}</p>
			<p v-if="saved" class="text-success" role="status">{{ $t("forecast.energy.saved") }}</p>
			<button class="btn btn-primary" type="submit" :disabled="saving || !loaded">
				{{ $t("forecast.energy.save") }}
			</button>
		</form>
	</details>
</template>

<script lang="ts">
import { defineComponent, type PropType } from "vue";
import api from "@/api";
import type { EnergyDevice, EnergySettings } from "./energyIntelligence";

const defaults = (): EnergySettings => ({
	robust: false,
	arrivals: false,
	useLearnedEfficiency: false,
	batteryWear: {},
	settlementMode: "simulation",
	settlementFrom: null,
});

export default defineComponent({
	props: {
		settings: { type: Object as PropType<EnergySettings>, default: undefined },
		devices: {
			type: Array as PropType<Pick<EnergyDevice, "name" | "title">[]>,
			default: () => [],
		},
	},
	data: () => ({
		draft: defaults(),
		wear: {} as Record<string, string>,
		planes: {} as Record<string, "ac" | "dc" | "unknown">,
		efficiencies: {} as Record<
			string,
			{ chargeEfficiency: string; dischargeEfficiency: string }
		>,
		efficiencyDirections: [
			{ key: "chargeEfficiency" as const, label: "forecast.energy.chargeEfficiency" },
			{ key: "dischargeEfficiency" as const, label: "forecast.energy.dischargeEfficiency" },
		],
		settlementDate: "",
		saving: false,
		loaded: false,
		error: "",
		saved: false,
		toggles: [
			{ key: "robust" as const, label: "forecast.energy.robust" },
			{ key: "arrivals" as const, label: "forecast.energy.arrivals" },
			{ key: "useLearnedEfficiency" as const, label: "forecast.energy.learnedEfficiency" },
		],
	}),
	watch: {
		devices: {
			immediate: true,
			handler() {
				this.initializeDevices();
			},
		},
	},
	methods: {
		initializeDevices() {
			for (const device of this.devices) {
				this.planes[device.name] ||= "unknown";
				this.efficiencies[device.name] ||= {
					chargeEfficiency: "",
					dischargeEfficiency: "",
				};
			}
		},
		async opened(event: Event) {
			if (!(event.target as HTMLDetailsElement).open) return;
			this.loaded = false;
			this.error = "";
			this.saved = false;
			try {
				const settings = (await api.get<EnergySettings>("config/energyintelligence")).data;
				this.draft = { ...settings, batteryWear: { ...settings.batteryWear } };
				this.planes = { ...settings.batteryEnergyPlane };
				this.efficiencies = Object.fromEntries(
					Object.entries(settings.batteryEfficiency || {}).map(([name, value]) => [
						name,
						{
							chargeEfficiency: String(value.chargeEfficiency * 100),
							dischargeEfficiency: String(value.dischargeEfficiency * 100),
						},
					])
				);
				this.initializeDevices();
				this.wear = Object.fromEntries(
					Object.entries(settings.batteryWear || {}).map(([name, value]) => [
						name,
						String(value),
					])
				);
				if (settings.settlementFrom) {
					const date = new Date(settings.settlementFrom);
					const local = new Date(date.getTime() - date.getTimezoneOffset() * 60000);
					this.settlementDate = local.toISOString().slice(0, 10);
				} else this.settlementDate = "";
				this.loaded = true;
			} catch (error) {
				this.error = String(error);
			}
		},
		async save() {
			if (!this.loaded || this.saving) return;
			this.error = "";
			this.saved = false;
			const batteryWear: Record<string, number> = {};
			for (const [name, text] of Object.entries(this.wear)) {
				if (text === "") continue;
				const value = Number(text);
				if (!Number.isFinite(value) || value < 0 || value > 100) {
					this.error = this.$t("forecast.energy.invalidWear");
					return;
				}
				batteryWear[name] = value;
			}
			const batteryEfficiency: NonNullable<EnergySettings["batteryEfficiency"]> = {};
			for (const [name, entry] of Object.entries(this.efficiencies)) {
				if (entry.chargeEfficiency === "" && entry.dischargeEfficiency === "") continue;
				const chargeEfficiency = Number(entry.chargeEfficiency) / 100;
				const dischargeEfficiency = Number(entry.dischargeEfficiency) / 100;
				if (
					![chargeEfficiency, dischargeEfficiency].every(
						(value) => Number.isFinite(value) && value > 0 && value <= 1
					)
				) {
					this.error = this.$t("forecast.energy.invalidEfficiency");
					return;
				}
				batteryEfficiency[name] = { chargeEfficiency, dischargeEfficiency };
			}
			this.saving = true;
			try {
				await api.put("config/energyintelligence", {
					...this.draft,
					batteryWear,
					batteryEnergyPlane: this.planes,
					batteryEfficiency,
					settlementFrom: this.settlementDate
						? new Date(`${this.settlementDate}T00:00:00`).toISOString()
						: null,
				});
				this.saved = true;
			} catch (error) {
				this.error = String(error);
			} finally {
				this.saving = false;
			}
		},
	},
});
</script>

<style scoped>
summary {
	cursor: pointer;
}
form {
	max-width: 38rem;
}
</style>
