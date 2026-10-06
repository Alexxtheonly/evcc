import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, expect, test, vi } from "vite-plus/test";
import api from "@/api";
import HeatingDegreeDetails from "./HeatingDegreeDetails.vue";
import { autumn, winter } from "./__fixtures__/heatingDegree";

vi.mock("@/api", () => ({ default: { get: vi.fn() } }));
afterEach(() => vi.resetAllMocks());

const global = {
  mocks: {
    $t: (key: string, args?: Record<string, unknown>) =>
      `${key}${args ? JSON.stringify(args) : ""}`,
    $i18n: { locale: "en" },
  },
};

const mountWith = async (data: unknown) => {
  vi.mocked(api.get).mockResolvedValue({ data });
  const wrapper = mount(HeatingDegreeDetails, { global });
  await flushPromises();
  return wrapper;
};

test("autumn: shut gate with its reason, no correction claimed", async () => {
  const wrapper = await mountWith(autumn());
  expect(api.get).toHaveBeenCalledExactlyOnceWith("energyplan/heatingdegree");
  expect(wrapper.get('[data-testid="heating-degree-state"]').text()).toBe(
    "forecast.energy.heating.notCorrecting"
  );
  expect(wrapper.get('[data-testid="heating-degree-status"]').text()).toContain(
    'forecast.energy.heating.modeLine{"mode":"forecast.energy.heating.modes.auto"}'
  );
  expect(wrapper.get('[data-testid="heating-degree-gate"]').text()).toBe(
    'forecast.energy.heating.gateShut{"reason":"forecast.energy.heating.reasons.daily_r_low"}'
  );
  expect(wrapper.get('[data-testid="heating-degree-check-dailyR"]').text()).toBe(
    "forecast.energy.heating.notMet"
  );
  expect(wrapper.get('[data-testid="heating-degree-check-days"]').text()).toBe(
    "forecast.energy.heating.met"
  );
  expect(wrapper.get('[data-testid="heating-degree-fit"]').text()).toContain("-0.18");
  expect(wrapper.get('[data-testid="heating-degree-on-2026-10-07"]').text()).toBe("–");
  const shadow = wrapper.get('[data-testid="heating-degree-shadow"]').text();
  expect(shadow).toContain('forecast.energy.heating.collecting{"n":0,"required":14}');
  expect(wrapper.text()).not.toContain("forecast.energy.heating.capped");
});

test("winter: correcting with On, cap marked, shadow score with interval", async () => {
  const wrapper = await mountWith(winter());
  expect(wrapper.get('[data-testid="heating-degree-state"]').text()).toBe(
    "forecast.energy.heating.correcting"
  );
  expect(wrapper.get('[data-testid="heating-degree-gate"]').text()).toBe(
    "forecast.energy.heating.gateOpen"
  );
  const days = wrapper.get('[data-testid="heating-degree-days"]').text();
  expect(days).toContain("forecast.energy.heating.capped");
  expect(wrapper.get('[data-testid="heating-degree-on-2026-02-20"]').text()).toBe("+8.4 kWh");
  expect(wrapper.get('[data-testid="heating-degree-on-2026-02-21"]').text()).toBe("−2.5 kWh");
  const shadow = wrapper.get('[data-testid="heating-degree-shadow"]').text();
  expect(shadow).toContain("forecast.energy.heating.shadowScore.better");
  expect(shadow).toContain('"delta":"−0.75 kWh","low":"-1.43","high":"-0.26","n":21');
  expect(shadow).toContain('forecast.energy.heating.collecting{"n":9,"required":14}');
});

test("load error", async () => {
  vi.mocked(api.get).mockRejectedValue(new Error("offline"));
  const wrapper = mount(HeatingDegreeDetails, { global });
  await flushPromises();
  expect(wrapper.get('[role="alert"]').text()).toBe("forecast.energy.heating.loadError");
});
