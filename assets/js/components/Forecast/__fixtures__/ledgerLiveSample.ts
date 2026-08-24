// A real GET /api/savingsledger response from the owner's site, window
// 2026-08-22T05:45Z..2026-08-24T05:45Z, re-captured 2026-08-24 against the current
// build (the earlier capture predates meterResidual.eurBand, chain.w2Drift and
// chainEarliest; every euro figure below is unchanged from it - only the counterfactual
// battery's rate ceiling moved, because the p99 is taken over a history that has since
// grown).
//
// Kept verbatim (a .ts module rather than the .json it arrived as, because the repo's
// .gitignore ignores *.json outside i18n/) because it carries a negative Control
// contribution at BOTH lenses that is SMALLER than the period's own measurement noise
// (-EUR 0.326 against an eurBand of EUR 0.625) - the case the card must render as "too
// small to call", not as "the controller cost you money". Do not "tidy" the figures: the
// telescoping identity between worlds and contributions is what several tests assert,
// and the noise band is deliberately larger than the figure it sits under.

import type { LedgerDecisionRow, SavingsLedger } from "../savingsLedger.types";

const ledgerLiveSample: SavingsLedger = {
  from: "2026-08-22T07:45:00+02:00",
  to: "2026-08-24T07:45:00+02:00",
  chainEarliest: "2026-08-21T12:45:00+02:00",
  realised: {
    settled: {
      perSlot: 0.4835212054956298,
      periodAverage: 0.6019048490884557,
    },
    coverage: {
      validSlots: 84,
      totalSlots: 192,
      fraction: 0.4375,
    },
    note: "prices only the grid tariff rate (kWh); standing charges, meter fees and VAT are not included unless baked into the tariff configuration",
  },
  chain: {
    worlds: [
      {
        label: "W0",
        settled: {
          perSlot: 5.84949222251276,
          periodAverage: 6.6655763935982275,
        },
      },
      {
        label: "W1",
        settled: {
          perSlot: 2.9390474678946483,
          periodAverage: 2.7166053210852192,
        },
      },
      {
        label: "W2",
        settled: {
          perSlot: 0.15725778607779525,
          periodAverage: 0.1927071129467863,
        },
      },
      {
        label: "W3",
        settled: {
          perSlot: 0.4835212054956298,
          periodAverage: 0.6019048490884557,
        },
      },
    ],
    contributions: [
      {
        label: "PV",
        settled: {
          perSlot: 2.9104447546181116,
          periodAverage: 3.9489710725130083,
        },
      },
      {
        label: "Battery",
        settled: {
          perSlot: 2.781789681816853,
          periodAverage: 2.523898208138433,
        },
      },
      {
        label: "Control",
        settled: {
          perSlot: -0.32626341941783454,
          periodAverage: -0.4091977361416693,
        },
      },
    ],
    coverage: {
      validSlots: 84,
      totalSlots: 192,
      fraction: 0.4375,
    },
    batteryPhysics: {
      capacityKWh: 19.32,
      capacitySource: "device-reported capacity, persisted",
      etaC: 0.9,
      etaD: 0.9,
      etaSource:
        "constant (0.9), not derived - shared with core/site_optimizer.go's eta, see BatteryEta",
      floorFrac: 0.040999999999999995,
      floorSource: "lowest observed SoC in history (fallback: no configured minimum recorded)",
      maxChargeKWh: 0.7186599999999999,
      maxDischargeKWh: 0.52416,
    },
    control: {
      full: -0.32626341941783454,
      routing: -0.4091977361416693,
      timing: 0.08293431672383478,
    },
    meterResidual: {
      sumKWh: -0.19973772143128032,
      absSumKWh: 1.8703931973967187,
      slots: 84,
      eurBand: 0.6251744729127164,
    },
    w2Drift: {
      gaps: 6,
      carriedKWh: 5.7154531111111115,
      finalKWh: -2.6442116682567143,
    },
    notes: [
      "prices only the grid tariff rate (kWh); standing charges, meter fees and VAT are not included unless baked into the tariff configuration",
      "meterResidual is the measured gap between this period's sources and sinks - see its own doc comment for why it is not expected to be zero; its eurBand is that gap priced at the period's mean grid rate, and any figure above smaller than it is inside the noise, not a direction",
      "control.routing includes round-trip battery conversion losses (charge-then-discharge isn't lossless), not only which sink the energy went to",
      "control.timing reflects real money only under per-slot settlement; under period-average billing it is a diagnostic, not a figure actually paid",
      "decisions[].slotFlowDeltaEur prices only the vetoed slot itself, at its own starting SoC - a decision whose cost or benefit only materialises in a later slot can show the wrong sign here",
      "counterfactual battery rate ceiling: 0.719kWh/slot charge, 0.524kWh/slot discharge - the 99th percentile of observed single-slot energy in this battery's history (not a device spec)",
      "counterfactual battery floor: 4.1% SoC (lowest observed SoC in history (fallback: no configured minimum recorded)) - no configured minimum is on record for this battery, so this is the lowest SoC ever observed, which is a behaviour of the controller being measured; a single extra low reading can move it and therefore the control contribution materially",
      "counterfactual battery: anchored to the measured charge once, at the period's first slot, then simulated - across 6 gap(s) in the record it was handed the +5.72kWh the real pack itself moved while unmeasured (unpriced in this world exactly as it is in what you paid), and it ends the period -2.64kWh from the real pack, energy neither cost figure values",
      "periodAverage prices are the mean over the 84 valid slots only (43.8% coverage) - excluded slots are not assumed to average out evenly",
      "EV charge timing is not attributed to any measure - PV/Battery/Control all price a loadpoint's energy at when it was actually drawn, so shifting a charge to a cheaper slot shows EUR 0 of value here even when it saved money",
      "feed-in price is EUR 0.00 for every slot in this period, so every export credit in this payload is EUR 0.00 - that reflects the configured/observed feed-in rate, not a computation error",
    ],
  },
  decisions: [
    {
      ts: "2026-08-23T21:00:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-23T21:15:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-23T21:30:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-23T21:45:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-23T22:00:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-23T22:15:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-23T22:30:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-23T22:45:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-23T23:00:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-23T23:15:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-23T23:30:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-23T23:45:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T00:00:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T00:15:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T00:30:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T00:45:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T01:00:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T01:15:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T01:30:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T01:45:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T02:00:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T02:15:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T02:30:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T02:45:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T03:00:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T03:15:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T03:30:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T03:45:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T04:00:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T04:15:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T04:45:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T05:00:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T05:15:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T05:30:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T05:45:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "unknown",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T06:00:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "normal",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T06:15:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "normal",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T06:30:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "normal",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T06:45:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "normal",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T07:00:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "normal",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T07:15:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "normal",
      healthOk: true,
      modeChanged: false,
    },
    {
      ts: "2026-08-24T07:30:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "normal",
      healthOk: true,
      modeChanged: false,
    },
  ],
};

/**
 * NOT CAPTURED - constructed. Every one of the 42 rows above is `appliedMode: "unknown"`
 * against `suggestedMode` "unknown" or "normal", i.e. 42 steady rows: the capture carries
 * no veto, no vetoReason and no slotFlowDeltaEur at all, so on its own it exercises none
 * of the euro-printing or veto paths in SavingsLedgerDecisions.vue.
 *
 * These four are hand-built to the shape core/metrics/ledger_decisions.go's DecisionRow
 * actually emits, one per outcome the capture is missing:
 *  - suggestedMode is OMITTED, never "unknown", when no run produced a suggestion
 *    (decodeSuggestedMode maps the legacy token to nil on the read path);
 *  - slotFlowDeltaEur is OMITTED whenever DecisionDeltas could not price the slot (no
 *    battery physics, or the slot is outside the valid slot set) - never a 0 sentinel;
 *  - it is positive when the APPLIED mode cost more than the rejected suggestion would
 *    have within that slot, negative when it cost less.
 *
 * The timestamps continue the capture's own timeline and the magnitudes are on its scale;
 * they are plausible, not observed. Do not fold them into `decisions` above - that array
 * is a verbatim capture and several tests count it.
 */
export const constructedDecisionRows: LedgerDecisionRow[] = [
  // a veto whose slot could be priced, and where the controller's choice cost money
  {
    ts: "2026-08-24T07:45:00+02:00",
    appliedMode: "normal",
    suggestedMode: "charge",
    vetoReason: "payback",
    healthOk: true,
    modeChanged: false,
    slotFlowDeltaEur: 0.0412,
  },
  // the same, the other way: the veto was the cheaper choice in that slot
  {
    ts: "2026-08-24T08:00:00+02:00",
    appliedMode: "hold",
    suggestedMode: "charge",
    vetoReason: "liveRate",
    healthOk: true,
    modeChanged: false,
    slotFlowDeltaEur: -0.0176,
  },
  // a real veto in a slot the ledger could not price - absence, never a zero
  {
    ts: "2026-08-24T08:15:00+02:00",
    appliedMode: "hold",
    suggestedMode: "charge",
    vetoReason: "damping",
    healthOk: true,
    modeChanged: false,
  },
  // no optimizer run produced a suggestion for this slot at all
  {
    ts: "2026-08-24T08:30:00+02:00",
    appliedMode: "hold",
    healthOk: true,
    modeChanged: false,
  },
];

export default ledgerLiveSample;
