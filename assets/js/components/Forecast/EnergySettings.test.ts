import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, expect, test, vi } from "vite-plus/test";
import EnergySettings from "./EnergySettings.vue";
import GenericModal from "../Helper/GenericModal.vue";
import api from "@/api";
import type { EnergyInsights, EnergySettings as Settings } from "./energyIntelligence";

vi.mock("@/api", () => ({ default: { get: vi.fn(), put: vi.fn() } }));
afterEach(() => vi.resetAllMocks());
const defaults = (): Settings => ({
  robust: false,
  arrivals: false,
  useLearnedEfficiency: false,
  batteryWear: {},
  settlementMode: "simulation",
  settlementFrom: null,
});
const global = {
  stubs: { GenericModal: { template: "<div><slot /></div>" } },
  mocks: {
    $t: (key: string, args?: Record<string, unknown>) =>
      `${key}${args ? JSON.stringify(args) : ""}`,
    $i18n: { locale: "en" },
  },
};
const mountSettings = (
  devices = [{ name: "storage", title: "Home battery" }],
  economics: EnergyInsights["economics"] = []
) => mount(EnergySettings, { props: { devices, economics }, global });
const open = async (wrapper: ReturnType<typeof mountSettings>) => {
  wrapper.getComponent(GenericModal).vm.$emit("open");
  await flushPromises();
};
const currentEconomics = {
  name: "storage",
  chargeEfficiency: 0.9,
  dischargeEfficiency: 0.9,
  source: "default",
};

test("cannot overwrite settings after failed read and allows retry", async () => {
  vi.mocked(api.get)
    .mockRejectedValueOnce(new Error("unavailable"))
    .mockResolvedValue({ data: defaults() });
  const wrapper = mountSettings();
  await open(wrapper);
  expect(wrapper.get('button[type="submit"]').attributes("disabled")).toBeDefined();
  await wrapper.get("form").trigger("submit");
  expect(api.put).not.toHaveBeenCalled();
  await open(wrapper);
  expect(wrapper.get('button[type="submit"]').attributes("disabled")).toBeUndefined();
});

test("default values are explained without creating manual overrides or assuming free wear", async () => {
  vi.mocked(api.get).mockResolvedValue({ data: defaults() });
  vi.mocked(api.put).mockResolvedValue({});
  const wrapper = mountSettings(undefined, [currentEconomics]);
  await open(wrapper);
  expect(wrapper.get('[data-testid="energy-effective-storage"]').text()).toContain(
    '"roundtrip":"81.0"'
  );
  expect(wrapper.get("#energy-efficiency-mode-storage").element).toHaveProperty(
    "value",
    "automatic"
  );
  expect(wrapper.find("#energy-chargeEfficiency-storage").exists()).toBe(false);
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).toHaveBeenCalledExactlyOnceWith("config/energyintelligence", {
    ...defaults(),
    batteryEnergyPlane: {},
    batteryEfficiency: {},
  });
});

test("Custom prefills effective efficiencies; later plan updates do not replace edited draft", async () => {
  vi.mocked(api.get).mockResolvedValue({ data: defaults() });
  vi.mocked(api.put).mockResolvedValue({});
  const wrapper = mountSettings(undefined, [currentEconomics]);
  await open(wrapper);
  await wrapper.get("#energy-efficiency-mode-storage").setValue("custom");
  expect(wrapper.get("#energy-chargeEfficiency-storage").element).toHaveProperty("value", "90");
  await wrapper.get("#energy-chargeEfficiency-storage").setValue("96");
  await wrapper.setProps({ economics: [{ ...currentEconomics, chargeEfficiency: 0.85 }] });
  expect(wrapper.get("#energy-chargeEfficiency-storage").element).toHaveProperty("value", "96");
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).toHaveBeenCalledWith(
    "config/energyintelligence",
    expect.objectContaining({
      batteryEfficiency: { storage: { chargeEfficiency: 0.96, dischargeEfficiency: 0.9 } },
    })
  );
});

test("unchanged configured values preserve billing instant, meter plane and explicit zero wear", async () => {
  const settings: Settings = {
    ...defaults(),
    robust: true,
    arrivals: true,
    useLearnedEfficiency: true,
    batteryWear: { storage: 0 },
    batteryEnergyPlane: { storage: "dc" },
    batteryEfficiency: { storage: { chargeEfficiency: 0.95, dischargeEfficiency: 0.92 } },
    settlementMode: "interval",
    settlementFrom: "2026-04-01T13:00:00+05:30",
  };
  vi.mocked(api.get).mockResolvedValue({ data: settings });
  vi.mocked(api.put).mockResolvedValue({});
  const wrapper = mountSettings();
  await open(wrapper);
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).toHaveBeenCalledExactlyOnceWith("config/energyintelligence", settings);
});

test("Automatic explicitly removes custom efficiencies without labelling old custom values as defaults", async () => {
  vi.mocked(api.get).mockResolvedValue({
    data: {
      ...defaults(),
      batteryEfficiency: { storage: { chargeEfficiency: 0.95, dischargeEfficiency: 0.92 } },
    },
  });
  vi.mocked(api.put).mockResolvedValue({});
  const wrapper = mountSettings(undefined, [
    { ...currentEconomics, chargeEfficiency: 0.95, source: "configured" },
  ]);
  await open(wrapper);
  await wrapper.get("#energy-efficiency-mode-storage").setValue("automatic");
  expect(wrapper.get('[data-testid="energy-effective-storage"]').text()).toBe(
    "forecast.energy.efficiencyPending"
  );
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).toHaveBeenCalledWith(
    "config/energyintelligence",
    expect.objectContaining({ batteryEfficiency: {} })
  );
});

test("new devices and repeated state frames preserve draft and absent economics stay unknown", async () => {
  vi.mocked(api.get).mockResolvedValue({ data: defaults() });
  const wrapper = mountSettings([]);
  await open(wrapper);
  await wrapper.setProps({
    devices: [{ name: "late", title: "A very long generic battery title" }],
  });
  await wrapper.get("#energy-efficiency-mode-late").setValue("custom");
  await wrapper.get("#energy-chargeEfficiency-late").setValue("95");
  await wrapper.setProps({
    devices: [
      { name: "late", title: "A very long generic battery title" },
      { name: "second", title: "Second storage" },
    ],
  });
  expect(wrapper.get("#energy-chargeEfficiency-late").element).toHaveProperty("value", "95");
  expect(wrapper.find("#energy-chargeEfficiency-second").exists()).toBe(false);
  expect(wrapper.get('[data-testid="energy-effective-second"]').text()).toBe(
    "forecast.energy.efficiencyPending"
  );
});

test("close and reopen discards draft and re-reads persisted settings", async () => {
  vi.mocked(api.get).mockResolvedValue({ data: defaults() });
  const wrapper = mountSettings();
  await open(wrapper);
  await wrapper.get("#energy-robust").setValue(true);
  wrapper.getComponent(GenericModal).vm.$emit("close");
  await open(wrapper);
  expect(wrapper.get("#energy-robust").element).toHaveProperty("checked", false);
  expect(api.put).not.toHaveBeenCalled();
  expect(api.get).toHaveBeenCalledTimes(2);
});

test("a read completing after close cannot authorize a stale save", async () => {
  let resolve!: (value: { data: Settings }) => void;
  vi.mocked(api.get).mockReturnValue(
    new Promise((done) => {
      resolve = done;
    })
  );
  const wrapper = mountSettings();
  wrapper.getComponent(GenericModal).vm.$emit("open");
  wrapper.getComponent(GenericModal).vm.$emit("close");
  resolve({ data: defaults() });
  await flushPromises();
  await wrapper.get("form").trigger("submit");
  expect(api.put).not.toHaveBeenCalled();
});

test("empty interval date selects Billing and cannot save while section was hidden", async () => {
  vi.mocked(api.get).mockResolvedValue({ data: { ...defaults(), settlementMode: "interval" } });
  const wrapper = mountSettings();
  await open(wrapper);
  await wrapper.get("form").trigger("submit");
  expect(api.put).not.toHaveBeenCalled();
  expect(wrapper.get('[role="alert"]').text()).toBe("forecast.energy.invalidBillingDate");
  expect(wrapper.get("#energy-settlement-from").isVisible()).toBe(true);
});

test("partial custom efficiencies and failed writes keep the form editable", async () => {
  vi.mocked(api.get).mockResolvedValue({ data: defaults() });
  vi.mocked(api.put).mockRejectedValue(new Error("write unavailable"));
  const wrapper = mountSettings();
  await open(wrapper);
  await wrapper.get("#energy-efficiency-mode-storage").setValue("custom");
  await wrapper.get("#energy-chargeEfficiency-storage").setValue("92");
  await wrapper.get("form").trigger("submit");
  expect(api.put).not.toHaveBeenCalled();
  await wrapper.get("#energy-dischargeEfficiency-storage").setValue("93");
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(wrapper.get('[role="alert"]').text()).toContain("write unavailable");
  expect(wrapper.get('button[type="submit"]').attributes("disabled")).toBeUndefined();
});
