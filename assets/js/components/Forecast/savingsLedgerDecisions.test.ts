import { describe, it, expect } from "vite-plus/test";
import {
  decisionOutcome,
  decisionSlots,
  modeLabelKey,
  normalizeMode,
  unhealthyVetoCount,
} from "./savingsLedgerDecisions";
import type { LedgerDecisionRow } from "./savingsLedger.types";

function row(overrides: Partial<LedgerDecisionRow> = {}): LedgerDecisionRow {
  return {
    ts: "2026-08-23T13:00:00Z",
    appliedMode: "normal",
    suggestedMode: "normal",
    healthOk: true,
    modeChanged: false,
    ...overrides,
  };
}

describe("decisionOutcome", () => {
  it("is steady when nothing was vetoed", () => {
    expect(decisionOutcome(row())).toBe("steady");
  });

  // D6: api.BatteryUnknown means "no override in effect", which is what BatteryNormal
  // means. Rows written before the backend folded the two still carry "unknown" on one
  // side and "normal" on the other - a difference of wire strings, not of behaviour, and
  // never a veto.
  it("is steady when one side says unknown and the other normal", () => {
    expect(decisionOutcome(row({ appliedMode: "unknown", suggestedMode: "normal" }))).toBe(
      "steady"
    );
    expect(decisionOutcome(row({ appliedMode: "unknown", suggestedMode: "unknown" }))).toBe(
      "steady"
    );
  });

  it("still reads a real veto against an unknown applied mode", () => {
    expect(decisionOutcome(row({ appliedMode: "unknown", suggestedMode: "charge" }))).toBe(
      "vetoed-unknown"
    );
  });

  it("is vetoed-unknown when a veto happened but slotFlowDeltaEur is not computable", () => {
    // ADR-011 rule 3: absence must never read as "saved" (0)
    expect(decisionOutcome(row({ appliedMode: "normal", suggestedMode: "hold" }))).toBe(
      "vetoed-unknown"
    );
  });

  it("is vetoed-cost when the applied mode cost more than the rejected suggestion, slot-locally", () => {
    expect(
      decisionOutcome(row({ appliedMode: "normal", suggestedMode: "hold", slotFlowDeltaEur: 0.06 }))
    ).toBe("vetoed-cost");
  });

  it("is vetoed-saved when the slot-local delta is negative or exactly zero", () => {
    expect(
      decisionOutcome(
        row({ appliedMode: "hold", suggestedMode: "normal", slotFlowDeltaEur: -0.02 })
      )
    ).toBe("vetoed-saved");
    expect(
      decisionOutcome(row({ appliedMode: "hold", suggestedMode: "normal", slotFlowDeltaEur: 0 }))
    ).toBe("vetoed-saved");
  });
});

describe("normalizeMode", () => {
  it("folds an absent or unknown mode onto normal, so the wire word never reaches a label", () => {
    expect(normalizeMode("unknown")).toBe("normal");
    expect(normalizeMode("")).toBe("normal");
    expect(normalizeMode(undefined)).toBe("normal");
    expect(modeLabelKey("unknown")).toBe("forecast.optimizer.modeNormal");
  });

  it("leaves a real mode alone", () => {
    expect(normalizeMode("holdcharge")).toBe("holdcharge");
    expect(modeLabelKey("hold")).toBe("forecast.optimizer.modeHold");
  });
});

describe("decisionSlots", () => {
  it("sorts ascending by timestamp and tags each row's outcome", () => {
    const rows = [
      row({ ts: "2026-08-23T13:30:00Z" }),
      row({
        ts: "2026-08-23T13:00:00Z",
        appliedMode: "hold",
        suggestedMode: "normal",
        slotFlowDeltaEur: -0.1,
      }),
    ];
    const slots = decisionSlots(rows);
    expect(slots.map((s) => s.row.ts)).toEqual(["2026-08-23T13:00:00Z", "2026-08-23T13:30:00Z"]);
    expect(slots[0]!.outcome).toBe("vetoed-saved");
    expect(slots[1]!.outcome).toBe("steady");
  });
});

describe("unhealthyVetoCount", () => {
  it("counts only vetoed rows recorded while unhealthy", () => {
    const rows = [
      row({ appliedMode: "hold", suggestedMode: "normal", healthOk: false }),
      row({ appliedMode: "normal", suggestedMode: "normal", healthOk: false }), // steady, not counted
      row({ appliedMode: "unknown", suggestedMode: "normal", healthOk: false }), // same, not counted
      row({ appliedMode: "charge", suggestedMode: "normal", healthOk: true }), // healthy, not counted
    ];
    expect(unhealthyVetoCount(rows)).toBe(1);
  });
});
