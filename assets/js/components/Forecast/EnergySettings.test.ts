import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, expect, test, vi } from "vite-plus/test";
import EnergySettings from "./EnergySettings.vue";
import api from "@/api";

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
    settlementMode: "simulation",
    settlementFrom: null,
  });
});
