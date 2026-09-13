import { shallowMount } from "@vue/test-utils";
import { describe, expect, test } from "vite-plus/test";
import EnergyPlanCard from "./EnergyPlanCard.vue";
import EnergyPlanChart from "./EnergyPlanChart.vue";
import { CURRENCY } from "@/types/evcc";
import { forecastTotals, socTimeline, type EnergyInsights } from "./energyIntelligence";

const settings = {
  robust: false,
  arrivals: false,
  useLearnedEfficiency: false,
  batteryWear: {},
  settlementMode: "simulation" as const,
};
const slot = {
  start: "2026-09-13T12:00:00Z",
  end: "2026-09-13T12:15:00Z",
  homeWh: 250,
  homeLowWh: 100,
  homeHighWh: 600,
  solarWh: 1800,
  solarLowWh: 500,
  solarHighWh: 2200,
  gridPrice: 0.2,
};
const global = {
  renderStubDefaultSlot: true,
  mocks: {
    $t: (key: string, args?: Record<string, unknown>) =>
      `${key} ${args ? JSON.stringify(args) : ""}`,
    $i18n: { locale: "en" },
  },
};

describe("energy plan", () => {
  test("energy-only loadpoint JSON has no initial SoC and does not claim a zero charge level", () => {
    const device = JSON.parse(
      '{"key":"loadpoint:1","name":"heater","title":"Heating","kind":"vehicle","capacityKWh":0,"plan":[{"start":"2026-09-13T12:00:00Z","chargeWh":1500,"dischargeWh":0,"soc":0}]}'
    );
    const insights: EnergyInsights = {
      updated: slot.start,
      automatic: false,
      status: "ready",
      settings,
      forecast: [slot],
      devices: [device],
    };
    const wrapper = shallowMount(EnergyPlanCard, { props: { insights }, global });
    expect(wrapper.text()).toContain("forecast.energy.energyOnly");
    expect(wrapper.text()).toContain("forecast.energy.charging");
    expect(wrapper.text()).not.toContain("forecast.energy.levelJourney");
    expect(wrapper.text()).not.toContain("forecast.energy.levelAtEnd");
  });

  test("missing initial and terminal charge levels are unknown, never zero", () => {
    const device = JSON.parse(
      '{"key":"storage","name":"storage","title":"Storage","kind":"battery","capacityKWh":10,"plan":[{"start":"2026-09-13T12:00:00Z","chargeWh":1500,"dischargeWh":0}]}'
    );
    const insights: EnergyInsights = {
      updated: slot.start,
      automatic: false,
      status: "ready",
      settings,
      forecast: [slot],
      devices: [device],
    };
    const wrapper = shallowMount(EnergyPlanCard, { props: { insights }, global });
    expect(wrapper.text()).toContain("forecast.energy.unknownLevel");
    expect(wrapper.text()).not.toContain("NaN");
    expect(socTimeline(device, [slot])).toEqual([]);
    const chart = shallowMount(EnergyPlanChart, {
      props: { devices: [device], slots: [slot], low: [30], high: [50] },
      global,
    });
    expect(chart.find(".soc-line").exists()).toBe(false);
    expect(chart.find("polygon").attributes("points")).not.toContain("NaN");
  });
  test("chart positions unequal intervals by elapsed time, not array index", () => {
    const slots = [slot, { ...slot, start: slot.end, end: "2026-09-13T13:00:00Z" }];
    const device = {
      key: "storage",
      name: "storage",
      title: "Storage",
      kind: "battery" as const,
      capacityKWh: 10,
      initialSoc: 50,
      plan: [
        { start: slot.start, chargeWh: 1000, dischargeWh: 0, soc: 60 },
        { start: slot.end, chargeWh: 1000, dischargeWh: 0, soc: 70 },
      ],
    };
    const wrapper = shallowMount(EnergyPlanChart, { props: { slots, devices: [device] }, global });
    const points = wrapper
      .get(".soc-line")
      .attributes("points")!
      .split(" ")
      .map((point) => point.split(",").map(Number));
    expect(points).toEqual([
      [0, 60],
      [166.25, 48],
      [665, 36],
    ]);
    expect(wrapper.findAll("text")).toHaveLength(0);
  });

  test("supports multiple generic batteries, empty plans and the selected currency", () => {
    const empty = {
      key: "storage-a",
      name: "storage-a",
      title: "Speicher mit einem besonders langen frei gewählten Namen",
      kind: "battery" as const,
      capacityKWh: 10,
      initialSoc: 50,
      plan: [],
    };
    const insights: EnergyInsights = {
      updated: slot.start,
      automatic: false,
      status: "ready",
      settings,
      forecast: [slot],
      devices: [empty, { ...empty, key: "storage-b", name: "storage-b", title: "Second storage" }],
    };
    const wrapper = shallowMount(EnergyPlanCard, {
      props: { insights, currency: CURRENCY.USD },
      global,
    });
    expect(wrapper.getComponent(EnergyPlanChart).props("devices")).toHaveLength(2);
    expect(wrapper.text()).toContain("forecast.energy.noPlan");
    expect(wrapper.text()).not.toContain("forecast.energy.idle");
    expect(wrapper.text()).toContain("¢/kWh");
    expect(wrapper.text()).not.toContain("€/kWh");
  });
  test("places initial charge at horizon start and solver charge at each interval end", () => {
    const device = {
      key: "storage",
      name: "storage",
      title: "Storage",
      kind: "battery" as const,
      capacityKWh: 10,
      initialSoc: 50,
      plan: [{ start: slot.start, chargeWh: 1000, dischargeWh: 0, soc: 60 }],
    };
    expect(socTimeline(device, [slot])).toEqual([
      { time: slot.start, soc: 50 },
      { time: slot.end, soc: 60 },
    ]);
  });
  test("reports a failed attempt with no fabricated plan or cash saving", () => {
    const insights: EnergyInsights = {
      updated: slot.start,
      automatic: false,
      status: "unavailable",
      reason: "missing household readings",
      settings,
    };
    const wrapper = shallowMount(EnergyPlanCard, { props: { insights, automatic: false }, global });
    expect(wrapper.get('[role="status"]').text()).toContain("missing household readings");
    expect(wrapper.text()).toContain("forecast.energy.advisory");
    expect(wrapper.find('input[type="range"]').exists()).toBe(false);
    expect(wrapper.find("table").exists()).toBe(false);
  });

  test("shows real incomplete coverage and keeps expected cars unavailable before arrival", () => {
    const insights: EnergyInsights = {
      updated: slot.start,
      automatic: false,
      status: "degraded",
      settings,
      forecast: [slot],
      profile: { source: "interpolated", samples: 2749, coveredBuckets: 94, rejectedSamples: 0 },
      devices: [
        {
          key: "vehicle:test",
          name: "test",
          title: "Car",
          kind: "expectedVehicle",
          capacityKWh: 75,
          initialSoc: 30,
          arrival: "2026-09-13T18:00:00Z",
          plan: [{ start: slot.start, chargeWh: 0, dischargeWh: 0, soc: 30 }],
        },
      ],
    };
    const wrapper = shallowMount(EnergyPlanCard, { props: { insights }, global });
    expect(wrapper.get('[data-testid="energy-profile-coverage"]').text()).toContain('"buckets":94');
    expect(wrapper.text()).toContain("forecast.energy.away");
    expect(wrapper.text()).toContain("forecast.energy.expected");
    expect(wrapper.text()).not.toContain("NaN");
  });

  test("Wh forecasts become kWh once, including genuine zero solar", () => {
    expect(forecastTotals([slot, { ...slot, solarWh: 0, solarLowWh: 0, solarHighWh: 0 }])).toEqual({
      home: 0.5,
      homeLow: 0.2,
      homeHigh: 1.2,
      solar: 1.8,
      solarLow: 0.5,
      solarHigh: 2.2,
    });
  });
});
