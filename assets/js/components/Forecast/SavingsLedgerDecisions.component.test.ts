import { mount, config } from "@vue/test-utils";
import { describe, expect, test } from "vite-plus/test";
import en from "../../../../i18n/en.json";
import SavingsLedgerDecisions from "./SavingsLedgerDecisions.vue";
import liveSample, { constructedDecisionRows } from "./__fixtures__/ledgerLiveSample";
import type { LedgerDecisionRow } from "./savingsLedger.types";

// minimal $t/$te walk over en.json, so the assertions below are on real English text.
// $t takes named values as its ONLY second argument - the component's own plural helper
// picks a "<key>One" message rather than passing a count here, because vue-i18n's
// $t(key, plural, options) drops every placeholder but {count}.
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
  await wrapper.find('[data-testid="savingsLedgerDecisionsView-table"]').trigger("click");
};

const cellsOf = (wrapper: any) =>
  wrapper.find('[data-testid^="savings-ledger-decision-row-"]').findAll("td");

// applied "unknown" against suggested "normal": two spellings of "evcc held no
// override", with no delta at all
const legacyRows = (liveSample.decisions ?? []).filter(
  (r) => r.appliedMode === "unknown" && r.suggestedMode === "normal"
);

// the same slot as an older backend put it on the wire: the two spellings plus a priced
// "veto" of EUR 0.00 against a suggestion that was never rejected
const legacyPricedRow: LedgerDecisionRow = {
  ...legacyRows[0]!,
  slotFlowDeltaEur: 0,
};

// the capture's own rows are all steady, so every veto and euro path below runs against
// the constructed rows instead
const [vetoCostRow, vetoSavedRow, vetoUnpricedRow, noSuggestionRow] = constructedDecisionRows as [
  LedgerDecisionRow,
  LedgerDecisionRow,
  LedgerDecisionRow,
  LedgerDecisionRow,
];

describe("SavingsLedgerDecisions table view", () => {
  // the row's state and its delta cell must agree: keying one on the state and the other
  // on nullness renders a priced override beside a row that recorded no override
  test("a followed row prints no delta, whatever the payload put in slotFlowDeltaEur", async () => {
    // legacyPricedRow spreads legacyRows[0]; a re-capture that drops the legacy rows would
    // fail every assertion below pointing at the renderer rather than at the fixture
    expect(legacyRows.length).toBeGreaterThan(0);
    const wrapper = mountDecisions([legacyPricedRow]);
    await showTable(wrapper);

    const rows = wrapper.findAll('[data-testid^="savings-ledger-decision-row-"]');
    expect(rows).toHaveLength(1);
    expect(rows[0]!.attributes("data-state")).toBe("followed");

    const cells = rows[0]!.findAll("td");
    expect(cells[3]!.text()).toBe("—");
    expect(cells[3]!.classes()).not.toContain("text-danger");
    expect(wrapper.text()).not.toContain("0.00");
  });

  test("a real override still prints its delta, and a costly one is still marked", async () => {
    const wrapper = mountDecisions([vetoCostRow]);
    await showTable(wrapper);

    const cells = cellsOf(wrapper);
    expect(cells[3]!.text()).toBe("€0.04");
    expect(cells[3]!.classes()).toContain("text-danger");
  });

  test("an override that came out cheaper prints its delta without the loss colour", async () => {
    const wrapper = mountDecisions([vetoSavedRow]);
    await showTable(wrapper);

    const row = wrapper.find('[data-testid^="savings-ledger-decision-row-"]');
    expect(row.attributes("data-state")).toBe("diverged");
    const cells = row.findAll("td");
    expect(cells[3]!.text()).toBe("-€0.02");
    expect(cells[3]!.classes()).not.toContain("text-danger");
  });

  test("an override with no computable delta prints absence, not a figure", async () => {
    const wrapper = mountDecisions([vetoUnpricedRow]);
    await showTable(wrapper);

    const row = wrapper.find('[data-testid^="savings-ledger-decision-row-"]');
    expect(row.attributes("data-state")).toBe("diverged");
    expect(row.findAll("td")[3]!.text()).toBe("not computable");
  });
});

// api.BatteryUnknown stringifies to "unknown", the same token a failed run and a
// battery-less site both produce. Folding it into "followed" via normalizeMode shows
// slots where the optimizer agreed with what was applied when it said nothing at all.
describe("SavingsLedgerDecisions absent suggestions", () => {
  test("an absent suggestion is its own state, not agreement and not an override", async () => {
    const wrapper = mountDecisions([noSuggestionRow]);
    await showTable(wrapper);

    const row = wrapper.find('[data-testid^="savings-ledger-decision-row-"]');
    expect(row.attributes("data-state")).toBe("unrecorded");
    const cells = row.findAll("td");
    expect(cells[1]!.text()).toBe("No suggestion");
    expect(cells[3]!.text()).toBe("—");
  });

  test("the caption counts it as a slot without a suggestion, not as agreement", () => {
    const text = mountDecisions([noSuggestionRow])
      .find('[data-testid="savings-ledger-decisions-caption"]')
      .text();
    expect(text).toContain("1 slot without a suggestion");
    expect(text).not.toContain("followed the suggestion");
  });

  test("a genuine agreement is counted as one", () => {
    const text = mountDecisions([{ ...noSuggestionRow, suggestedMode: "hold" }])
      .find('[data-testid="savings-ledger-decisions-caption"]')
      .text();
    expect(text).toContain("1 followed the suggestion");
    expect(text).not.toContain("without a suggestion");
  });

  test("the legacy 'unknown' spelling is absence too, not an override", async () => {
    // the older wire shape for the identical slot: "unknown" rather than absent. The Go
    // read path decodes it to nil, so this only reaches the UI from an older backend -
    // reading it as an override of a suggestion that was never made invents a decision.
    const wrapper = mountDecisions([{ ...noSuggestionRow, suggestedMode: "unknown" }]);
    await showTable(wrapper);

    const row = wrapper.find('[data-testid^="savings-ledger-decision-row-"]');
    expect(row.attributes("data-state")).toBe("unrecorded");
  });
});

describe("SavingsLedgerDecisions timeline", () => {
  // GET /api/savingsledger serialises a period with no control_slots rows as [], not
  // null, and Forecast.vue mounts the card on a truthy array - so this is reachable on
  // any period predating the feature and on a fresh install. echartsChart's deep watcher
  // evaluates chartOption eagerly, regardless of the v-if gating the chart element, so an
  // unguarded chartOption threw here instead of rendering the notice.
  test("an empty period renders its notice instead of throwing", () => {
    const wrapper = mountDecisions([]);
    expect(wrapper.find('[data-testid="savings-ledger-decisions-empty"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="savings-ledger-decisions-timeline"]').exists()).toBe(false);
  });

  test("the caption names the span the decisions cover, which the period above may exceed", () => {
    const text = mountDecisions([vetoCostRow])
      .find('[data-testid="savings-ledger-decisions-caption"]')
      .text();
    expect(text).toContain("Recorded slots:");
  });

  test("the money clause counts the priced overrides, not all of them", () => {
    const wrapper = mountDecisions([vetoCostRow, vetoUnpricedRow]);
    expect(wrapper.find('[data-testid="savings-ledger-decisions-caption"]').text()).toContain(
      "2 overrode the suggestion"
    );
    const net = wrapper.find('[data-testid="savings-ledger-decisions-net"]');
    // the figure covers one of the two, and says so in the singular: "those overrides
    // cost EUR 0.04" would attribute one slot's effect to both
    expect(net.text()).toContain("the priced override cost €0.04 in its own slot");
    expect(net.text()).not.toContain("{amount}");
    expect(net.classes()).toContain("text-danger");
  });

  test("an override with no computable figure is counted but never netted as zero", () => {
    const net = mountDecisions([vetoUnpricedRow])
      .find('[data-testid="savings-ledger-decisions-net"]')
      .text();
    expect(net).toContain("no slot-local figure is computable");
    expect(net).not.toContain("0.00");
  });
});
