import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, expect, test, vi } from "vite-plus/test";
import EnergySettings from "./EnergySettings.vue";
import api from "@/api";
import type { EnergyDevice } from "./energyIntelligence";

vi.mock("@/api", () => ({ default: { get: vi.fn(), put: vi.fn() } }));
afterEach(() => vi.resetAllMocks());

const mountSettings = () =>
  mount(EnergySettings, { global: { mocks: { $t: (key: string) => key } } });

test("cannot overwrite settings after a failed read", async () => {
  vi.mocked(api.get).mockRejectedValue(new Error("unavailable"));
  const wrapper = mountSettings();
  wrapper.get("details").element.open = true;
  await wrapper.get("details").trigger("toggle");
  await flushPromises();
  expect(wrapper.get("button").attributes("disabled")).toBeDefined();
  await wrapper.get("form").trigger("submit");
  expect(api.put).not.toHaveBeenCalled();
});

test("declared efficiencies convert percentages once and preserve the meter plane", async () => {
  vi.mocked(api.get).mockResolvedValue({
    data: {
      robust: false,
      arrivals: false,
      useLearnedEfficiency: false,
      batteryWear: {},
      batteryEnergyPlane: { battery: "dc" },
      batteryEfficiency: { battery: { chargeEfficiency: 0.95, dischargeEfficiency: 0.92 } },
      settlementMode: "simulation",
      settlementFrom: null,
    },
  });
  vi.mocked(api.put).mockResolvedValue({});
  const device: EnergyDevice = {
    key: "battery",
    name: "battery",
    title: "Battery",
    kind: "battery",
    capacityKWh: 19.32,
    initialSoc: 50,
    plan: [],
  };
  const wrapper = mount(EnergySettings, {
    props: { devices: [device] },
    global: { mocks: { $t: (key: string) => key } },
  });
  wrapper.get("details").element.open = true;
  await wrapper.get("details").trigger("toggle");
  await flushPromises();
  expect((wrapper.get("#energy-chargeEfficiency-battery").element as HTMLInputElement).value).toBe(
    "95"
  );
  await wrapper.get("#energy-chargeEfficiency-battery").setValue("96");
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).toHaveBeenCalledWith(
    "config/energyintelligence",
    expect.objectContaining({
      batteryEnergyPlane: { battery: "dc" },
      batteryEfficiency: { battery: { chargeEfficiency: 0.96, dischargeEfficiency: 0.92 } },
    })
  );
  vi.mocked(api.put).mockClear();
  await wrapper.get("#energy-dischargeEfficiency-battery").setValue("");
  await wrapper.get("form").trigger("submit");
  expect(api.put).not.toHaveBeenCalled();
  expect(wrapper.get('[role="alert"]').text()).toBe("forecast.energy.invalidEfficiency");
});

test("saving reporting preferences sends no automatic-control command", async () => {
  vi.mocked(api.get).mockResolvedValue({
    data: {
      robust: false,
      arrivals: false,
      useLearnedEfficiency: false,
      batteryWear: { "db:11": 0 },
      settlementMode: "simulation",
      settlementFrom: null,
    },
  });
  vi.mocked(api.put).mockResolvedValue({});
  const wrapper = mountSettings();
  wrapper.get("details").element.open = true;
  await wrapper.get("details").trigger("toggle");
  await flushPromises();
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).toHaveBeenCalledExactlyOnceWith("config/energyintelligence", {
    robust: false,
    arrivals: false,
    useLearnedEfficiency: false,
    batteryWear: { "db:11": 0 },
    batteryEnergyPlane: {},
    batteryEfficiency: {},
    settlementMode: "simulation",
    settlementFrom: null,
  });
});
