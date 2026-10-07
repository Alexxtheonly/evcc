import { shallowMount } from "@vue/test-utils";
import { describe, expect, test } from "vite-plus/test";
import EnergyPlanCard from "./EnergyPlanCard.vue";
import EnergyPlanChart from "./EnergyPlanChart.vue";
import BatteryControlStatus from "./BatteryControlStatus.vue";
import SolarShadingDetails from "./SolarShadingDetails.vue";
import HeatingDegreeDetails from "./HeatingDegreeDetails.vue";
import { CURRENCY } from "@/types/evcc";
import { forecastTotals, socTimeline, type EnergyInsights } from "./energyIntelligence";
import { livePlanEvening } from "./__fixtures__/energyPlanLive";

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
  stubs: { RouterLink: { template: "<a><slot /></a>" } },
  mocks: {
    $t: (key: string, args?: Record<string, unknown>) =>
      `${key} ${args ? JSON.stringify(args) : ""}`,
    $i18n: { locale: "en" },
  },
};
const plan = (overrides: Partial<EnergyInsights>): EnergyInsights => ({
  updated: slot.start,
  automatic: false,
  status: "ready",
  settings,
  forecast: [slot],
  devices: [],
  ...overrides,
});

describe("energy plan card", () => {
  test("real snapshot: headline first, one chart, day tiles, no per-interval inspector", () => {
    const wrapper = shallowMount(EnergyPlanCard, {
      props: {
        insights: livePlanEvening.optimizerInsights,
        decision: livePlanEvening.optimizerDecision,
        automatic: true,
      },
      global,
    });
    const headline = wrapper.get('[data-testid="energy-plan-headline"]').findAll("p");
    expect(headline[0]!.text()).toContain("forecast.energy.plan.headline.batteryChargeSolar");
    expect(headline[0]!.text()).toContain('"device":"Home battery 19 kWh"');
    expect(headline[0]!.text()).toContain("forecast.energy.plan.headline.batteryThen");
    expect(headline[1]!.text()).toContain('"device":"Carport (Family car)"');
    // the battery's evening phase runs to the horizon end, which is not a decision time
    expect(headline[0]!.text()).toContain("forecast.energy.plan.whenFromToPlanEnd");
    expect(headline[0]!.text()).not.toContain("00:00");
    expect(headline[1]!.text()).toContain("forecast.energy.plan.whenRange");
    expect(wrapper.get('[data-testid="energy-chart-notes"]').text()).toContain(
      "forecast.energy.plan.gridHint"
    );
    expect(wrapper.findAllComponents(EnergyPlanChart)).toHaveLength(1);
    expect(wrapper.findAll('[data-testid="energy-plan-days"] section')).toHaveLength(2);
    expect(wrapper.text()).not.toContain("forecast.energy.inspect");
    expect(wrapper.find('input[type="range"]').exists()).toBe(false);
    expect(wrapper.findAll("button").map((b) => b.text())).toEqual(["forecast.energy.configure"]);
    expect(wrapper.getComponent(BatteryControlStatus).props("compact")).toBe(true);
    expect(wrapper.text()).not.toMatch(/NaN|undefined/);
  });

  test("the schedule keeps every device's windows, levels and notes under Details", () => {
    const wrapper = shallowMount(EnergyPlanCard, {
      props: { insights: livePlanEvening.optimizerInsights },
      global,
    });
    const schedule = wrapper.get('[data-testid="energy-plan-schedule"]');
    expect(schedule.element.tagName).toBe("DETAILS");
    expect(schedule.attributes("open")).toBeUndefined();
    const groups = schedule.findAll("tbody");
    expect(groups.map((group) => group.find("th").text().split(" ")[0])).toEqual([
      "Carport",
      "Garage",
      "Home",
      "forecast.energy.gridChargingTotal",
    ]);
    expect(groups[2]!.text()).toContain("forecast.energy.discharging");
    expect(groups[2]!.text()).toContain("forecast.energy.levelJourney");
    expect(groups[3]!.findAll("tr").length).toBeGreaterThan(1);
    expect(schedule.text()).not.toContain("db:2");
  });

  test("energy-only and unknown levels stay unavailable, never zero", () => {
    const heater = JSON.parse(
      '{"key":"loadpoint:1","name":"heater","title":"Heating","kind":"vehicle","capacityKWh":0,"plan":[{"start":"2026-09-13T12:00:00Z","chargeWh":1500,"dischargeWh":0,"soc":0}]}'
    );
    const storage = JSON.parse(
      '{"key":"storage","name":"storage","title":"Storage","kind":"battery","capacityKWh":10,"plan":[{"start":"2026-09-13T12:00:00Z","chargeWh":1500,"dischargeWh":0}]}'
    );
    const wrapper = shallowMount(EnergyPlanCard, {
      props: { insights: plan({ devices: [heater, storage] }) },
      global,
    });
    const schedule = wrapper.get('[data-testid="energy-plan-schedule"]').text();
    expect(schedule).toContain("forecast.energy.energyOnly");
    expect(schedule).toContain("forecast.energy.unknownLevel");
    expect(schedule).not.toContain("0 %");
    const tile = wrapper.get('[data-testid="energy-plan-days"]').text();
    expect(tile).toContain("forecast.energy.unknownLevel");
    expect(tile).toContain("forecast.energy.plan.unavailable");
    expect(socTimeline(storage, [slot])).toEqual([]);
    expect(wrapper.text()).not.toContain("NaN");
  });

  test("expected cars keep arrival notes and the expected-arrival caveat", () => {
    const wrapper = shallowMount(EnergyPlanCard, {
      props: {
        insights: plan({
          status: "degraded",
          profile: {
            source: "interpolated",
            samples: 2749,
            coveredBuckets: 94,
            rejectedSamples: 0,
          },
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
        }),
      },
      global,
    });
    expect(wrapper.get('[data-testid="energy-profile-coverage"]').text()).toContain('"buckets":94');
    expect(wrapper.text()).toContain("forecast.energy.expected");
    expect(wrapper.text()).toContain("forecast.energy.arrival");
    expect(wrapper.text()).toContain("forecast.energy.plan.headline.idle");
  });

  test("a failed attempt shows its reason and no fabricated plan, tiles or schedule", () => {
    const wrapper = shallowMount(EnergyPlanCard, {
      props: {
        insights: plan({
          status: "unavailable",
          reason: "missing household readings",
          forecast: undefined,
        }),
        automatic: false,
      },
      global,
    });
    expect(wrapper.get('[role="status"]').text()).toContain("missing household readings");
    expect(wrapper.text()).toContain("forecast.energy.plan.headline.unavailable");
    expect(wrapper.text()).toContain("forecast.energy.advisory");
    expect(wrapper.findComponent(EnergyPlanChart).exists()).toBe(false);
    expect(wrapper.find('[data-testid="energy-plan-days"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="energy-plan-schedule"]').exists()).toBe(false);
  });

  test("prices in the headline use the selected currency", () => {
    const slots = Array.from({ length: 4 }, (_, index) => ({
      ...slot,
      start: new Date(Date.parse(slot.start) + index * 900_000).toISOString(),
      end: new Date(Date.parse(slot.start) + (index + 1) * 900_000).toISOString(),
      gridChargeMinWh: 400,
      gridChargeMaxWh: 400,
    }));
    const wrapper = shallowMount(EnergyPlanCard, {
      props: { insights: plan({ forecast: slots }), currency: CURRENCY.USD },
      global,
    });
    const text = wrapper.get('[data-testid="energy-plan-headline"]').text();
    expect(text).toContain("forecast.energy.plan.headline.gridChargePrice");
    expect(text).toContain("¢/kWh");
    // numbers and units never wrap apart
    expect(text).toMatch(/\d\u00a0kWh/);
    expect(text).toMatch(/\d\u00a0¢\/kWh/);
    expect(text).not.toContain("ct/kWh");
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

  test("solar shading details stay collapsed and unloaded until opened", async () => {
    const wrapper = shallowMount(EnergyPlanCard, {
      props: { insights: livePlanEvening.optimizerInsights },
      global,
    });
    const details = wrapper.get('[data-testid="energy-solar-shading"]');
    expect(details.attributes("open")).toBeUndefined();
    expect(details.text()).toContain("forecast.energy.shading.title");
    expect(wrapper.findComponent(SolarShadingDetails).exists()).toBe(false);
    (details.element as HTMLDetailsElement).open = true;
    await details.trigger("toggle");
    expect(wrapper.findComponent(SolarShadingDetails).exists()).toBe(true);
  });

  test("heating degree details stay collapsed and unloaded until opened", async () => {
    const wrapper = shallowMount(EnergyPlanCard, {
      props: { insights: livePlanEvening.optimizerInsights },
      global,
    });
    const details = wrapper.get('[data-testid="energy-heating-degree"]');
    expect(details.attributes("open")).toBeUndefined();
    expect(details.text()).toContain("forecast.energy.heating.title");
    expect(wrapper.findComponent(HeatingDegreeDetails).exists()).toBe(false);
    (details.element as HTMLDetailsElement).open = true;
    await details.trigger("toggle");
    expect(wrapper.findComponent(HeatingDegreeDetails).exists()).toBe(true);
  });

  test("explains each vehicle's cheap-charge band with its reference", () => {
    const vehicle = (
      key: string,
      band: NonNullable<EnergyInsights["devices"]>[number]["band"]
    ) => ({
      key,
      name: key,
      title: key,
      kind: "vehicle" as const,
      capacityKWh: 55,
      initialSoc: 36,
      plan: [{ start: slot.start, chargeWh: 0, dischargeWh: 0, soc: 36 }],
      band,
    });
    const reference = {
      price: 0.3606,
      median: 0.4007,
      source: "median" as const,
      days: 90,
      covered: 51,
      margin: 10,
      samples: 2861,
    };
    const wrapper = shallowMount(EnergyPlanCard, {
      props: {
        insights: plan({
          devices: [
            vehicle("calypso", {
              status: "waitingCheap",
              topSoc: 50,
              minRunMinutes: 30,
              reference,
              nextStart: slot.start,
              nextPrice: 0.3136,
            }),
            vehicle("luna", {
              status: "noReference",
              topSoc: 60,
              minRunMinutes: 30,
              reference: { source: "median", days: 30, margin: 10, samples: 12 },
            }),
            { ...vehicle("battery", undefined), kind: "battery" as const },
          ],
        }),
      },
      global,
    });
    const lines = wrapper.get('[data-testid="energy-plan-band"]').findAll("li");
    expect(lines).toHaveLength(2);
    expect(lines[0]!.text()).toContain("forecast.energy.band.status.waitingCheap");
    expect(lines[0]!.text()).toContain('"device":"calypso"');
    expect(lines[0]!.text()).toContain('"top":"50\u00a0%"');
    expect(lines[0]!.text()).toContain("forecast.energy.band.referenceMedian");
    expect(lines[0]!.text()).toContain('"days":90');
    expect(lines[0]!.text()).toContain('"covered":"51"');
    expect(lines[1]!.text()).toContain("forecast.energy.band.status.noReference");
    expect(lines[1]!.text()).toContain("forecast.energy.band.referenceMissing");
    expect(wrapper.text()).not.toMatch(/NaN|undefined/);
  });

  test("no band line without a configured band", () => {
    const wrapper = shallowMount(EnergyPlanCard, { props: { insights: plan({}) }, global });
    expect(wrapper.find('[data-testid="energy-plan-band"]').exists()).toBe(false);
  });
});
