import { shallowMount, config } from "@vue/test-utils";
import { describe, expect, test } from "vite-plus/test";
import en from "../../../../i18n/en.json";
import SavingsLedgerInfoModal from "./SavingsLedgerInfoModal.vue";

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
config.global.renderStubDefaultSlot = true;

const chain = (capacitySource: string) =>
  ({
    batteryPhysics: {
      capacityKWh: 19.32,
      capacitySource,
      etaC: 0.9,
      etaD: 0.9,
      etaSource: "constant (0.9), not derived - shared with core/site_optimizer.go's eta",
      floorFrac: 0.041,
      floorSource: "lowest observed SoC in history",
    },
  }) as any;

const physicsText = (capacitySource: string) =>
  shallowMount(SavingsLedgerInfoModal, { props: { chain: chain(capacitySource) } })
    .find('[data-testid="savings-ledger-info-physics"]')
    .findAll("li")[0]!
    .text();

describe("SavingsLedgerInfoModal provenance", () => {
  test("drops the code pointer but keeps the provenance beside it", () => {
    expect(physicsText("device-reported capacity - see core/site.go")).toBe(
      "Capacity 19.3 kWh - device-reported capacity."
    );
  });

  // the filter is clause-granular, so a provenance string that is a SINGLE clause naming a
  // .go file would leave "" and the template would render "Capacity 19.3 kWh - .".
  // Absence rendered as punctuation is still a claim about where the figure came from.
  test("a source that is nothing but a code pointer falls back to itself, not to ''", () => {
    const text = physicsText("see core/site.go");
    expect(text).not.toBe("Capacity 19.3 kWh - .");
    expect(text).toBe("Capacity 19.3 kWh - see core/site.go.");
  });
});
