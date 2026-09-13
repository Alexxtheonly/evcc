<template>
	<GenericModal
		id="energySettingsModal"
		ref="modal"
		:title="$t('forecast.energy.settings')"
		:uncloseable="saving"
		:prevent-dismiss="saving"
		@open="load"
		@close="closed"
	>
		<form ref="form" novalidate @submit.prevent="save">
			<p class="small">{{ $t("forecast.energy.settingsNote") }}</p>
			<p v-if="error" class="alert alert-danger" role="alert">{{ error }}</p>
			<div v-if="!loaded" class="py-3" role="status">
				<button v-if="error" type="button" class="btn btn-outline-secondary" @click="load">
					{{ $t("forecast.energy.retry") }}
				</button>
				<span v-else>{{ $t("forecast.energy.loading") }}</span>
			</div>
			<template v-else>
				<div
					class="d-flex flex-wrap gap-2 mb-3"
					:aria-label="$t('forecast.energy.settings')"
				>
					<button
						v-for="section in sections"
						:key="section"
						type="button"
						class="btn btn-sm"
						:class="section === activeSection ? 'btn-primary' : 'btn-outline-secondary'"
						:aria-pressed="section === activeSection"
						@click="activeSection = section"
					>
						{{ $t(`forecast.energy.sections.${section}`) }}
					</button>
				</div>
				<section v-show="activeSection === 'planning'">
					<h6>{{ $t("forecast.energy.sections.planning") }}</h6>
					<p class="small">{{ $t("forecast.energy.planningDefaults") }}</p>
					<div
						v-for="setting in toggles"
						:key="setting.key"
						class="form-check form-switch mb-3"
					>
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
						<div class="small text-muted">{{ $t(setting.hint) }}</div>
					</div>
					<p class="small">{{ $t("forecast.energy.arrivalPrerequisites") }}</p>
				</section>
				<section v-show="activeSection === 'billing'">
					<h6>{{ $t("forecast.energy.sections.billing") }}</h6>
					<label for="energy-settlement" class="form-label">{{
						$t("forecast.energy.settlement")
					}}</label>
					<select
						id="energy-settlement"
						v-model="draft.settlementMode"
						class="form-select mb-2"
					>
						<option value="simulation">
							{{ $t("forecast.energy.simulationOption") }}
						</option>
						<option value="interval">{{ $t("forecast.energy.intervalOption") }}</option>
					</select>
					<p class="small text-muted">{{ $t("forecast.energy.billingNote") }}</p>
					<template v-if="draft.settlementMode === 'interval'">
						<label for="energy-settlement-from" class="form-label">{{
							$t("forecast.energy.settlementFrom")
						}}</label>
						<input
							id="energy-settlement-from"
							v-model="settlementDate"
							type="date"
							class="form-control"
						/>
						<p class="small text-muted mt-2">
							{{ $t("forecast.energy.billingDateNote") }}
						</p>
					</template>
				</section>
				<section v-show="activeSection === 'batteries'">
					<h6>{{ $t("forecast.energy.sections.batteries") }}</h6>
					<p class="small">{{ $t("forecast.energy.batteryDefaults") }}</p>
					<p v-if="!devices.length" class="small">
						{{ $t("forecast.energy.noBatteries") }}
					</p>
					<div
						v-for="device in devices"
						:key="device.name"
						class="border rounded p-3 mb-3"
					>
						<h6>{{ device.title }}</h6>
						<label :for="`energy-efficiency-mode-${device.name}`" class="form-label">{{
							$t("forecast.energy.efficiencyMode")
						}}</label>
						<select
							:id="`energy-efficiency-mode-${device.name}`"
							:value="custom[device.name] ? 'custom' : 'automatic'"
							class="form-select"
							@change="changeEfficiencyMode(device.name, $event)"
						>
							<option value="automatic">
								{{ $t("forecast.energy.efficiencyAutomatic") }}
							</option>
							<option value="custom">
								{{ $t("forecast.energy.efficiencyCustom") }}
							</option>
						</select>
						<p class="small mt-2 mb-2" :data-testid="`energy-effective-${device.name}`">
							{{ effectiveDescription(device.name) }}
						</p>
						<div v-if="custom[device.name]" class="row g-2 mb-2">
							<div
								v-for="direction in efficiencyDirections"
								:key="direction.key"
								class="col-sm-6"
							>
								<label
									:for="`energy-${direction.key}-${device.name}`"
									class="form-label small"
									>{{ $t(direction.label) }}</label
								>
								<input
									:id="`energy-${direction.key}-${device.name}`"
									v-model="efficiencies[device.name]![direction.key]"
									class="form-control"
									type="number"
									min="0.000001"
									max="100"
									step="any"
								/>
							</div>
						</div>
						<details class="mt-3">
							<summary>{{ $t("forecast.energy.calibrationAndWear") }}</summary>
							<label :for="`energy-plane-${device.name}`" class="form-label mt-3">{{
								$t("forecast.energy.measurementPlane")
							}}</label>
							<select
								:id="`energy-plane-${device.name}`"
								v-model="planes[device.name]"
								class="form-select"
							>
								<option value="unknown">
									{{ $t("forecast.energy.planeUnknown") }}
								</option>
								<option value="dc">{{ $t("forecast.energy.planeDc") }}</option>
								<option value="ac">{{ $t("forecast.energy.planeAc") }}</option>
							</select>
							<p class="small text-muted mt-2">
								{{ $t("forecast.energy.planeNote") }}
							</p>
							<label :for="`energy-wear-${device.name}`" class="form-label"
								>{{ $t("forecast.energy.wear") }} ({{
									fmtCurrencySymbol(currency)
								}})</label
							>
							<input
								:id="`energy-wear-${device.name}`"
								v-model="wear[device.name]"
								class="form-control"
								type="number"
								min="0"
								max="100"
								step="0.001"
								:placeholder="$t('forecast.energy.wearUnknown')"
							/>
							<p class="small text-muted mt-2 mb-0">
								{{ $t("forecast.energy.wearNote") }}
							</p>
						</details>
					</div>
				</section>
			</template>
			<div class="d-flex justify-content-between gap-3 border-top pt-3 mt-3">
				<button
					type="button"
					class="btn btn-outline-secondary flex-shrink-0 text-nowrap"
					:disabled="saving"
					@click="cancel"
				>
					{{ $t("forecast.energy.cancel") }}
				</button>
				<button type="submit" class="btn btn-primary" :disabled="saving || !loaded">
					{{ $t("forecast.energy.save") }}
				</button>
			</div>
		</form>
	</GenericModal>
</template>
<script lang="ts">
import { defineComponent, type PropType } from "vue";
import api from "@/api";
import Modal from "bootstrap/js/dist/modal";
import GenericModal from "../Helper/GenericModal.vue";
import formatter from "@/mixins/formatter";
import { CURRENCY } from "@/types/evcc";
import type { EnergyDevice, EnergyInsights, EnergySettings } from "./energyIntelligence";

const defaults = (): EnergySettings => ({
	robust: false,
	arrivals: false,
	useLearnedEfficiency: false,
	batteryWear: {},
	settlementMode: "simulation",
	settlementFrom: null,
});

export default defineComponent({
	components: { GenericModal },
	mixins: [formatter],
	props: {
		devices: {
			type: Array as PropType<Pick<EnergyDevice, "name" | "title">[]>,
			default: () => [],
		},
		economics: { type: Array as PropType<EnergyInsights["economics"]>, default: () => [] },
		currency: { type: String as PropType<CURRENCY>, default: CURRENCY.EUR },
	},
	emits: ["saved"],
	data: () => ({
		draft: defaults(),
		wear: {} as Record<string, string>,
		planes: {} as Record<string, "ac" | "dc" | "unknown">,
		efficiencies: {} as Record<
			string,
			{ chargeEfficiency: string; dischargeEfficiency: string }
		>,
		custom: {} as Record<string, boolean>,
		efficiencyDirections: [
			{ key: "chargeEfficiency" as const, label: "forecast.energy.chargeEfficiency" },
			{ key: "dischargeEfficiency" as const, label: "forecast.energy.dischargeEfficiency" },
		],
		settlementDate: "",
		initialSettlementDate: "",
		saving: false,
		loaded: false,
		error: "",
		request: 0,
		activeSection: "planning",
		sections: ["planning", "billing", "batteries"],
		toggles: [
			{
				key: "robust" as const,
				label: "forecast.energy.robust",
				hint: "forecast.energy.robustHint",
			},
			{
				key: "arrivals" as const,
				label: "forecast.energy.arrivals",
				hint: "forecast.energy.arrivalsHint",
			},
			{
				key: "useLearnedEfficiency" as const,
				label: "forecast.energy.learnedEfficiency",
				hint: "forecast.energy.learnedHint",
			},
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
		cancel() {
			const element = document.getElementById("energySettingsModal");
			if (element) Modal.getInstance(element)?.hide();
		},
		closed() {
			this.request++;
			this.loaded = false;
		},
		invalidBattery(name: string, field: string, message: string) {
			this.error = `${this.devices.find((device) => device.name === name)?.title ?? name}: ${this.$t(message)}`;
			this.activeSection = "batteries";
			this.$nextTick(() => {
				const input = document.getElementById(`energy-${field}-${name}`);
				const details = input?.closest("details");
				if (details) details.open = true;
				input?.focus();
			});
		},
		hasBadInput(id: string) {
			const input = (this.$refs["form"] as HTMLFormElement).elements.namedItem(id);
			return input instanceof HTMLInputElement && input.validity.badInput;
		},
		initializeDevices() {
			for (const device of this.devices) {
				this.planes[device.name] ||= "unknown";
				this.efficiencies[device.name] ||= {
					chargeEfficiency: "",
					dischargeEfficiency: "",
				};
			}
		},
		effectiveDescription(name: string) {
			const item = this.economics?.find((item) => item.name === name);
			if (!item || (!this.custom[name] && item.source === "configured"))
				return this.$t("forecast.energy.efficiencyPending");
			const source = ["default", "measured", "configured"].includes(item.source)
				? this.$t(`forecast.energy.sources.${item.source}`)
				: item.source;
			return this.$t("forecast.energy.effectiveEfficiency", {
				source,
				charge: this.fmtNumber(item.chargeEfficiency * 100, 1),
				discharge: this.fmtNumber(item.dischargeEfficiency * 100, 1),
				roundtrip: this.fmtNumber(
					item.chargeEfficiency * item.dischargeEfficiency * 100,
					1
				),
			});
		},
		changeEfficiencyMode(name: string, event: Event) {
			this.custom[name] = (event.target as HTMLSelectElement).value === "custom";
			if (!this.custom[name]) return;
			const current = this.efficiencies[name]!;
			if (current.chargeEfficiency || current.dischargeEfficiency) return;
			const item = this.economics?.find((item) => item.name === name);
			if (item)
				this.efficiencies[name] = {
					chargeEfficiency: String(item.chargeEfficiency * 100),
					dischargeEfficiency: String(item.dischargeEfficiency * 100),
				};
		},
		async load() {
			document
				.getElementById("energySettingsModal")
				?.setAttribute("aria-label", this.$t("forecast.energy.settings"));
			const request = ++this.request;
			this.loaded = false;
			this.error = "";
			this.activeSection = "planning";
			try {
				const settings = (await api.get<EnergySettings>("config/energyintelligence")).data;
				if (request !== this.request) return;
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
				this.custom = Object.fromEntries(
					Object.keys(settings.batteryEfficiency || {}).map((name) => [name, true])
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
					this.settlementDate = new Date(
						date.getTime() - date.getTimezoneOffset() * 60000
					)
						.toISOString()
						.slice(0, 10);
				} else this.settlementDate = "";
				this.initialSettlementDate = this.settlementDate;
				this.loaded = true;
			} catch (error) {
				if (request === this.request) this.error = String(error);
			}
		},
		async save() {
			if (!this.loaded || this.saving) return;
			this.error = "";
			for (const device of this.devices) {
				if (this.hasBadInput(`energy-wear-${device.name}`)) {
					this.invalidBattery(device.name, "wear", "forecast.energy.invalidWear");
					return;
				}
				if (this.custom[device.name]) {
					for (const direction of this.efficiencyDirections) {
						if (this.hasBadInput(`energy-${direction.key}-${device.name}`)) {
							this.invalidBattery(
								device.name,
								direction.key,
								"forecast.energy.invalidEfficiency"
							);
							return;
						}
					}
				}
			}
			const batteryWear: Record<string, number> = {};
			for (const [name, text] of Object.entries(this.wear)) {
				if (text === "") continue;
				const value = Number(text);
				if (!Number.isFinite(value) || value < 0 || value > 100) {
					this.invalidBattery(name, "wear", "forecast.energy.invalidWear");
					return;
				}
				batteryWear[name] = value;
			}
			const batteryEfficiency: NonNullable<EnergySettings["batteryEfficiency"]> = {};
			for (const [name, entry] of Object.entries(this.efficiencies)) {
				if (!this.custom[name]) continue;
				const chargeEfficiency = Number(entry.chargeEfficiency) / 100;
				const dischargeEfficiency = Number(entry.dischargeEfficiency) / 100;
				if (
					![chargeEfficiency, dischargeEfficiency].every(
						(value) => Number.isFinite(value) && value > 0 && value <= 1
					)
				) {
					const field =
						Number.isFinite(chargeEfficiency) &&
						chargeEfficiency > 0 &&
						chargeEfficiency <= 1
							? "dischargeEfficiency"
							: "chargeEfficiency";
					this.invalidBattery(name, field, "forecast.energy.invalidEfficiency");
					return;
				}
				batteryEfficiency[name] = { chargeEfficiency, dischargeEfficiency };
			}
			const batteryEnergyPlane = Object.fromEntries(
				Object.entries(this.planes).filter(
					([name, value]) =>
						value !== "unknown" || this.draft.batteryEnergyPlane?.[name] != null
				)
			);
			if (
				this.draft.settlementMode === "interval" &&
				(!this.settlementDate || this.hasBadInput("energy-settlement-from"))
			) {
				this.error = this.$t("forecast.energy.invalidBillingDate");
				this.activeSection = "billing";
				this.$nextTick(() => document.getElementById("energy-settlement-from")?.focus());
				return;
			}
			this.saving = true;
			try {
				await api.put("config/energyintelligence", {
					...this.draft,
					batteryWear,
					batteryEnergyPlane,
					batteryEfficiency,
					settlementFrom:
						this.settlementDate === this.initialSettlementDate
							? (this.draft.settlementFrom ?? null)
							: this.settlementDate
								? new Date(`${this.settlementDate}T00:00:00`).toISOString()
								: null,
				});
				this.$emit("saved");
				this.cancel();
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
form {
	overflow-wrap: anywhere;
}
form p,
form .text-muted {
	color: var(--evcc-default-text) !important;
}
summary {
	cursor: pointer;
}
</style>
