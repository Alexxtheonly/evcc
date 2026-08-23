import { mount, config } from "@vue/test-utils";
import { describe, expect, test } from "vite-plus/test";
import OptimizeHeader from "./OptimizeHeader.vue";
import { CURRENCY, OptimizationStatus } from "@/types/evcc";
import en from "../../../../i18n/en.json";

// minimal $t/$te that walk en.json so tests assert on real English text, matching
// the pattern in Vehicles/Status.test.ts
const lookup = (key: string): string | undefined => {
  const v = key.split(".").reduce<any>((o, k) => o?.[k], en);
  return typeof v === "string" ? v : undefined;
};
config.global.mocks["$t"] = (key: string) => lookup(key) ?? key;
config.global.mocks["$te"] = (key: string) => lookup(key) !== undefined;
config.global.mocks["$i18n"] = { locale: "en-US" };

const baseProps = {
  updated: "",
  status: OptimizationStatus.OPTIMAL,
  horizonHours: 47,
  currency: CURRENCY.EUR,
  chargingStrategies: [],
  selectedStrategy: "",
  pending: false,
};

// ADR-011 rule 6 / B24: netCost is the solver's raw objective value (includes a
// terminal battery-value credit that hasn't actually been earned), never a settled
// grid cost - it must not be rendered through fmtMoney's currency styling (symbol,
// locale currency grouping/rounding), which would present it as an exact amount the
// user will pay.
describe("net cost display", () => {
  test("renders as a plain number with the currency code, not a currency-styled amount", () => {
    const wrapper = mount(OptimizeHeader, { props: { ...baseProps, netCost: 12.4 } });
    const text = wrapper.text();

    expect(text).toContain("12.40");
    expect(text).toContain("EUR");
    // a currency-styled Intl.NumberFormat("en-US", {style: "currency", currency: "EUR"})
    // would render "€12.40" or "EUR 12.40" with the currency-specific grouping rules -
    // this asserts no currency symbol leaked in regardless of formatting details
    expect(text).not.toContain("€");
  });

  test("negative objective (credit) keeps its sign, still no currency symbol", () => {
    const wrapper = mount(OptimizeHeader, { props: { ...baseProps, netCost: -3.8 } });
    const text = wrapper.text();

    expect(text).toContain("-3.80");
    expect(text).not.toContain("€");
  });

  test("label is not 'Net grid cost' - that implied a settled figure", () => {
    const wrapper = mount(OptimizeHeader, { props: { ...baseProps, netCost: 0 } });
    expect(wrapper.text()).not.toContain("Net grid cost");
    expect(wrapper.text()).toContain("Solver objective");
  });
});
