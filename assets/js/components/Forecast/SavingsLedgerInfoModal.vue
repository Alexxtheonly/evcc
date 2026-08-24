<template>
	<GenericModal
		id="savingsLedgerInfoModal"
		ref="modal"
		:title="$t('forecast.savingsLedger.info.title')"
		data-testid="savings-ledger-info-modal"
	>
		<p>{{ $t("forecast.savingsLedger.info.start") }}</p>
		<p>{{ $t("forecast.savingsLedger.info.steps") }}</p>
		<p>{{ $t("forecast.savingsLedger.info.end") }}</p>
		<p>{{ $t("forecast.savingsLedger.info.estimates") }}</p>

		<!-- the real Source strings from batteryPhysics, never an invented error bar: the
		     API has none. Rendered only when the payload actually carries them. -->
		<ul v-if="physicsLines.length" class="small" data-testid="savings-ledger-info-physics">
			<li v-for="(line, i) in physicsLines" :key="i">{{ line }}</li>
		</ul>

		<p class="small text-muted">{{ $t("forecast.savingsLedger.coverageExcluded") }}</p>
		<p v-if="chainCoveragePct" class="small text-muted">
			{{ $t("forecast.savingsLedger.coverageChainDiverges", { pct: chainCoveragePct }) }}
		</p>

		<!-- Every note the API sent is here in full, none may be dropped, but collapsed:
		     they are the backend's own wording down to field names, and opening the modal
		     on them buries the plain-language explanation above. -->
		<details v-if="notes.length" class="notes" data-testid="savings-ledger-info-notes-details">
			<summary class="notes-title">{{ $t("forecast.savingsLedger.notesTitle") }}</summary>
			<ul class="notes-list small" data-testid="savings-ledger-info-notes">
				<li v-for="(note, i) in notes" :key="i">{{ note }}</li>
			</ul>
		</details>
	</GenericModal>
</template>

<script lang="ts">
import { defineComponent, type PropType } from "vue";
import GenericModal from "../Helper/GenericModal.vue";
import formatter from "@/mixins/formatter";
import type { LedgerChain } from "./savingsLedger.types";

// batteryPhysics' *Source fields are the API's own provenance, rendered verbatim except
// for clauses that cross-reference the Go source ("shared with core/site_optimizer.go's
// eta, see BatteryEta"): a source path means nothing to whoever is reading this modal.
// The provenance itself is never softened, only the code pointer goes.
//
// The filter is clause-granular, so a single-clause string naming a .go file would leave
// "" and the i18n template around it would render "Capacity 10.0 kWh - .". Absence
// rendered as punctuation is still a claim about the provenance, so fall back to the
// original, code pointer and all, rather than to nothing.
function readableSource(source: string): string {
	const readable = source
		.split(" - ")
		.filter((clause) => !clause.includes(".go"))
		.join(" - ");
	return readable || source;
}

export default defineComponent({
	name: "SavingsLedgerInfoModal",
	components: { GenericModal },
	mixins: [formatter],
	props: {
		chain: { type: Object as PropType<LedgerChain | undefined>, default: undefined },
		// already de-duplicated by the card (realised.note and chain.notes[0] are the same
		// sentence) - every caveat the API sends is rendered here, never dropped.
		notes: { type: Array as PropType<string[]>, default: () => [] },
		// percentage string, only when the chain's coverage differs from the headline's
		chainCoveragePct: { type: String, default: "" },
	},
	computed: {
		physicsLines(): string[] {
			const phys = this.chain?.batteryPhysics;
			if (!phys) return [];
			return [
				this.$t("forecast.savingsLedger.info.physicsCapacity", {
					value: `${this.fmtNumber(phys.capacityKWh, 1)} kWh`,
					source: readableSource(phys.capacitySource),
				}) as string,
				// deliberately NOT a single round-trip figure: etaC and etaD are separate
				// one-way constants in the Go source, and multiplying them here would
				// present a derived number as if the API had reported it.
				this.$t("forecast.savingsLedger.info.physicsEta", {
					charge: this.fmtPercentage(phys.etaC * 100, 0),
					discharge: this.fmtPercentage(phys.etaD * 100, 0),
					source: readableSource(phys.etaSource),
				}) as string,
				this.$t("forecast.savingsLedger.info.physicsFloor", {
					value: this.fmtPercentage(phys.floorFrac * 100, 1),
					source: readableSource(phys.floorSource),
				}) as string,
			];
		},
	},
	methods: {
		open() {
			(this.$refs["modal"] as InstanceType<typeof GenericModal> | undefined)?.open();
		},
	},
});
</script>

<style scoped>
.notes {
	margin-top: 1rem;
}
.notes-title {
	font-size: 0.6875rem;
	letter-spacing: 0.06em;
	text-transform: uppercase;
	font-weight: 700;
	color: var(--evcc-gray);
	cursor: pointer;
}
.notes-list {
	margin: 0.375rem 0 0;
	padding-left: 1.1rem;
	color: var(--evcc-gray);
	line-height: 1.5;
}
.notes-list li + li {
	margin-top: 0.25rem;
}
</style>
