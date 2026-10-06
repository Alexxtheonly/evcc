import { flushPromises, shallowMount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, test, vi } from "vite-plus/test";
import api from "@/api";
import settings from "@/settings";
import EnergyPlanCard from "./EnergyPlanCard.vue";
import EnergyPlanChart from "./EnergyPlanChart.vue";
import type { EnergyInsights, PlanHistory } from "./energyIntelligence";

vi.mock("@/api", () => ({ default: { get: vi.fn() } }));

const insights: EnergyInsights = {
  updated: "2026-10-05T10:00:00Z",
  automatic: true,
  status: "ready",
  settings: {
    robust: false,
    arrivals: false,
    useLearnedEfficiency: false,
    batteryWear: {},
    settlementMode: "simulation",
  },
  forecast: [
    {
      start: "2026-10-05T10:00:00Z",
      end: "2026-10-05T10:15:00Z",
      homeWh: 250,
      homeLowWh: 100,
      homeHighWh: 600,
      solarWh: 0,
      solarLowWh: 0,
      solarHighWh: 0,
      gridPrice: 0.2,
    },
  ],
  devices: [],
};
const response = (hours: number): PlanHistory => ({
  from: `${hours}`,
  to: "",
  forecast: [],
  devices: [],
});
const global = {
  renderStubDefaultSlot: true,
  stubs: { RouterLink: { template: "<a><slot /></a>" } },
  mocks: {
    $t: (key: string) => key,
    $i18n: { locale: "en" },
  },
};

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-10-05T10:05:00Z"));
  vi.mocked(api.get).mockReset();
  settings.planHistoryHours = 24;
});
afterEach(() => {
  vi.useRealTimers();
});

describe("energy plan card history", () => {
  test("fetches only while the plan tab is active and a window is chosen", async () => {
    vi.mocked(api.get).mockResolvedValue({ status: 200, data: response(24) });
    const wrapper = shallowMount(EnergyPlanCard, { props: { insights }, global });
    await flushPromises();
    expect(api.get).not.toHaveBeenCalled();
    await wrapper.setProps({ active: true });
    await flushPromises();
    expect(api.get).toHaveBeenCalledOnce();
    expect(vi.mocked(api.get).mock.calls[0]![0]).toBe("energyplan/history");
    expect(vi.mocked(api.get).mock.calls[0]![1]).toMatchObject({ params: { hours: 24 } });
    const chart = wrapper.getComponent(EnergyPlanChart);
    expect(chart.props("history")).toEqual(response(24));
    expect(chart.props("historyHours")).toBe(24);
    chart.vm.$emit("update:historyHours", 0);
    await flushPromises();
    expect(settings.planHistoryHours).toBe(0);
    expect(api.get).toHaveBeenCalledOnce();
    wrapper.unmount();
  });

  test("refreshes once per slot, not on every plan update", async () => {
    vi.mocked(api.get).mockResolvedValue({ status: 200, data: response(24) });
    const wrapper = shallowMount(EnergyPlanCard, { props: { insights, active: true }, global });
    await flushPromises();
    expect(api.get).toHaveBeenCalledTimes(1);
    await wrapper.setProps({ insights: { ...insights, updated: "2026-10-05T10:05:30Z" } });
    await flushPromises();
    expect(api.get).toHaveBeenCalledTimes(1);
    // next boundary 10:15 plus 45 s for the slot to be written
    vi.advanceTimersByTime(10 * 60_000 + 44_000);
    expect(api.get).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(1_000);
    expect(api.get).toHaveBeenCalledTimes(2);
    vi.advanceTimersByTime(15 * 60_000);
    expect(api.get).toHaveBeenCalledTimes(3);
    wrapper.unmount();
    vi.advanceTimersByTime(60 * 60_000);
    expect(api.get).toHaveBeenCalledTimes(3);
  });

  test("a stale response never replaces a newer window; failures are quiet", async () => {
    const pending: ((value: unknown) => void)[] = [];
    vi.mocked(api.get).mockImplementation(
      () => new Promise((resolve) => pending.push(resolve)) as ReturnType<typeof api.get>
    );
    const wrapper = shallowMount(EnergyPlanCard, { props: { insights, active: true }, global });
    settings.planHistoryHours = 48;
    await flushPromises();
    expect(pending).toHaveLength(2);
    pending[1]!({ status: 200, data: response(48) });
    pending[0]!({ status: 200, data: response(24) });
    await flushPromises();
    const chart = wrapper.getComponent(EnergyPlanChart);
    expect(chart.props("history")).toEqual(response(48));

    vi.mocked(api.get).mockResolvedValueOnce({ status: 400, data: { error: "database offline" } });
    settings.planHistoryHours = 6;
    await flushPromises();
    expect(chart.props("history")).toBeUndefined();
    expect(chart.props("historyError")).toBe(true);
    expect(vi.mocked(api.get).mock.calls.at(-1)![1]).toMatchObject({ params: { hours: 6 } });
    wrapper.unmount();
  });
});
