// A real GET /api/savingsledger response from the owner's site, window
// 2026-08-22T05:45Z..2026-08-24T05:45Z, captured 2026-08-24.
//
// Kept verbatim (a .ts module rather than the .json it arrived as, because the repo's
// .gitignore ignores *.json outside i18n/) because it carries a genuine Control
// overspend at BOTH lenses - the case the waterfall exists to render honestly, and the
// one hand-written fixtures kept getting wrong. Do not "tidy" the figures: the
// telescoping identity between worlds and contributions is what several tests assert.

import type { SavingsLedger } from "../savingsLedger.types";

const ledgerLiveSample: SavingsLedger = {
  from: "2026-08-22T05:45:00Z",
  to: "2026-08-24T05:45:00Z",
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
          perSlot: 0.15725778607779528,
          periodAverage: 0.19270711294678636,
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
          periodAverage: 2.5238982081384327,
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
      floorSource: "lowest observed SoC in history",
      maxChargeKWh: 0.7971900000000005,
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
    },
    notes: [
      "prices only the grid tariff rate (kWh); standing charges, meter fees and VAT are not included unless baked into the tariff configuration",
      "meterResidual (kWh, not EUR) is the measured gap between this period's sources and sinks - see its own doc comment for why it is not expected to be zero; treat it as the noise floor under every euro figure above",
      "control.routing includes round-trip battery conversion losses (charge-then-discharge isn't lossless), not only which sink the energy went to",
      "control.timing reflects real money only under per-slot settlement; under period-average billing it is a diagnostic, not a figure actually paid",
      "decisions[].slotFlowDeltaEur prices only the vetoed slot itself, at its own starting SoC - a decision whose cost or benefit only materialises in a later slot can show the wrong sign here",
      "counterfactual battery rate ceiling: 0.797kWh/slot charge, 0.524kWh/slot discharge - the 99th percentile of observed single-slot energy in this battery's history (not a device spec)",
      "counterfactual battery floor: 4.1% SoC (lowest observed SoC in history) - the lowest SoC observed anywhere in this battery's history, not a configured limit; a single extra low reading can move it and therefore the control contribution materially",
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
      slotFlowDeltaEur: 0,
    },
    {
      ts: "2026-08-24T07:00:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "normal",
      healthOk: true,
      modeChanged: false,
      slotFlowDeltaEur: 0,
    },
    {
      ts: "2026-08-24T07:15:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "normal",
      healthOk: true,
      modeChanged: false,
      slotFlowDeltaEur: 0,
    },
    {
      ts: "2026-08-24T07:30:00+02:00",
      appliedMode: "unknown",
      suggestedMode: "normal",
      healthOk: true,
      modeChanged: false,
      slotFlowDeltaEur: 0,
    },
  ],
};

export default ledgerLiveSample;
