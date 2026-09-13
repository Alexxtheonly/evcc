import { shallowMount } from "@vue/test-utils";
import { describe, expect, test, vi } from "vite-plus/test";
import EnergyPlanChart from "./EnergyPlanChart.vue";
import EnergyPlanCard from "./EnergyPlanCard.vue";
import { CURRENCY } from "@/types/evcc";
import type { EnergyDevice, EnergyForecastSlot, EnergyInsights } from "./energyIntelligence";

const slots: EnergyForecastSlot[] = [
  {
    start: "2026-09-13T12:10:00Z",
    end: "2026-09-13T12:15:00Z",
    gridPrice: -0.1,
    homeWh: 20,
    homeLowWh: 10,
    homeHighWh: 30,
    solarWh: 0,
    solarLowWh: 0,
    solarHighWh: 0,
  },
  {
    start: "2026-09-13T12:15:00Z",
    end: "2026-09-13T13:00:00Z",
    gridPrice: 0.3,
    homeWh: 20,
    homeLowWh: 10,
    homeHighWh: 30,
    solarWh: 0,
    solarLowWh: 0,
    solarHighWh: 0,
  },
];
const device: EnergyDevice = {
  key: "battery:a",
  name: "a",
  title: "Same title",
  kind: "battery",
  capacityKWh: 10,
  initialSoc: 0,
  plan: [
    { start: slots[0]!.start, chargeWh: 1000, dischargeWh: 0, soc: 10 },
    { start: slots[1]!.start, chargeWh: 0, dischargeWh: 1000, soc: 0 },
  ],
};
const global = {
  renderStubDefaultSlot: true,
  mocks: {
    $t: (key: string, args?: Record<string, unknown>) =>
      `${key} ${args ? JSON.stringify(args) : ""}`,
    $i18n: { locale: "en" },
  },
};
const create = (forecast = slots, devices = [device]) =>
  shallowMount(EnergyPlanChart, { props: { slots: forecast, devices }, global });

describe("energy chart", () => {
  test("missing prices leave a gap instead of a zero or connecting line", () => {
    const forecast = [
      slots[0]!,
      { ...slots[1]!, gridPrice: NaN },
      { ...slots[1]!, start: slots[1]!.end, end: "2026-09-13T13:15:00Z" },
    ];
    const wrapper = create(forecast);
    expect(wrapper.findAll(".price-line")).toHaveLength(2);
    expect(wrapper.html()).not.toMatch(/NaN|Infinity/);
    const unknown = create(slots.map((slot) => ({ ...slot, gridPrice: NaN })));
    expect(unknown.find(".price-row").exists()).toBe(false);
    expect(unknown.text()).toContain("forecast.energy.priceUnavailable");
  });
  test("all plots inspect actual elapsed time including partial first slot and final edge", async () => {
    const wrapper = create();
    for (const svg of wrapper.findAll("svg")) {
      vi.spyOn(svg.element, "getBoundingClientRect").mockReturnValue({
        left: 100,
        width: 500,
      } as DOMRect);
      await svg.trigger("pointermove", { clientX: 149, clientY: 300 });
      expect(wrapper.emitted("inspect")?.at(-1)).toEqual([0]);
      await svg.trigger("pointermove", { clientX: 150, clientY: 300 });
      expect(wrapper.emitted("inspect")?.at(-1)).toEqual([1]);
      await svg.trigger("pointerdown", { clientX: 600, clientY: 300, pointerType: "touch" });
      expect(wrapper.emitted("inspect")?.at(-1)).toEqual([1]);
    }
    expect(wrapper.find(".chart-tooltip").exists()).toBe(true);
    await wrapper.get('[role="group"]').trigger("pointerleave");
    expect(wrapper.find(".chart-tooltip").exists()).toBe(false);
  });

  test("keyboard navigation clamps, supports Home and End, and leaves other keys alone", async () => {
    const wrapper = create();
    const timeline = wrapper.get('[role="group"]');
    for (const [key, index] of [
      ["ArrowLeft", 0],
      ["ArrowRight", 1],
      ["End", 1],
      ["Home", 0],
    ] as const) {
      await timeline.trigger("keydown", { key });
      expect(wrapper.emitted("inspect")?.at(-1)).toEqual([index]);
    }
    const count = wrapper.emitted("inspect")?.length;
    await timeline.trigger("keydown", { key: "Tab" });
    expect(wrapper.emitted("inspect")).toHaveLength(count!);
  });

  test("negative and zero prices, initial zero SoC and interval end SoC keep their units", async () => {
    const wrapper = create();
    expect(wrapper.get(".price-line").attributes("points")).toBe("0,75 66.5,75 66.5,5 665,5");
    expect(wrapper.get(".soc-line").attributes("points")).toBe("0,120 66.5,108 665,120");
    expect(wrapper.get("rect.charge").attributes("width")).toBe("66.5");
    expect(wrapper.get("rect.discharge").attributes("x")).toBe("66.5");
    await wrapper.setProps({
      slots: slots.map((slot) => ({ ...slot, gridPrice: 0 })),
      currency: CURRENCY.USD,
    });
    expect(wrapper.get(".price-line").attributes("points")).toBe("0,75 66.5,75 66.5,75 665,75");
    expect(wrapper.text()).toContain("¢/kWh");
    expect(wrapper.html()).not.toMatch(/NaN|Infinity/);
  });

  test("legend layers and duplicate-titled devices toggle independently with stable colors", async () => {
    const wrapper = create(slots, [device, { ...device, key: "battery:b", name: "b" }]);
    const colors = wrapper.findAll(".soc-line").map((line) => line.attributes("stroke"));
    expect(colors[0]).not.toBe(colors[1]);
    await wrapper.findAll("button")[0]!.trigger("click");
    expect(wrapper.findAll(".soc-line")).toHaveLength(1);
    expect(wrapper.get(".soc-line").attributes("stroke")).toBe(colors[1]);
    expect(wrapper.findAll("button")[0]!.attributes("aria-pressed")).toBe("false");
    await wrapper.findAll('input[type="checkbox"]')[2]!.setValue(false);
    expect(wrapper.find("rect.charge").exists()).toBe(false);
    expect(wrapper.find("rect.discharge").exists()).toBe(true);
    await wrapper.findAll('input[type="checkbox"]')[0]!.setValue(false);
    expect(wrapper.find(".price-row").exists()).toBe(false);
  });

  test("grid allocation is site-level, distinguishes definite, possible, zero and missing", async () => {
    const wrapper = create();
    expect(wrapper.text()).toContain("forecast.energy.gridChargingUnavailable");
    await wrapper.setProps({
      slots: slots.map((slot, index) => ({
        ...slot,
        gridImportWh: 1000,
        gridChargeMinWh: index ? 0 : 500,
        gridChargeMaxWh: 1000,
      })),
    });
    expect(wrapper.findAll("rect.grid")).toHaveLength(1);
    expect(wrapper.findAll("rect.grid-possible")).toHaveLength(1);
    await wrapper.get("button").trigger("click");
    expect(wrapper.findAll("rect.grid")).toHaveLength(1);
    expect(wrapper.find(".activity-row:not(.grid-charge-row)").exists()).toBe(false);
    await wrapper.setProps({
      slots: slots.map((slot) => ({
        ...slot,
        gridImportWh: 0,
        gridChargeMinWh: 0,
        gridChargeMaxWh: 0,
      })),
    });
    expect(wrapper.text()).toContain("forecast.energy.noGridCharging");
    expect(wrapper.text()).not.toContain("forecast.energy.gridChargingUnavailable");
  });

  test("unknown and energy-only devices never fabricate SoC; invalid ranges are omitted", () => {
    const wrapper = create(slots, [
      { ...device, capacityKWh: 0 },
      { ...device, key: "unknown", initialSoc: undefined, plan: [] },
    ]);
    expect(wrapper.find(".soc-line").exists()).toBe(false);
    expect(wrapper.findAll(".activity-row")).toHaveLength(2);
    const chart = shallowMount(EnergyPlanChart, {
      props: { slots, devices: [device], low: [0, NaN], high: [10, 20] },
      global,
    });
    expect(chart.find("polygon").exists()).toBe(false);
  });

  test("card has no slider and chart selection updates the detailed interval and action", async () => {
    const insights: EnergyInsights = {
      updated: slots[0]!.start,
      automatic: false,
      status: "ready",
      settings: {
        robust: false,
        arrivals: false,
        useLearnedEfficiency: false,
        batteryWear: {},
        settlementMode: "simulation",
      },
      forecast: slots,
      devices: [device],
    };
    const wrapper = shallowMount(EnergyPlanCard, { props: { insights }, global });
    expect(wrapper.find('input[type="range"]').exists()).toBe(false);
    wrapper.getComponent(EnergyPlanChart).vm.$emit("inspect", 1);
    await wrapper.vm.$nextTick();
    expect(wrapper.get(".inspection").text()).toContain("forecast.energy.discharging");
    expect(wrapper.getComponent(EnergyPlanChart).props("selectedIndex")).toBe(1);
    expect(wrapper.get(".inspection").text()).toContain("0 %");
  });
});
