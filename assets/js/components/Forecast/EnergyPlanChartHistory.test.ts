import { shallowMount, type VueWrapper } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, test, vi } from "vite-plus/test";
import type {
  EnergyDevice,
  EnergyForecastSlot,
  HistorySlot,
  PlanHistory,
} from "./energyIntelligence";

const setOption = vi.fn();
vi.mock("./echarts", () => ({
  echarts: {
    use: vi.fn(),
    init: vi.fn((el: HTMLElement) => ({
      setOption,
      resize: vi.fn(),
      dispose: vi.fn(),
      getDom: () => el,
    })),
  },
}));

const { default: EnergyPlanChart } = await import("./EnergyPlanChart.vue");

const planSlot = (start: string, end: string): EnergyForecastSlot => ({
  start,
  end,
  gridPrice: 0.4,
  homeWh: 250,
  homeLowWh: 100,
  homeHighWh: 400,
  solarWh: 0,
  solarLowWh: 0,
  solarHighWh: 0,
});
const slots = [
  planSlot("2026-10-05T10:00:00Z", "2026-10-05T10:15:00Z"),
  planSlot("2026-10-05T10:15:00Z", "2026-10-05T10:30:00Z"),
];
const battery: EnergyDevice = {
  key: "battery:db:2",
  name: "db:2",
  title: "Home battery",
  kind: "battery",
  capacityKWh: 10,
  initialSoc: 50,
  plan: slots.map((slot) => ({ start: slot.start, chargeWh: 0, dischargeWh: 0, soc: 50 })),
};
const measured = (start: string, end: string, values: Partial<HistorySlot> = {}): HistorySlot => ({
  start,
  end,
  measured: true,
  homeWh: 300,
  solarWh: 0,
  gridImportWh: 1800,
  gridExportWh: 0,
  batteryChargeWh: 1500,
  batteryDischargeWh: 0,
  loadpointWh: 0,
  gridChargeWh: 1500,
  gridPrice: 0.21,
  feedInPrice: 0.08,
  gridCost: 0.378,
  feedInRevenue: 0,
  ...values,
});
const starts = ["09:00", "09:15", "09:30", "09:45", "10:00"].map((t) => `2026-10-05T${t}:00Z`);
const history: PlanHistory = {
  from: starts[0]!,
  to: starts[4]!,
  forecast: [
    measured(starts[0]!, starts[1]!, {
      planned: {
        snapshotId: 1,
        snapshot: "2026-10-05T08:45:30Z",
        devices: [
          {
            key: battery.key,
            title: "Home battery",
            kind: "battery",
            chargeWh: 0,
            dischargeWh: 0,
            soc: 20,
          },
        ],
      },
    }),
    measured(starts[1]!, starts[2]!, { flagged: true }),
    measured(starts[2]!, starts[3]!),
    measured(starts[3]!, starts[4]!),
  ],
  devices: [
    {
      key: battery.key,
      name: "db:2",
      title: "BYD",
      kind: "battery",
      capacityKWh: 10,
      plan: [20, 26, 34, 42].map((soc, i) => ({
        start: starts[i]!,
        chargeWh: 1500,
        dischargeWh: 0,
        soc,
      })),
    },
  ],
};
const global = {
  mocks: {
    $t: (key: string, args?: Record<string, unknown>) =>
      `${key}${args ? JSON.stringify(args) : ""}`,
    $i18n: { locale: "en" },
  },
};

// The plot spans x = 40 … 440: one hour of measurements (40 … 306) then 30 min of plan.
const WRAP = { left: 0, top: 100, width: 488, height: 400, right: 488, bottom: 500 } as DOMRect;
let wrapper: VueWrapper;
function mount(props: Record<string, unknown> = {}) {
  wrapper = shallowMount(EnergyPlanChart, {
    attachTo: document.body,
    props: { slots, devices: [battery], history, historyHours: 1, ...props },
    global,
  });
  vi.spyOn(wrapper.get(".chart-wrap").element, "getBoundingClientRect").mockReturnValue(WRAP);
  return wrapper;
}
const tooltip = () => wrapper.find('[data-testid="plan-tooltip"]');
const lastOption = () => setOption.mock.calls.at(-1)![0];

beforeEach(() => {
  setOption.mockClear();
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    callback(0);
    return 0;
  });
});
afterEach(() => {
  wrapper?.unmount();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("energy plan chart with measured history", () => {
  test("the window selector offers Off to 48 h and reports a choice", async () => {
    mount({ historyHours: 24 });
    const select = wrapper.get<HTMLSelectElement>('[data-testid="plan-history-hours"]');
    expect(select.findAll("option").map((option) => option.element.value)).toEqual([
      "0",
      "6",
      "12",
      "24",
      "48",
    ]);
    expect(select.element.value).toBe("24");
    expect(wrapper.get(`label[for="${select.attributes("id")}"]`).text()).toBe(
      "forecast.energy.plan.history.label"
    );
    await select.setValue("0");
    expect(wrapper.emitted("update:historyHours")).toEqual([[0]]);
  });

  test("the axis starts a window before now, with Measured and Plan labels around now", async () => {
    mount();
    await wrapper.vm.$nextTick();
    const option = lastOption();
    expect(option.xAxis[0].min).toBe(Date.parse(starts[0]!));
    expect(option.series.map((s: { id: string }) => s.id)).toContain("past-home");
    // happy-dom has no layout; give the chart the width the bounding box reports
    (wrapper.vm as unknown as { width: number }).width = 488;
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-testid="era-measured"]').text()).toBe(
      "forecast.energy.plan.history.measured"
    );
    // now sits two thirds into the 400 px plot: 40 + 266.67, labels 6 px either side
    expect(wrapper.get('[data-testid="era-plan"]').attributes("style")).toContain("left: 312.66");
    expect(wrapper.get('[data-testid="era-measured"]').attributes("style")).toContain(
      "right: 187.33"
    );
  });

  test("Off draws the plan alone and shows no era labels", async () => {
    mount({ historyHours: 0 });
    await wrapper.vm.$nextTick();
    expect(lastOption().xAxis[0].min).toBe(Date.parse(slots[0]!.start));
    expect(lastOption().series.some((s: { id: string }) => s.id.startsWith("past-"))).toBe(false);
    expect(wrapper.find('[data-testid="era-measured"]').exists()).toBe(false);
  });

  test("hovering a measured slot marks it measured and compares it with the plan in force", async () => {
    mount();
    const wrap = wrapper.get(".chart-wrap");
    await wrap.trigger("pointermove", { pointerType: "mouse", clientX: 60, clientY: 300 });
    await wrapper.vm.$nextTick();
    const text = tooltip().text();
    expect(text).toContain("forecast.energy.plan.history.measured");
    expect(text).toContain("forecast.energy.plan.history.gridImport");
    expect(text).toContain("7.2 kW");
    expect(text).toContain('forecast.energy.plan.history.level{"start":"20 %","end":"26 %"}');
    expect(text).toContain('forecast.energy.plan.history.fromGrid{"energy":"1.50 kWh"}');
    expect(text).toContain("€0.38");
    expect(wrapper.find('[data-testid="plan-tooltip-planned"]').exists()).toBe(true);
    expect(wrapper.get('[data-testid="plan-tooltip-planned"]').text()).toContain(
      "forecast.energy.idle"
    );
    // the next slot has no covering snapshot, and a flagged reading says so
    await wrap.trigger("pointermove", { pointerType: "mouse", clientX: 120, clientY: 300 });
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[data-testid="plan-tooltip-planned"]').exists()).toBe(false);
    expect(tooltip().text()).toContain("forecast.energy.plan.history.flagged");
    // a plan slot keeps the plan popup
    await wrap.trigger("pointermove", { pointerType: "mouse", clientX: 420, clientY: 300 });
    await wrapper.vm.$nextTick();
    expect(tooltip().text()).not.toContain("forecast.energy.plan.history.measured");
    await wrap.trigger("pointerleave", { pointerType: "mouse" });
    expect(tooltip().exists()).toBe(false);
  });

  test("a tap on a measured slot survives the finger lifting off", async () => {
    mount();
    const wrap = wrapper.get(".chart-wrap");
    await wrap.trigger("pointerdown", { pointerType: "touch", clientX: 60, clientY: 300 });
    await wrap.trigger("pointerup", { pointerType: "touch", clientX: 60, clientY: 300 });
    await wrap.trigger("pointerleave", { pointerType: "touch" });
    expect(tooltip().text()).toContain("forecast.energy.plan.history.measured");
    expect(tooltip().classes()).toContain("interactive");
  });

  test("keyboard stepping starts at now and walks back through measured slots", async () => {
    mount();
    const wrap = wrapper.get(".chart-wrap");
    await wrap.trigger("keydown", { key: "ArrowRight" });
    expect(tooltip().text()).not.toContain("forecast.energy.plan.history.measured");
    await wrap.trigger("keydown", { key: "ArrowLeft" });
    expect(tooltip().text()).toContain("forecast.energy.plan.history.measured");
    expect(wrapper.get('[aria-live="polite"]').text()).toContain(
      "forecast.energy.plan.history.measured"
    );
    await wrap.trigger("keydown", { key: "Home" });
    expect(wrapper.find('[data-testid="plan-tooltip-planned"]').exists()).toBe(true);
    await wrap.trigger("keydown", { key: "End" });
    expect(tooltip().text()).not.toContain("forecast.energy.plan.history.measured");
  });

  test("a failed history request is reported beside the selector", () => {
    mount({ history: undefined, historyError: true, historyHours: 24 });
    expect(wrapper.get('[data-testid="plan-history-error"]').text()).toBe(
      "forecast.energy.plan.history.unavailable"
    );
  });
});
