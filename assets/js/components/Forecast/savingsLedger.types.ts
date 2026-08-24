// View-model types for the ADR-011 savings ledger (GET /api/savingsledger?from=&to=).
//
// Mirrors core/metrics/ledger*.go's JSON-tagged response structs field-for-field -
// built from the Go source, not from the mockup's fake data (see savingsledger UI
// brief). Where a Go doc comment states an honesty rule this type's shape exists to
// carry, that's repeated here so a future edit to this file can't silently drop it.

/** core/metrics/ledger_settlement.go Settled: the same energy series priced both ways. */
export interface LedgerSettled {
  /** Each slot at its own realised price - real money only under per-slot settlement. */
  perSlot: number;
  /** Import/export volumes priced at the period's mean realised price. */
  periodAverage: number;
}

/** core/metrics/ledger_slots.go Coverage. Fraction can be 0 for two different reasons
 * (zero possible slots vs. zero valid slots) - check totalSlots before trusting it. */
export interface LedgerCoverage {
  validSlots: number;
  totalSlots: number;
  fraction: number;
}

/** core/metrics/ledger_settlement.go RealisedCost: the one measured number everything
 * else hangs off. Reads only the grid meter and the tariffs table (ADR-011 rule 2). */
export interface LedgerRealisedCost {
  settled: LedgerSettled;
  coverage: LedgerCoverage;
  /** Always noteInvoiceComparability - render, never drop. */
  note: string;
}

/** core/metrics/ledger_worlds.go WorldCost: one W0..W3 world's cost, priced both ways. */
export interface LedgerWorldCost {
  label: "W0" | "W1" | "W2" | "W3";
  settled: LedgerSettled;
}

/** core/metrics/ledger_worlds.go Contribution: Cost(previous world) - Cost(this world).
 * Summing every contribution in a chain equals Cost(W0) - Cost(actual) by construction. */
export interface LedgerContribution {
  label: "PV" | "Battery" | "Control";
  settled: LedgerSettled;
}

/** core/metrics/ledger_worlds.go ControlSplit: the W2->W3 contribution split into
 * routing (period-average diff, timing removed) and timing (what's left). Full ==
 * Contributions[2].settled.perSlot. Timing is real money only under per-slot settlement -
 * see noteTimingSettlement. */
export interface LedgerControlSplit {
  full: number;
  routing: number;
  timing: number;
}

/** core/metrics/ledger_worlds.go batteryPhysics (unexported Go type, still serialised):
 * the assumptions behind the W2 counterfactual battery, each with a Source string
 * saying whether it's device-reported or derived (fallback) from history - this is the
 * ledger's only per-figure "how sure are we" signal; there is no numeric error bar in
 * the API, unlike the mockup's fabricated "+/-" figures. */
export interface LedgerBatteryPhysics {
  capacityKWh: number;
  capacitySource: string;
  etaC: number;
  etaD: number;
  etaSource: string;
  floorFrac: number;
  floorSource: string;
  maxChargeKWh: number;
  maxDischargeKWh: number;
}

/** core/metrics/ledger_slots.go MeterResidual (the A1 diagnostic): the measured gap
 * between a period's sources and sinks, in kWh (never priced in EUR) - the noise floor
 * under every euro figure in the payload. Not expected to be zero. */
export interface LedgerMeterResidual {
  sumKWh: number;
  absSumKWh: number;
  slots: number;
}

/** core/metrics/ledger_worlds.go Chain: the full W0..W3 chain for a period, priced both
 * ways, plus the W2 battery physics and the routing/timing split when a battery is
 * configured. Notes carry ADR-011 rule 7's caveats and must be rendered, not dropped. */
export interface LedgerChain {
  worlds: LedgerWorldCost[];
  contributions: LedgerContribution[];
  coverage: LedgerCoverage;
  batteryPhysics?: LedgerBatteryPhysics;
  control?: LedgerControlSplit;
  meterResidual: LedgerMeterResidual;
  notes?: string[];
}

/** core/metrics/ledger_decisions.go DecisionRow: one control_slots row, plus (when
 * computable) the euro cost of a veto. slotFlowDeltaEur is undefined - never 0 - when
 * it isn't computable (no veto, slot outside the valid set, or no battery).
 *
 * IMPORTANT: slotFlowDeltaEur is NOT hindsight. It prices applied-vs-suggested for the
 * one vetoed slot only, at that slot's own starting SoC, and does not follow either
 * trajectory forward - a decision whose payoff only materialises in a later slot can
 * show the opposite sign here. Label it "slot-local", never "hindsight". */
export interface LedgerDecisionRow {
  /** @format date-time */
  ts: string;
  appliedMode: string;
  suggestedMode: string;
  vetoReason?: string;
  healthOk: boolean;
  /** True when AppliedMode (a point sample seconds into the slot) may not have held for
   * the full 15 minutes - the slot-local delta still simulates the full slot regardless. */
  modeChanged: boolean;
  slotFlowDeltaEur?: number;
}

/** core/metrics/ledger.go Ledger: the full ADR-011 response. Chain is undefined, with
 * chainUnavailable explaining why, when only the battery-physics part of the
 * computation failed - realised and decisions are still present in that case. */
export interface SavingsLedger {
  /** @format date-time */
  from: string;
  /** @format date-time */
  to: string;
  realised: LedgerRealisedCost;
  chain?: LedgerChain;
  chainUnavailable?: string;
  decisions: LedgerDecisionRow[];
}

/** util.ErrorAsJson: the shape of every error response this endpoint returns (400/422/500). */
export interface SavingsLedgerErrorBody {
  error: string;
  line?: number;
}
