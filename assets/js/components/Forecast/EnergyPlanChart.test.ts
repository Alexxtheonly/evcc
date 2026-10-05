import { shallowMount, type VueWrapper } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, test, vi } from "vite-plus/test";
import { CURRENCY } from "@/types/evcc";
import type { EnergyDevice, EnergyForecastSlot } from "./energyIntelligence";

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

const slots: EnergyForecastSlot[] = [
  {
    start: "2026-10-05T10:00:00Z",
    end: "2026-10-05T10:15:00Z",
    gridPrice: 0.2,
    homeWh: 250,
    homeLowWh: 100,
    homeHighWh: 400,
    solarWh: 0,
    solarLowWh: 0,
    solarHighWh: 0,
  },
  {
    start: "2026-10-05T10:15:00Z",
    end: "2026-10-05T10:30:00Z",
    gridPrice: 0.4,
    homeWh: 250,
    homeLowWh: 100,
    homeHighWh: 400,
    solarWh: 500,
    solarLowWh: 250,
    solarHighWh: 750,
  },
];
const battery: EnergyDevice = {
  key: "battery:db:2",
  name: "db:2",
  title: "Home battery",
  kind: "battery",
  capacityKWh: 10,
  initialSoc: 30,
  plan: [
    { start: slots[0]!.start, chargeWh: 500, dischargeWh: 0, soc: 35 },
    { start: slots[1]!.start, chargeWh: 0, dischargeWh: 0, soc: 35 },
  ],
};
const car: EnergyDevice = {
  key: "loadpoint:0",
  name: "car-a",
  title: "Carport (Family car)",
  kind: "vehicle",
  capacityKWh: 60,
  initialSoc: 40,
  plan: [
    { start: slots[0]!.start, chargeWh: 0, dischargeWh: 0, soc: 40 },
    { start: slots[1]!.start, chargeWh: 2750, dischargeWh: 0, soc: 45 },
  ],
};
const global = {
  mocks: {
    $t: (key: string, args?: Record<string, unknown>) =>
      `${key}${args ? JSON.stringify(args) : ""}`,
    $i18n: { locale: "en" },
  },
};

// The plot spans x = 40 … 440 (layout left 40, right 48 inside a 488 px wide chart).
const WRAP = { left: 0, top: 100, width: 488, height: 400, right: 488, bottom: 500 } as DOMRect;
let wrapper: VueWrapper;

function mount(props: Record<string, unknown> = {}) {
  wrapper = shallowMount(EnergyPlanChart, {
    attachTo: document.body,
    props: { slots, devices: [battery, car], ...props },
    global,
  });
  vi.spyOn(wrapper.get(".chart-wrap").element, "getBoundingClientRect").mockReturnValue(WRAP);
  return wrapper;
}
const tooltip = () => wrapper.find('[data-testid="plan-tooltip"]');
const seriesIds = () =>
  (setOption.mock.calls.at(-1)![0].series as { id: string }[]).map((series) => series.id);

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

describe("energy plan chart", () => {
  test("legend buttons toggle layers and per-device series; power layers start hidden", async () => {
    mount();
    await wrapper.vm.$nextTick();
    expect(seriesIds()).toContain("price");
    expect(seriesIds()).not.toContain("power-battery:db:2");
    const price = wrapper.get('[data-testid="layer-price"]');
    expect(price.attributes("aria-pressed")).toBe("true");
    await price.trigger("click");
    await wrapper.vm.$nextTick();
    expect(price.attributes("aria-pressed")).toBe("false");
    expect(seriesIds()).not.toContain("price");
    await wrapper.get('[data-testid="soc-loadpoint:0"]').trigger("click");
    await wrapper.get('[data-testid="power-battery:db:2"]').trigger("click");
    await wrapper.vm.$nextTick();
    expect(seriesIds()).not.toContain("soc-loadpoint:0");
    expect(seriesIds()).toContain("soc-battery:db:2");
    expect(seriesIds()).toContain("power-battery:db:2");
    expect(wrapper.get('[data-testid="power-battery:db:2"]').attributes("aria-label")).toContain(
      "Home battery"
    );
  });

  test("a lifted finger keeps the details; outside taps, page scroll and cancel dismiss", async () => {
    mount();
    const wrap = wrapper.get(".chart-wrap");
    const tap = async () => {
      await wrap.trigger("pointerdown", { pointerType: "touch", clientX: 340, clientY: 300 });
      await wrap.trigger("pointerup", { pointerType: "touch", clientX: 340, clientY: 300 });
      await wrap.trigger("pointerleave", { pointerType: "touch" });
      expect(tooltip().exists()).toBe(true);
    };
    await tap();
    expect(tooltip().text()).toContain("Carport (Family car)");
    expect(tooltip().text()).not.toContain("car-a");
    expect(tooltip().classes()).toContain("interactive");
    tooltip().element.dispatchEvent(new Event("scroll"));
    tooltip().element.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    await wrapper.vm.$nextTick();
    expect(tooltip().exists()).toBe(true);
    document.body.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    await wrapper.vm.$nextTick();
    expect(tooltip().exists()).toBe(false);
    await tap();
    document.dispatchEvent(new Event("scroll"));
    await wrapper.vm.$nextTick();
    expect(tooltip().exists()).toBe(false);
    await tap();
    await wrap.trigger("pointercancel", { pointerType: "touch" });
    expect(tooltip().exists()).toBe(false);
  });

  test("a finger drag follows the pointer; a hovering finger without contact does nothing", async () => {
    mount();
    const wrap = wrapper.get(".chart-wrap");
    await wrap.trigger("pointermove", { pointerType: "touch", clientX: 340, clientY: 300 });
    expect(tooltip().exists()).toBe(false);
    await wrap.trigger("pointerdown", { pointerType: "touch", clientX: 60, clientY: 300 });
    expect(tooltip().text()).toContain("charging");
    await wrap.trigger("pointermove", { pointerType: "touch", clientX: 340, clientY: 300 });
    await wrapper.vm.$nextTick();
    expect(tooltip().text()).toContain("forecast.energy.plan.state.charging");
    expect(tooltip().text()).toContain("11.0\u00a0kW");
  });

  test("mouse hover shows the slot under the pointer and leaving hides it", async () => {
    mount({ currency: CURRENCY.USD });
    const wrap = wrapper.get(".chart-wrap");
    await wrap.trigger("pointermove", { pointerType: "mouse", clientX: 100, clientY: 300 });
    await wrapper.vm.$nextTick();
    expect(tooltip().text()).toContain("20.0 ¢/kWh");
    await wrap.trigger("pointermove", { pointerType: "mouse", clientX: 439, clientY: 300 });
    await wrapper.vm.$nextTick();
    expect(tooltip().text()).toContain("40.0 ¢/kWh");
    expect(tooltip().classes()).not.toContain("interactive");
    await wrap.trigger("pointerleave", { pointerType: "mouse" });
    expect(tooltip().exists()).toBe(false);
  });

  test("first placement uses the rendered popup size instead of an assumed one", async () => {
    mount();
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(700);
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(1000);
    const measure = HTMLElement.prototype.getBoundingClientRect;
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(
      function (this: HTMLElement) {
        return this.dataset["testid"] === "plan-tooltip"
          ? ({ width: 250, height: 420 } as DOMRect)
          : measure.call(this);
      }
    );
    await wrapper
      .get(".chart-wrap")
      .trigger("pointermove", { pointerType: "mouse", clientX: 100, clientY: 650 });
    await wrapper.vm.$nextTick();
    const element = tooltip().element as HTMLElement;
    expect(element.style.visibility).toBe("");
    expect(element.style.top).toBe(`${700 - 420 - 8}px`);
    expect(element.style.left).toBe("116px");
  });

  test("keyboard moves the crosshair across slots, clamps at the ends and Escape closes", async () => {
    mount();
    const wrap = wrapper.get(".chart-wrap");
    await wrap.trigger("keydown", { key: "ArrowRight" });
    expect(tooltip().text()).toContain("20.0");
    await wrap.trigger("keydown", { key: "ArrowRight" });
    await wrap.trigger("keydown", { key: "ArrowRight" });
    expect(tooltip().text()).toContain("40.0");
    expect(wrapper.get('[aria-live="polite"]').text()).toContain("Carport (Family car): 45\u00a0%");
    await wrap.trigger("keydown", { key: "Home" });
    expect(tooltip().text()).toContain("20.0");
    await wrap.trigger("keydown", { key: "Tab" });
    expect(tooltip().text()).toContain("20.0");
    await wrap.trigger("keydown", { key: "Escape" });
    expect(tooltip().exists()).toBe(false);
  });

  test("per-device power toggles sit behind a closed More layers disclosure", async () => {
    mount();
    const more = wrapper.get('[data-testid="more-layers"]');
    const group = wrapper.get(`#${more.attributes("aria-controls")}`);
    expect(more.attributes("aria-expanded")).toBe("false");
    expect(group.isVisible()).toBe(false);
    await more.trigger("click");
    expect(more.attributes("aria-expanded")).toBe("true");
    expect(group.isVisible()).toBe(true);
    expect(group.findAll("button")).toHaveLength(2);
  });

  test("popup device names carry the full title for truncated display", async () => {
    mount();
    await wrapper
      .get(".chart-wrap")
      .trigger("pointerdown", { pointerType: "touch", clientX: 340, clientY: 300 });
    const names = tooltip().findAll(".device-name");
    expect(names.map((name) => name.attributes("title"))).toEqual([
      "Home battery",
      "Carport (Family car)",
    ]);
  });

  test("energy-only devices get a power toggle but no charge-level toggle", () => {
    mount({
      devices: [
        { ...car, key: "loadpoint:1", title: "Heater", capacityKWh: 0, initialSoc: undefined },
      ],
    });
    expect(wrapper.find('[data-testid="soc-loadpoint:1"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="power-loadpoint:1"]').exists()).toBe(true);
  });
});
