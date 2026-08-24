import { describe, it, expect } from "vite-plus/test";
import {
  slotState,
  deltaDirection,
  moneyDirection,
  decisionSlots,
  decisionSummary,
  modesPresent,
  axisStepHours,
  laneRuns,
  overrideRuns,
  badgedRuns,
  SLOT_MS,
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

const DAY_MS = 24 * 3600 * 1000;

describe("slotState", () => {
  it("is followed when the applied mode matches the suggestion", () => {
    expect(slotState(row())).toBe("followed");
  });

  it("is diverged when two known modes differ", () => {
    expect(slotState(row({ appliedMode: "normal", suggestedMode: "hold" }))).toBe("diverged");
  });

  it("reads a legacy unknown applied mode as normal operation, never as an override", () => {
    // api.BatteryUnknown means evcc held no override, which on a site with a battery is
    // what normal means - reading it as a veto reports a decision that was never made
    expect(slotState(row({ appliedMode: "unknown", suggestedMode: "normal" }))).toBe("followed");
    expect(slotState(row({ appliedMode: "", suggestedMode: "normal" }))).toBe("followed");
    expect(slotState(row({ appliedMode: "unknown", suggestedMode: "hold" }))).toBe("diverged");
  });

  it("is unrecorded when there is no suggestion to compare against", () => {
    expect(slotState(row({ appliedMode: "normal", suggestedMode: undefined }))).toBe("unrecorded");
  });
});

describe("deltaDirection", () => {
  it("separates a missing figure from a zero one", () => {
    // ADR-011 rule 3: absence must never read as "saved" (0)
    expect(deltaDirection(row())).toBe("unknown");
    expect(deltaDirection(row({ slotFlowDeltaEur: 0 }))).toBe("neutral");
    expect(deltaDirection(row({ slotFlowDeltaEur: -0.02 }))).toBe("saved");
    expect(deltaDirection(row({ slotFlowDeltaEur: 0.06 }))).toBe("cost");
  });
});

describe("decisionSlots", () => {
  it("sorts ascending by timestamp and tags each row's state", () => {
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
    expect(slots[0]!.state).toBe("diverged");
    expect(slots[1]!.state).toBe("followed");
  });
});

describe("decisionSummary", () => {
  it("counts each state and nets only the priced overrides", () => {
    const s = decisionSummary(
      decisionSlots([
        row(),
        row({ ts: "2026-08-23T13:15:00Z", appliedMode: "hold", slotFlowDeltaEur: 0.1 }),
        row({
          ts: "2026-08-23T13:30:00Z",
          appliedMode: "charge",
          healthOk: false,
          slotFlowDeltaEur: -0.04,
        }),
        // diverged but not priceable - counted, never netted as zero
        row({ ts: "2026-08-23T13:45:00Z", appliedMode: "hold" }),
        // a legacy "unknown" applied mode is normal operation, not an override
        row({ ts: "2026-08-23T14:00:00Z", appliedMode: "unknown", modeChanged: true }),
        row({ ts: "2026-08-23T14:15:00Z", suggestedMode: undefined }),
      ])
    );
    expect(s.slots).toBe(6);
    expect(s.suggested).toBe(5);
    expect(s.followed).toBe(2);
    expect(s.diverged).toBe(3);
    expect(s.divergedPriced).toBe(2);
    expect(s.netDeltaEur).toBeCloseTo(0.06, 10);
    expect(s.unhealthy).toBe(1);
  });

  it("leaves the net null when no override is priceable", () => {
    const s = decisionSummary(decisionSlots([row({ appliedMode: "hold" })]));
    expect(s.diverged).toBe(1);
    expect(s.netDeltaEur).toBeNull();
  });
});

describe("modesPresent", () => {
  it("lists only real modes actually on screen, in palette order", () => {
    const modes = modesPresent(
      decisionSlots([
        row({ appliedMode: "unknown", suggestedMode: "hold" }),
        row({ ts: "2026-08-23T13:15:00Z", appliedMode: "charge", suggestedMode: undefined }),
      ])
    );
    // the legacy "unknown" is listed as the normal it means, and an absent suggestion
    // contributes no mode at all
    expect(modes).toEqual(["normal", "hold", "charge"]);
  });
});

describe("axisStepHours", () => {
  it("thins the hour labels as the window grows", () => {
    expect(axisStepHours(DAY_MS)).toBe(3);
    expect(axisStepHours(3 * DAY_MS)).toBe(6);
    expect(axisStepHours(7 * DAY_MS)).toBe(12);
  });
});

describe("laneRuns", () => {
  const at = (minutes: number) => new Date(Date.parse("2026-08-23T13:00:00Z") + minutes * 60000);

  it("merges adjacent slots that share a value", () => {
    const runs = laneRuns(
      decisionSlots([
        row({ ts: at(0).toISOString() }),
        row({ ts: at(15).toISOString() }),
        row({ ts: at(30).toISOString(), appliedMode: "hold" }),
      ]),
      (slot) => slot.row.appliedMode
    );
    expect(runs.map((r) => r.value)).toEqual(["normal", "hold"]);
    expect(runs[0]!.end - runs[0]!.start).toBe(2 * SLOT_MS);
  });

  it("keeps a gap where slots are missing rather than bridging it", () => {
    const runs = laneRuns(
      decisionSlots([row({ ts: at(0).toISOString() }), row({ ts: at(60).toISOString() })]),
      (slot) => slot.row.appliedMode
    );
    expect(runs).toHaveLength(2);
  });

  it("drops the slots the mapper rejects", () => {
    const runs = laneRuns(decisionSlots([row()]), () => null);
    expect(runs).toHaveLength(0);
  });
});

describe("overrideRuns", () => {
  const at = (minutes: number) =>
    new Date(Date.parse("2026-08-23T13:00:00Z") + minutes * 60000).toISOString();

  it("sums the priced slots of a run and leaves an unpriceable run null", () => {
    const runs = overrideRuns(
      decisionSlots([
        row({ ts: at(0), appliedMode: "hold", slotFlowDeltaEur: 0.1 }),
        row({ ts: at(15), appliedMode: "hold", slotFlowDeltaEur: 0.2 }),
        row({ ts: at(30) }), // followed - ends the run
        row({ ts: at(45), appliedMode: "charge" }), // diverged, no figure
      ])
    );
    expect(runs).toHaveLength(2);
    expect(runs[0]!.slots).toBe(2);
    expect(runs[0]!.pricedSlots).toBe(2);
    expect(runs[0]!.deltaEur).toBeCloseTo(0.3, 10);
    expect(runs[1]!.deltaEur).toBeNull();
    expect(runs[1]!.pricedSlots).toBe(0);
  });

  it("reports how much of a partly-priced run the figure actually covers", () => {
    const runs = overrideRuns(
      decisionSlots([
        row({ ts: at(0), appliedMode: "hold", slotFlowDeltaEur: 0.1 }),
        row({ ts: at(15), appliedMode: "hold" }),
      ])
    );
    expect(runs[0]!.slots).toBe(2);
    expect(runs[0]!.pricedSlots).toBe(1);
    expect(runs[0]!.deltaEur).toBeCloseTo(0.1, 10);
  });
});

describe("badgedRuns", () => {
  const run = (startMin: number, deltaEur: number | null, unpricedSlots = 0) => ({
    start: startMin * 60000,
    end: (startMin + 15) * 60000,
    slots: 1 + unpricedSlots,
    pricedSlots: 1,
    deltaEur,
  });
  const MIN_GAP = 60 * 60000; // one badge width, in time, at some chart width

  it("keeps the biggest, drops the unpriceable, and never crowds two badges together", () => {
    const picked = badgedRuns(
      [run(0, 0.5), run(30, -0.9), run(60, null), run(700, 0.2), run(1200, -0.05)],
      MIN_GAP
    );
    // 0 and 30 are less than the gap apart - only the bigger of the two survives
    expect(picked.map((r) => r.deltaEur)).toEqual([-0.9, 0.2, -0.05]);
  });

  it("refuses to badge a partial sum over a whole run", () => {
    expect(badgedRuns([run(0, 0.4, 5)], MIN_GAP)).toHaveLength(0);
  });

  it("refuses to badge a wash as a saving", () => {
    expect(badgedRuns([run(0, 0), run(600, -0.002)], MIN_GAP)).toHaveLength(0);
  });

  it("respects the cap", () => {
    expect(
      badgedRuns([run(0, 1), run(400, 2), run(800, 3), run(1200, 4)], MIN_GAP, 2)
    ).toHaveLength(2);
  });
});

describe("moneyDirection", () => {
  it("separates a wash from a saving, and absence from both", () => {
    expect(moneyDirection(null)).toBe("unknown");
    expect(moneyDirection(undefined)).toBe("unknown");
    expect(moneyDirection(0)).toBe("neutral");
    expect(moneyDirection(0.004)).toBe("neutral");
    expect(moneyDirection(-0.02)).toBe("saved");
    expect(moneyDirection(0.02)).toBe("cost");
  });
});
