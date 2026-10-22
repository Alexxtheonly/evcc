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
    heatingDegree: "auto", // absent means auto
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
    heatingDegree: "off",
  };
  vi.mocked(api.get).mockResolvedValue({ data: settings });
  vi.mocked(api.put).mockResolvedValue({});
  const wrapper = mountSettings();
  await open(wrapper);
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).toHaveBeenCalledExactlyOnceWith("config/energyintelligence", settings);
});

test("heat pump model and meter are carried unchanged through a save", async () => {
  const settings: Settings = { ...defaults(), heatPumpModel: "on", heatPumpMeter: "db:19" };
  vi.mocked(api.get).mockResolvedValue({ data: settings });
  vi.mocked(api.put).mockResolvedValue({});
  const wrapper = mountSettings();
  await open(wrapper);
  await wrapper
    .get('[data-testid="energy-heatingDegree"] #energy-heatingDegree-off')
    .setValue(true);
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).toHaveBeenCalledExactlyOnceWith(
    "config/energyintelligence",
    expect.objectContaining({ heatPumpModel: "on", heatPumpMeter: "db:19", heatingDegree: "off" })
  );

  // settings saved before the fields existed stay without them: the server reads absent as shadow
  vi.mocked(api.put).mockClear();
  vi.mocked(api.get).mockResolvedValue({ data: defaults() });
  await open(wrapper);
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  const sent = vi.mocked(api.put).mock.calls[0]![1] as Settings;
  expect(sent).not.toHaveProperty("heatPumpModel");
  expect(sent).not.toHaveProperty("heatPumpMeter");
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

test("invalid wear reveals and focuses the named battery field even from Planning", async () => {
  vi.mocked(api.get).mockResolvedValue({ data: defaults() });
  const wrapper = mount(EnergySettings, {
    attachTo: document.body,
    props: {
      devices: [
        { name: "first", title: "First battery" },
        { name: "second", title: "Second battery" },
      ],
    },
    global,
  });
  try {
    await open(wrapper);
    const input = wrapper.get("#energy-wear-second");
    await input.setValue("-1");
    expect((input.element.closest("details") as HTMLDetailsElement).open).toBe(false);
    await wrapper.get("form").trigger("submit");
    await flushPromises();
    expect(wrapper.get('[role="alert"]').text()).toContain("Second battery");
    expect((input.element.closest("details") as HTMLDetailsElement).open).toBe(true);
    expect(document.activeElement).toBe(input.element);
    expect(api.put).not.toHaveBeenCalled();
  } finally {
    wrapper.unmount();
  }
});

test("native badInput is not interpreted as clearing an optional wear value", async () => {
  vi.mocked(api.get).mockResolvedValue({
    data: { ...defaults(), batteryWear: { storage: 0.035 } },
  });
  vi.mocked(api.put).mockResolvedValue({});
  const wrapper = mountSettings();
  await open(wrapper);
  const input = wrapper.get("#energy-wear-storage");
  await input.setValue("");
  Object.defineProperty(input.element, "validity", {
    configurable: true,
    value: { badInput: true },
  });
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).not.toHaveBeenCalled();
  expect(wrapper.get('[role="alert"]').text()).toContain("Home battery");
  Object.defineProperty(input.element, "validity", {
    configurable: true,
    value: { badInput: false },
  });
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).toHaveBeenCalledExactlyOnceWith(
    "config/energyintelligence",
    expect.objectContaining({ batteryWear: {} })
  );
});

test("a partially edited native date cannot save the previous complete date", async () => {
  vi.mocked(api.get).mockResolvedValue({
    data: { ...defaults(), settlementMode: "interval", settlementFrom: "2026-04-01T00:00:00Z" },
  });
  const wrapper = mountSettings();
  await open(wrapper);
  const input = wrapper.get("#energy-settlement-from");
  Object.defineProperty(input.element, "validity", { value: { badInput: true } });
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).not.toHaveBeenCalled();
  expect(wrapper.get('[role="alert"]').text()).toBe("forecast.energy.invalidBillingDate");
  expect(input.isVisible()).toBe(true);
});

test("invalid wear survives blur after an earlier validation rerender and can be explicitly cleared", async () => {
  vi.mocked(api.get).mockResolvedValue({ data: defaults() });
  vi.mocked(api.put).mockResolvedValue({});
  const wrapper = mountSettings();
  await open(wrapper);
  const input = wrapper.get("#energy-wear-storage");
  await input.setValue("-1");
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).not.toHaveBeenCalled();
  await input.setValue("");
  Object.defineProperty(input.element, "validity", {
    configurable: true,
    value: { badInput: true },
  });
  await input.trigger("input");
  Object.defineProperty(input.element, "validity", {
    configurable: true,
    value: { badInput: false },
  });
  await input.trigger("blur");
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).not.toHaveBeenCalled();
  await wrapper
    .findAll("button")
    .find((button) => button.attributes("aria-label")?.startsWith("forecast.energy.clearWear"))!
    .trigger("click");
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).toHaveBeenCalledExactlyOnceWith(
    "config/energyintelligence",
    expect.objectContaining({ batteryWear: {} })
  );
});

test("focus returns to the invoker only after the modal has finished closing", async () => {
  vi.mocked(api.get).mockResolvedValue({ data: defaults() });
  const invoker = document.createElement("button");
  invoker.textContent = "Configure";
  document.body.append(invoker);
  const wrapper = mount(EnergySettings, { attachTo: document.body, global });
  try {
    invoker.focus();
    await open(wrapper);
    const cancel = wrapper
      .findAll("button")
      .find((button) => button.text() === "forecast.energy.cancel")!;
    (cancel.element as HTMLButtonElement).focus();
    wrapper.getComponent(GenericModal).vm.$emit("close");
    await flushPromises();
    expect(document.activeElement).not.toBe(invoker);
    wrapper.getComponent(GenericModal).vm.$emit("closed");
    await flushPromises();
    expect(document.activeElement).toBe(invoker);
    expect(wrapper.emitted("closed")).toHaveLength(1);
  } finally {
    wrapper.unmount();
    invoker.remove();
  }
});

test("solar shading toggle is off by default and saves both directions", async () => {
  vi.mocked(api.get).mockResolvedValue({ data: defaults() });
  vi.mocked(api.put).mockResolvedValue({});
  const wrapper = mountSettings();
  await open(wrapper);
  const toggle = wrapper.get("#energy-solarShading");
  expect((toggle.element as HTMLInputElement).checked).toBe(false);
  expect(wrapper.text()).toContain("forecast.energy.solarShadingHint");
  await toggle.setValue(true);
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).toHaveBeenCalledExactlyOnceWith(
    "config/energyintelligence",
    expect.objectContaining({ solarShading: true, robust: false })
  );

  vi.mocked(api.put).mockClear();
  vi.mocked(api.get).mockResolvedValue({ data: { ...defaults(), solarShading: true } });
  await open(wrapper);
  expect((wrapper.get("#energy-solarShading").element as HTMLInputElement).checked).toBe(true);
  await wrapper.get("#energy-solarShading").setValue(false);
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).toHaveBeenCalledExactlyOnceWith(
    "config/energyintelligence",
    expect.objectContaining({ solarShading: false })
  );
});

test("heating degree: three options with explanations, auto when absent, saved as chosen", async () => {
  vi.mocked(api.get).mockResolvedValue({ data: defaults() });
  vi.mocked(api.put).mockResolvedValue({});
  const wrapper = mountSettings();
  await open(wrapper);
  const radio = (mode: string) => wrapper.get(`#energy-heatingDegree-${mode}`);
  expect((radio("auto").element as HTMLInputElement).checked).toBe(true);
  expect((radio("on").element as HTMLInputElement).checked).toBe(false);
  const text = wrapper.get('[data-testid="energy-heatingDegree"]').text();
  for (const mode of ["auto", "on", "off"]) {
    expect(text).toContain(`forecast.energy.heatingDegreeModes.${mode}`);
    expect(text).toContain(`forecast.energy.heatingDegreeHints.${mode}`);
  }
  await radio("on").setValue(true);
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).toHaveBeenCalledExactlyOnceWith(
    "config/energyintelligence",
    expect.objectContaining({ heatingDegree: "on", robust: false })
  );

  vi.mocked(api.put).mockClear();
  vi.mocked(api.get).mockResolvedValue({ data: { ...defaults(), heatingDegree: "on" } });
  await open(wrapper);
  expect((radio("on").element as HTMLInputElement).checked).toBe(true);
  await radio("off").setValue(true);
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).toHaveBeenCalledExactlyOnceWith(
    "config/energyintelligence",
    expect.objectContaining({ heatingDegree: "off" })
  );
});

test("cheap band reference: entered values are saved as numbers, empty ones stay absent", async () => {
  vi.mocked(api.get).mockResolvedValue({ data: { ...defaults(), cheapBandDays: 90 } });
  vi.mocked(api.put).mockResolvedValue({});
  const wrapper = mountSettings();
  await open(wrapper);
  expect(wrapper.get("#energy-cheapBandDays").element).toHaveProperty("value", "90");
  await wrapper.get("#energy-cheapBandDays").setValue(""); // back to the 30-day default
  await wrapper.get("#energy-cheapBandMargin").setValue("0");
  await wrapper.get("#energy-cheapBandPrice").setValue("0.28");
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  const payload = vi.mocked(api.put).mock.calls[0]![1] as Settings;
  expect(payload).not.toHaveProperty("cheapBandDays");
  expect(payload.cheapBandMargin).toBe(0);
  expect(payload.cheapBandPrice).toBe(0.28);
});

test("cheap band reference: out-of-range values are refused before saving", async () => {
  vi.mocked(api.get).mockResolvedValue({ data: defaults() });
  const wrapper = mountSettings();
  await open(wrapper);
  for (const [field, value] of [
    ["cheapBandMargin", "95"],
    ["cheapBandPrice", "50"],
  ] as const) {
    await wrapper.get(`#energy-${field}`).setValue(value);
    await wrapper.get("form").trigger("submit");
    await flushPromises();
    expect(api.put).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain("forecast.energy.cheapBand.invalid");
    await wrapper.get(`#energy-${field}`).setValue("");
  }
});

test("battery feeding band charging is off unless switched on", async () => {
  vi.mocked(api.get).mockResolvedValue({ data: defaults() });
  vi.mocked(api.put).mockResolvedValue({});
  const wrapper = mountSettings();
  await open(wrapper);
  const toggle = wrapper.get("#energy-cheapBandFromBattery");
  expect((toggle.element as HTMLInputElement).checked).toBe(false);
  await toggle.setValue(true);
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).toHaveBeenCalledWith(
    "config/energyintelligence",
    expect.objectContaining({ cheapBandFromBattery: true })
  );
});

test("reference window offers 30 days (default) and 90 days", async () => {
  vi.mocked(api.get).mockResolvedValue({ data: defaults() });
  vi.mocked(api.put).mockResolvedValue({});
  const wrapper = mountSettings();
  await open(wrapper);
  const select = wrapper.get("#energy-cheapBandDays");
  expect(select.findAll("option").map((o) => o.element.value)).toEqual(["", "90"]);
  await select.setValue("90");
  await wrapper.get("form").trigger("submit");
  await flushPromises();
  expect(api.put).toHaveBeenCalledWith(
    "config/energyintelligence",
    expect.objectContaining({ cheapBandDays: 90 })
  );
});
