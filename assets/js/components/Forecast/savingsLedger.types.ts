// View-model types for GET /api/savingsledger?from=&to=, mirroring core/metrics/ledger*.go's
// JSON-tagged response structs field for field. Where a Go doc comment states a rule this
// type's shape exists to carry, it is repeated here so an edit cannot silently drop it.

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

/** core/metrics/ledger_settlement.go RealisedCost: the one measured number everything else
 * hangs off. Reads the grid meter and the tariffs table only. */
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

/** core/metrics/ledger_worlds.go ControlSplit: the W2->W3 contribution split into routing
 * (period-average diff, timing removed) and timing (what's left). Full ==
 * Contributions[2].settled.perSlot; timing is real money only under per-slot settlement. */
export interface LedgerControlSplit {
  full: number;
  routing: number;
  timing: number;
}

/** core/metrics/ledger_worlds.go batteryPhysics: the assumptions behind the W2
 * counterfactual battery, each with a Source string saying whether it is device-reported or
 * derived from history. The ledger's only per-figure "how sure are we" signal; the API
 * carries no numeric error bar, so nothing downstream may invent one. */
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

/** core/metrics/ledger_slots.go MeterResidual: the measured gap between a period's sources
 * and sinks, in kWh, the noise floor under every euro figure in the payload. Not expected
 * to be zero. */
export interface LedgerMeterResidual {
  sumKWh: number;
  absSumKWh: number;
  slots: number;
  /** absSumKWh priced at the period's mean grid rate - the noise floor in the same unit
   * as every contribution, so the two can actually be compared. A contribution smaller
   * than this is inside the noise and must not be rendered as a direction. */
  eurBand: number;
}

/** core/metrics/ledger_worlds.go W2Drift: the counterfactual battery's energy bookkeeping.
 * carriedKWh is what it was handed across gaps in the record, finalKWh where it ended
 * relative to the real pack. Both unpriced. */
export interface LedgerW2Drift {
  gaps: number;
  carriedKWh: number;
  finalKWh: number;
}

/** core/metrics/ledger_worlds.go Chain: the full W0..W3 chain for a period, priced both
 * ways, plus the W2 battery physics and the routing/timing split when a battery is
 * configured. Notes carry the payload's caveats and must be rendered, never dropped. */
export interface LedgerChain {
  worlds: LedgerWorldCost[];
  contributions: LedgerContribution[];
  coverage: LedgerCoverage;
  batteryPhysics?: LedgerBatteryPhysics;
  control?: LedgerControlSplit;
  meterResidual: LedgerMeterResidual;
  w2Drift?: LedgerW2Drift;
  notes?: string[];
  inventoryAdjusted?: {
    status: "estimated" | "partial" | "unavailable";
    reason?: string;
    control?: LedgerControlSplit;
    terminalAdjustment?: LedgerSettled;
    wearAdjustment?: number;
    assumptionsSource: string;
    valuation: string;
  };
}

/** core/metrics/ledger_decisions.go DecisionRow: one control_slots row, plus the euro cost
 * of a veto when computable. slotFlowDeltaEur is undefined, never 0, when it is not.
 *
 * slotFlowDeltaEur is NOT hindsight. It prices applied-vs-suggested for the one vetoed slot
 * only, at that slot's own starting SoC, and follows neither trajectory forward: a decision
 * whose payoff materialises in a later slot can show the opposite sign here. Label it
 * "slot-local", never "hindsight". */
export interface LedgerDecisionRow {
  /** @format date-time */
  ts: string;
  appliedMode: string;
  /** undefined, never the string "unknown", when no optimizer run produced a suggestion for
   * this slot: absence is not a sentinel. Legacy rows written before that distinction
   * existed still carry the literal "unknown". */
  suggestedMode?: string;
  vetoReason?: string;
  healthOk: boolean;
  /** True when AppliedMode (a point sample seconds into the slot) may not have held for the
   * full 15 minutes. The slot-local delta still simulates the full slot regardless. */
  modeChanged: boolean;
  slotFlowDeltaEur?: number;
  snapshotId?: string;
  outcome?: {
    status: "pending" | "completed" | "interrupted" | "unpriced";
    reason?: string;
    through?: string;
    slots: number;
    cashDeltaEur?: number;
    wearDeltaEur?: number;
    terminalEnergyDeltaKWh?: number;
    terminalValueDeltaEur?: number;
    netDeltaEur?: number;
    assumptionsSource: string;
  };
}

/** core/metrics/ledger.go Ledger. Chain is undefined, with chainUnavailable explaining why,
 * when only the battery-physics part of the computation failed: realised and decisions are
 * still present in that case. */
export interface SavingsLedger {
  /** @format date-time */
  from: string;
  /** @format date-time */
  to: string;
  realised: LedgerRealisedCost;
  /** The earliest instant the chain could have a valid slot for. NOT the same bound as an
   * ErrBeforeTariffStart refusal's `earliest`, which is where the tariff prices start.
   * @format date-time */
  chainEarliest?: string;
  chain?: LedgerChain;
  chainUnavailable?: string;
  decisions: LedgerDecisionRow[];
}

/** util.ErrorAsJson: the shape of every error response this endpoint returns (400/422/500).
 * earliest is only ever present alongside a 422 ErrBeforeTariffStart refusal whose Earliest
 * is non-zero. */
export interface SavingsLedgerErrorBody {
  error: string;
  line?: number;
  /** @format date-time */
  earliest?: string;
}
