import { mount, config } from "@vue/test-utils";
import { describe, expect, test } from "vite-plus/test";
import en from "../../../../i18n/en.json";
import SavingsLedgerDecisions from "./SavingsLedgerDecisions.vue";
import liveSample from "./__fixtures__/ledgerLiveSample";
import type { LedgerDecisionRow } from "./savingsLedger.types";

// same minimal $t/$te walk over en.json the neighbouring SavingsLedgerCard.test.ts uses,
// so the assertions below are on real English text
const lookup = (key: string): string | undefined => {
  const v = key.split(".").reduce<any>((o, k) => o?.[k], en);
  return typeof v === "string" ? v : undefined;
};
config.global.mocks["$t"] = (key: string, params?: Record<string, unknown>) => {
  const v = lookup(key);
  if (typeof v !== "string") return key;
  if (!params) return v;
  return Object.entries(params).reduce((s, [k, val]) => s.replaceAll(`{${k}}`, String(val)), v);
};
config.global.mocks["$te"] = (key: string) => lookup(key) !== undefined;
config.global.mocks["$i18n"] = { locale: "en-US" };

function mountDecisions(decisions: LedgerDecisionRow[]) {
  return mount(SavingsLedgerDecisions, { props: { decisions, currency: "EUR" as any } });
}

const showTable = async (wrapper: any) => {
  await wrapper.find('[data-testid="savings-ledger-decisions-table-toggle"]').trigger("click");
};

// the four rows the live build actually served: applied "unknown" against suggested
// "normal" - two spellings of "evcc held no override" - carrying slotFlowDeltaEur 0.
const legacyRows = (liveSample.decisions ?? []).filter(
  (r) => r.appliedMode === "unknown" && r.suggestedMode === "normal" && r.slotFlowDeltaEur === 0
);

const vetoRow: LedgerDecisionRow = {
  ts: "2026-08-24T09:00:00+02:00",
  appliedMode: "hold",
  suggestedMode: "charge",
  vetoReason: "payback",
  healthOk: true,
  modeChanged: false,
  slotFlowDeltaEur: 0.42,
};

const vetoUnpricedRow: LedgerDecisionRow = {
  ts: "2026-08-24T09:15:00+02:00",
  appliedMode: "hold",
  suggestedMode: "charge",
  vetoReason: "payback",
  healthOk: true,
  modeChanged: false,
};

describe("SavingsLedgerDecisions table view", () => {
  test("fixture sanity: the live sample really does carry the legacy rows", () => {
    expect(legacyRows.length).toBeGreaterThan(0);
  });

  // F1: the detail panel was gated on the outcome, but the table's delta cell was keyed
  // only on nullness - so a legacy row rendered Applied "Normal operation" | Suggested
  // "—" | Δ "€0.00": a priced veto in the same row as a column saying there was no veto.
  test("a steady row prints no delta, whatever the payload put in slotFlowDeltaEur", async () => {
    const wrapper = mountDecisions(legacyRows);
    await showTable(wrapper);

    const rows = wrapper.findAll('[data-testid^="savings-ledger-decision-row-"]');
    expect(rows.length).toBe(legacyRows.length);

    for (const row of rows) {
      expect(row.attributes("data-outcome")).toBe("steady");
      const cells = row.findAll("td");
      // 0 time, 1 applied, 2 suggested, 3 delta, 4 basis
      expect(cells[2]!.text()).toBe("—");
      expect(cells[3]!.text()).toBe("—");
      expect(cells[3]!.classes()).not.toContain("text-loss");
    }
    expect(wrapper.text()).not.toContain("0.00");
  });

  test("a real veto still prints its delta, and a costly one is still marked", async () => {
    const wrapper = mountDecisions([vetoRow]);
    await showTable(wrapper);

    const cells = wrapper.find('[data-testid^="savings-ledger-decision-row-"]').findAll("td");
    expect(cells[3]!.text()).toContain("0.42");
    expect(cells[3]!.classes()).toContain("text-loss");
  });

  test("a veto with no computable delta prints absence, not a figure", async () => {
    const wrapper = mountDecisions([vetoUnpricedRow]);
    await showTable(wrapper);

    const row = wrapper.find('[data-testid^="savings-ledger-decision-row-"]');
    expect(row.attributes("data-outcome")).toBe("vetoed-unknown");
    expect(row.findAll("td")[3]!.text()).toBe("—");
  });
});
