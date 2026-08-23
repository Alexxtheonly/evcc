<template>
	<div class="savings-ledger-chain" data-testid="savings-ledger-chain">
		<p class="chain-caption">{{ $t("forecast.savingsLedger.chainCaption") }}</p>

		<svg
			v-if="layout"
			class="chain-svg"
			viewBox="0 0 1000 76"
			role="img"
			:aria-label="ariaLabel"
			data-testid="savings-ledger-chain-bar"
		>
			<defs>
				<!-- "estimated" hatch: single-direction diagonal, one meaning per D-side. -->
				<pattern
					id="savingsLedgerChainHatch"
					width="9"
					height="9"
					patternUnits="userSpaceOnUse"
					patternTransform="rotate(45)"
				>
					<line
						x1="0"
						y1="0"
						x2="0"
						y2="9"
						stroke="var(--evcc-box)"
						stroke-opacity=".4"
						stroke-width="2.6"
					/>
				</pattern>
				<!-- "overspend" hatch: opposite diagonal so a segment that is BOTH estimated
				     and overspend composes into a crosshatch rather than the two patterns
				     colliding into one indistinguishable texture - carries the meaning
				     without colour, since --evcc-red and --savings-ledger-3 sit 0.4
				     luminance apart and read as one undifferentiated block under a
				     greyscale filter (D4). -->
				<pattern
					id="savingsLedgerChainOverspendHatch"
					width="9"
					height="9"
					patternUnits="userSpaceOnUse"
					patternTransform="rotate(-45)"
				>
					<line
						x1="0"
						y1="0"
						x2="0"
						y2="9"
						stroke="var(--evcc-box)"
						stroke-opacity=".4"
						stroke-width="2.6"
					/>
				</pattern>
			</defs>
			<!-- Drawn BEFORE the parts, not after: an overspend part's rectangle is drawn
			     backward over ground the cursor already covered (see chainBarLayout's doc
			     comment), which means its [x0,x1] range can fully overlap the residual's
			     own [residualX0,1] range - residualX0 IS that part's x0 when it's the last
			     segment. Found live against this site's real 2026-08-21..22 period (a
			     genuine Control overspend, -e0.64): with the residual drawn last and
			     opaque, it painted straight over the red segment AND both hatch overlays,
			     making the whole overspend invisible - a worse bug than D4's reported
			     colour-contrast issue, which assumed the segment was at least visible. -->
			<rect
				data-testid="savings-ledger-chain-residual"
				:x="layout.residualX0 * 1000"
				y="20"
				:width="Math.max(2, layout.residualWidth * 1000)"
				height="34"
				rx="3"
				class="residual-rect"
			/>
			<rect
				v-for="part in layout.parts"
				:key="part.key"
				:data-testid="`savings-ledger-chain-segment-${part.key}`"
				:data-kind="part.kind"
				:data-overspend="part.overspend"
				:x="part.x0 * 1000"
				y="20"
				:width="segmentWidth(part)"
				height="34"
				rx="3"
				:fill="fillFor(part)"
			/>
			<rect
				v-for="part in estimatedParts"
				:key="`${part.key}-hatch`"
				:x="part.x0 * 1000"
				y="20"
				:width="segmentWidth(part)"
				height="34"
				fill="url(#savingsLedgerChainHatch)"
			/>
			<rect
				v-for="part in overspendParts"
				:key="`${part.key}-overspend-hatch`"
				:data-testid="`savings-ledger-chain-segment-${part.key}-overspend-hatch`"
				:x="part.x0 * 1000"
				y="20"
				:width="segmentWidth(part)"
				height="34"
				fill="url(#savingsLedgerChainOverspendHatch)"
			/>
			<text x="0" y="70" font-size="11" class="axis-label">
				{{ fmtMoney(w0, currency, true, true) }}
				{{ $t("forecast.savingsLedger.withoutAnything") }}
			</text>
			<text
				x="1000"
				y="70"
				text-anchor="end"
				font-size="11"
				class="axis-label axis-label--strong"
			>
				{{ fmtMoney(paid, currency, true, true) }}
				{{ $t("forecast.savingsLedger.stillPaid") }}
			</text>
		</svg>

		<div class="ledger-rows" data-testid="savings-ledger-chain-rows">
			<div
				v-for="seg in segments"
				:key="seg.key"
				class="ledger-row"
				:class="{
					'ledger-row--zero': seg.kind === 'zero',
					'ledger-row--loss': seg.overspend,
				}"
				:data-testid="`savings-ledger-chain-row-${seg.key}`"
				:data-kind="seg.kind"
				:data-overspend="seg.overspend"
			>
				<span v-if="seg.kind === 'zero'" class="sw sw--zero"></span>
				<span
					v-else
					class="sw"
					:class="{ 'sw--hatch': seg.kind === 'estimated' }"
					:style="{ background: seg.overspend ? 'var(--evcc-red)' : chainColor(seg.key) }"
				></span>
				<div class="row-name">
					{{ $t(`forecast.savingsLedger.chain.${seg.key}.label`) }}
					<span
						v-if="seg.kind !== 'zero'"
						class="tag"
						:class="{ 'tag--est': seg.kind === 'estimated' }"
						data-testid="savings-ledger-chain-row-tag"
					>
						{{
							seg.kind === "estimated"
								? $t("forecast.savingsLedger.estimated")
								: $t("forecast.savingsLedger.measured")
						}}
					</span>
					<small>{{ $t(`forecast.savingsLedger.chain.${seg.key}.sub`) }}</small>
				</div>
				<!-- a "zero" row already means |eur| <= ZERO_EPSILON_EUR (savingsLedgerChain.ts) -
				     display 0 rather than the tiny signed remainder, which would otherwise round
				     to a confusing "-€0.00"/"€0.00" alongside the zero-swatch treatment. Display
				     only: the chain bar geometry above still uses the real seg.eur. -->
				<div class="row-val" data-testid="savings-ledger-chain-row-value">
					{{ fmtMoney(seg.kind === "zero" ? 0 : seg.eur, currency, true, true) }}
				</div>
			</div>

			<div
				class="ledger-row ledger-row--residual"
				data-testid="savings-ledger-chain-row-residual"
			>
				<span class="sw" style="background: var(--savings-ledger-residual)"></span>
				<div class="row-name">
					{{ $t("forecast.savingsLedger.stillPaid") }}
					<small>{{ $t("forecast.savingsLedger.stillPaidSub") }}</small>
				</div>
				<div class="row-val">{{ fmtMoney(paid, currency, true, true) }}</div>
			</div>
			<div class="ledger-row ledger-row--total" data-testid="savings-ledger-chain-row-total">
				<span></span>
				<div class="row-name">{{ $t("forecast.savingsLedger.withoutAnything") }}</div>
				<div class="row-val">{{ fmtMoney(w0, currency, true, true) }}</div>
			</div>
		</div>

		<p
			v-if="batteryPhysicsCaption"
			class="physics-note"
			data-testid="savings-ledger-battery-physics"
		>
			{{ batteryPhysicsCaption }}
		</p>
	</div>
</template>

<script lang="ts">
import { defineComponent, type PropType } from "vue";
import formatter from "@/mixins/formatter";
import type { CURRENCY } from "@/types/evcc";
import type { LedgerChain } from "./savingsLedger.types";
import {
	chainSegments,
	chainBarLayout,
	type ChainSegment,
	type ChainSegmentKey,
	type SettlementHeadline,
} from "./savingsLedgerChain";

// same validated ordinal ramp as the approved mockup (one hue, light -> dark), scoped
// to this component rather than added to the app-wide palette - nothing else uses a
// 4-step cost-chain ramp today.
const RAMP: Record<string, string> = {
	pv: "var(--savings-ledger-1)",
	battery: "var(--savings-ledger-2)",
	routing: "var(--savings-ledger-3)",
	control: "var(--savings-ledger-3)",
	timing: "var(--savings-ledger-4)",
};

export default defineComponent({
	name: "SavingsLedgerChain",
	mixins: [formatter],
	props: {
		chain: { type: Object as PropType<LedgerChain>, required: true },
		headline: { type: String as PropType<SettlementHeadline>, required: true },
		currency: { type: String as PropType<CURRENCY> },
	},
	computed: {
		segments(): ChainSegment[] {
			return chainSegments(this.chain, this.headline);
		},
		w0(): number {
			const key = this.headline === "perSlot" ? "perSlot" : "periodAverage";
			return this.chain.worlds[0]?.settled[key] ?? 0;
		},
		paid(): number {
			const key = this.headline === "perSlot" ? "perSlot" : "periodAverage";
			return this.chain.worlds[3]?.settled[key] ?? 0;
		},
		layout() {
			return chainBarLayout(this.segments, this.w0);
		},
		estimatedParts() {
			return this.layout?.parts.filter((p) => p.kind === "estimated") ?? [];
		},
		// D4: overspend needs its own visual cue beyond colour - --evcc-red and this
		// segment's normal --savings-ledger-3 fill sit 0.4 luminance apart, so a
		// greyscale filter shows one undifferentiated block otherwise.
		overspendParts() {
			return this.layout?.parts.filter((p) => p.overspend) ?? [];
		},
		ariaLabel(): string {
			return `${this.fmtMoney(this.w0, this.currency, true, true)} → ${this.fmtMoney(this.paid, this.currency, true, true)}`;
		},
		// ADR-011 rule 7: estimated figures carry their basis in the label. The API gives
		// no numeric error bar for the battery-derived segments (unlike the mockup's
		// fabricated "+/-" figures) - this renders the real provenance instead (Source
		// strings from core/metrics/ledger_worlds.go's batteryPhysics).
		batteryPhysicsCaption(): string | null {
			const phys = this.chain.batteryPhysics;
			if (!phys) return null;
			return `${this.$t("forecast.savingsLedger.estimated")}: capacity — ${phys.capacitySource}; round-trip efficiency — ${phys.etaSource}; discharge floor — ${phys.floorSource}.`;
		},
	},
	methods: {
		segmentWidth(part: { x0: number; x1: number }): number {
			return Math.max(2, (part.x1 - part.x0) * 1000);
		},
		chainColor(key: ChainSegmentKey): string {
			return RAMP[key] || "var(--savings-ledger-1)";
		},
		fillFor(part: { key: ChainSegmentKey; overspend: boolean }): string {
			return part.overspend ? "var(--evcc-red)" : this.chainColor(part.key);
		},
	},
});
</script>

<style scoped>
.savings-ledger-chain {
	--savings-ledger-1: #098a29;
	--savings-ledger-2: #076f20;
	--savings-ledger-3: #055517;
	--savings-ledger-4: #033c0f;
	--savings-ledger-residual: #dcdce3;
}
:root.dark .savings-ledger-chain {
	--savings-ledger-1: #0fc93b;
	--savings-ledger-2: #0ba631;
	--savings-ledger-3: #0a8a28;
	--savings-ledger-4: #076f20;
	--savings-ledger-residual: #33344f;
}

.chain-caption {
	font-size: 0.75rem;
	color: var(--evcc-gray);
	margin-bottom: 0.25rem;
}
.chain-svg {
	display: block;
	width: 100%;
	height: auto;
}
.residual-rect {
	fill: var(--savings-ledger-residual);
}
.axis-label {
	fill: var(--evcc-gray);
	font-family: inherit;
}
.axis-label--strong {
	fill: var(--evcc-default-text);
	font-weight: 700;
}

.ledger-rows {
	margin-top: 0.5rem;
	border-top: 1px solid var(--evcc-box-border);
}
.ledger-row {
	display: grid;
	grid-template-columns: 14px 1fr auto;
	gap: 10px;
	align-items: baseline;
	padding: 0.5rem 0;
	border-bottom: 1px solid var(--evcc-box-border);
}
.sw {
	width: 13px;
	height: 13px;
	border-radius: 3px;
	align-self: center;
	justify-self: center;
}
.sw--hatch {
	background-image: repeating-linear-gradient(
		45deg,
		rgba(255, 255, 255, 0.5) 0 2px,
		transparent 2px 4px
	);
}
.sw--zero {
	width: 2px;
	height: 15px;
	border-radius: 1px;
	background: var(--evcc-gray);
	justify-self: center;
}
.row-name {
	font-size: 0.8125rem;
}
.row-name small {
	display: block;
	color: var(--evcc-gray);
	font-size: 0.71875rem;
	line-height: 1.35;
}
.row-val {
	font-variant-numeric: tabular-nums;
	font-weight: 700;
	white-space: nowrap;
}
.ledger-row--zero .row-val {
	color: var(--evcc-gray);
}
.ledger-row--loss .row-val {
	color: var(--evcc-red);
}
.ledger-row--residual .row-val {
	color: var(--evcc-gray);
}
.ledger-row--total {
	border-bottom: none;
	padding-top: 0.625rem;
}
.ledger-row--total .row-name {
	font-weight: 700;
}
.tag {
	font-size: 0.625rem;
	letter-spacing: 0.05em;
	text-transform: uppercase;
	font-weight: 700;
	border-radius: 4px;
	padding: 1px 5px;
	margin-left: 6px;
	vertical-align: 1px;
	border: 1px solid var(--evcc-box-border);
	color: var(--evcc-gray);
	white-space: nowrap;
}
.tag--est {
	border-style: dashed;
}
.physics-note {
	margin-top: 0.5rem;
	font-size: 0.71875rem;
	color: var(--evcc-gray);
	line-height: 1.5;
}
</style>
