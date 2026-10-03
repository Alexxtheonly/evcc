import { shallowMount, flushPromises } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, test, vi } from "vite-plus/test";
import EnergyPricesCard from "./EnergyPricesCard.vue";
import DateNavigator from "../Sessions/DateNavigator.vue";
import api from "@/api";
import en from "../../../../i18n/en.json";
import de from "../../../../i18n/de.json";
import type { EnergyPriceBucket, EnergyPriceReport } from "./energyPrices";
import { siteCalendarDate } from "./energyPrices";

vi.mock("@/api", () => ({ default: { get: vi.fn() } }));

const covered = { validSlots: 3, totalSlots: 4, fraction: 0.75 };
const bucket = (date: string, imported: number, cost: number): EnergyPriceBucket => ({
  date,
  from: `${date}T00:00:00+01:00`,
  to: `${date}T01:00:00+01:00`,
  grid: {
    importKWh: imported,
    importCost: cost,
    weightedPrice: cost / imported,
    coverage: covered,
  },
  consumption: {
    consumptionKWh: 8,
    importKWh: imported,
    exportKWh: 2,
    importCost: cost,
    exportRevenue: 0.2,
    netCost: cost - 0.2,
    effectivePrice: (cost - 0.2) / 8,
    coverage: { validSlots: 2, totalSlots: 4, fraction: 0.5 },
  },
  worlds: [],
  evTiming: { status: "unavailable", includedSessions: 0, excludedSessions: 2 },
});
const report = (): EnergyPriceReport => {
  const totals = bucket("2026-03-01", 4, 0.6);
  totals.date = "2026-03";
  const world = {
    ...totals.consumption,
    coverage: { validSlots: 1, totalSlots: 4, fraction: 0.25 },
  };
  totals.worlds = ["W0", "W1", "W2", "W3"].map((label) => ({
    ...world,
    label: label as "W0" | "W1" | "W2" | "W3",
    weightedPrice: 0.12,
  }));
  return {
    month: "2026-03",
    timezone: "Europe/Berlin",
    from: "2026-02-28T23:00:00Z",
    to: "2026-03-31T22:00:00Z",
    evaluatedTo: "2026-03-02T01:00:00Z",
    earliest: "2025-12-01T00:00:00Z",
    totals,
    days: [bucket("2026-03-01", 3, 0.3), bucket("2026-03-02", 1, 0.3)],
    comparison: { status: "available" },
    evTiming: { status: "unavailable", includedSessions: 0, excludedSessions: 2 },
  };
};
const translations = (messages: typeof en | typeof de) => ({
  renderStubDefaultSlot: true,
  mocks: {
    $t: (key: string, params?: Record<string, unknown>) => {
      let value: unknown = messages;
      for (const part of key.split(".")) value = (value as Record<string, unknown>)?.[part];
      return Object.entries(params || {}).reduce(
        (text, [key, value]) => text.replaceAll(`{${key}}`, String(value)),
        typeof value === "string" ? value : key
      );
    },
    $i18n: { locale: messages === en ? "en" : "de" },
  },
});
const mountCard = () => shallowMount(EnergyPricesCard, { global: translations(en) });

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime("2026-03-02T01:10:00Z");
  vi.mocked(api.get).mockReset();
  vi.mocked(api.get).mockResolvedValue({ data: report() });
});
afterEach(() => vi.useRealTimers());

describe("energy prices", () => {
  test("uses the server current month and changes day without another replay", async () => {
    const wrapper = mountCard();
    await flushPromises();
    expect(api.get).toHaveBeenCalledWith("energyprices", { params: undefined });
    expect(wrapper.vm.date).toBe("2026-03-02");
    await wrapper.vm.selectDate({ year: 2026, month: 3, day: 1 });
    expect(api.get).toHaveBeenCalledTimes(1);
    expect(wrapper.vm.bucket?.grid.weightedPrice).toBeCloseTo(0.1);
    const window = wrapper.emitted("update:window")?.at(-1)?.[0];
    expect(window).toEqual({
      from: new Date("2026-02-28T23:00:00Z"),
      to: new Date("2026-03-01T00:00:00Z"),
    });
    wrapper.unmount();
  });

  test("month uses energy-weighted totals instead of averaging daily prices", async () => {
    const wrapper = mountCard();
    await flushPromises();
    await wrapper.vm.setPeriod("month");
    expect(wrapper.vm.bucket?.grid.weightedPrice).toBe(0.15);
    expect(wrapper.text()).toContain("€0.15/kWh");
    expect(wrapper.text()).toContain("75% coverage");
    expect(wrapper.text()).toContain("50% coverage");
    expect(wrapper.text()).toContain("25% coverage");
    expect(wrapper.text()).not.toContain("forecast.energyPrices.");
    expect(wrapper.text()).toContain("Grid only");
    expect(wrapper.text()).toContain("Solar + simple battery");
    expect(wrapper.text()).toContain("Actual operation");
    expect(api.get).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  test("site calendar handles month boundary independently of the viewer timezone", async () => {
    vi.setSystemTime("2026-02-28T23:30:00Z");
    expect(siteCalendarDate(new Date(), "America/Los_Angeles")).toBe("2026-02-28");
    expect(siteCalendarDate(new Date(), "Europe/Berlin")).toBe("2026-03-01");
    const wrapper = mountCard();
    await flushPromises();
    expect(wrapper.vm.date).toBe("2026-03-01");
    expect(wrapper.getComponent(DateNavigator).props("today")!.getDate()).toBe(1);
    wrapper.unmount();
  });

  test.each([en, de])(
    "keeps negative values and distinguishes missing prices from free imports",
    async (messages) => {
      const data = report();
      data.days[1]!.grid.weightedPrice = 0;
      data.days[1]!.grid.importCost = 0;
      data.days[1]!.consumption.effectivePrice = -0.02;
      data.days[1]!.inventoryBenefit = -0.13;
      vi.mocked(api.get).mockResolvedValue({ data });
      const wrapper = shallowMount(EnergyPricesCard, { global: translations(messages) });
      await flushPromises();
      expect(wrapper.text()).toContain(messages === en ? "€0.00/kWh" : "0,00 €/kWh");
      expect(wrapper.text()).toContain(messages === en ? "-€0.02/kWh" : "-0,02 €/kWh");
      expect(wrapper.text()).toContain(messages === en ? "-€0.13" : "-0,13 €");
      await wrapper.setData({
        report: {
          ...data,
          days: [
            data.days[0],
            { ...data.days[1], grid: { importKWh: 0, importCost: 0, coverage: covered } },
          ],
        },
      });
      expect(wrapper.text()).toContain(messages.forecast.energyPrices.noImport);
      expect(wrapper.text()).toContain(messages.forecast.energyPrices.unavailable);
      wrapper.unmount();
    }
  );

  test("missing readings never display fabricated cash totals", async () => {
    const data = report();
    const emptyCoverage = { validSlots: 0, totalSlots: 4, fraction: 0 };
    data.days[1]!.grid = { importKWh: 0, importCost: 0, coverage: emptyCoverage };
    data.days[1]!.consumption = {
      ...data.days[1]!.consumption,
      effectivePrice: undefined,
      coverage: emptyCoverage,
    };
    vi.mocked(api.get).mockResolvedValue({ data });
    const wrapper = mountCard();
    await flushPromises();
    expect(wrapper.text()).toContain("No valid readings");
    expect(wrapper.text()).not.toContain("€0.00");
    wrapper.unmount();
  });

  test("an old response cannot overwrite the chosen month and failed requests hide legacy data", async () => {
    const wrapper = mountCard();
    await flushPromises();
    let finish!: (value: unknown) => void;
    vi.mocked(api.get).mockReturnValueOnce(
      new Promise((resolve) => {
        finish = resolve;
      })
    );
    const first = wrapper.vm.fetchReport("2026-01");
    const december = { ...report(), month: "2025-12", days: [] };
    vi.mocked(api.get).mockResolvedValueOnce({ data: december });
    await wrapper.vm.fetchReport("2025-12");
    finish({ data: report() });
    await first;
    await flushPromises();
    expect(wrapper.vm.report?.month).toBe("2025-12");
    vi.mocked(api.get).mockRejectedValueOnce(new Error("unavailable"));
    await wrapper.vm.fetchReport("2026-02");
    await flushPromises();
    expect(wrapper.vm.requestedMonth).toBe("2026-02");
    expect(wrapper.emitted("update:window")?.at(-1)?.[0]).toBeNull();
    expect(wrapper.text()).not.toContain("€0.15/kWh");
    wrapper.unmount();
  });
  test("null arrays from empty history remain unavailable and future selections do not fetch", async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { ...report(), days: null } });
    const wrapper = mountCard();
    await flushPromises();
    expect(wrapper.text()).toContain("No completed intervals");
    await wrapper.vm.selectDate({ year: 2027, month: 1, day: 1 });
    expect(api.get).toHaveBeenCalledTimes(1);
    expect(wrapper.vm.date).toBe("2026-03-02");
    wrapper.unmount();
  });

  test("EV estimates use the selected connection cohort and remain separate from cash prices", async () => {
    const data = report();
    data.days[1]!.evTiming = {
      status: "partial",
      reason: "tariff_only_connection_cohort",
      includedSessions: 1,
      excludedSessions: 2,
      actualTariffCost: 2,
      immediateTariffCost: 1,
      savings: -1,
    };
    vi.mocked(api.get).mockResolvedValue({ data });
    const wrapper = mountCard();
    await flushPromises();
    expect(wrapper.text()).toContain("-€1.00");
    expect(wrapper.text()).toContain("1 included sessions · 2 excluded sessions");
    expect(wrapper.text()).not.toContain("tariff_only_connection_cohort");
    expect(wrapper.vm.bucket?.grid.weightedPrice).toBe(0.3);
    expect(wrapper.vm.reason("unrecognized_internal_reason")).toBe(
      "The historical evidence is insufficient for this estimate."
    );
    wrapper.unmount();
  });
});

test.each([en, de])(
  "inventory estimates remain visible when unavailable and explain historical assumptions",
  async (messages) => {
    const data = report();
    data.comparison.inventoryAdjusted = {
      status: "unavailable",
      reason: "missing_soc",
      segments: 1,
      estimatedEndpoints: 2,
      assumptionsSource: "mixed_snapshots_and_explicit_legacy_assumptions",
      valuation: "",
    };
    vi.mocked(api.get).mockResolvedValue({ data });
    const wrapper = shallowMount(EnergyPricesCard, { global: translations(messages) });
    await flushPromises();
    const detail = wrapper.get('[data-testid="inventory-estimate"]');
    expect(detail.text()).toContain(messages.forecast.energyPrices.unavailable);
    expect(detail.text()).toContain(messages.forecast.energy.inventoryDetails.missingSoc);
    expect(detail.text()).toContain(messages.forecast.energy.inventoryDetails.mixed);
    expect(detail.text()).toContain(
      messages.forecast.energyPrices.estimatedEndpoints.replace("{count}", "2")
    );
    expect(detail.text()).not.toMatch(/(?:€0|0[,.]00)/);
    expect(detail.text()).not.toContain("missing_soc");
    expect(detail.text()).not.toContain("forecast.energy");

    delete data.comparison.inventoryAdjusted;
    vi.mocked(api.get).mockResolvedValue({ data: { ...data } });
    await wrapper.vm.fetchReport();
    await flushPromises();
    expect(wrapper.get('[data-testid="inventory-estimate"]').text()).toContain(
      messages.forecast.energyPrices.reasons.unavailable
    );
    expect(detail.text()).not.toMatch(/(?:€0|0[,.]00)/);
    wrapper.unmount();
  }
);
