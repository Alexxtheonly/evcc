import { shallowMount } from "@vue/test-utils";
import { describe, expect, test } from "vite-plus/test";
import EnergyPlanCard from "./EnergyPlanCard.vue";
import { forecastTotals, type EnergyInsights } from "./energyIntelligence";

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
