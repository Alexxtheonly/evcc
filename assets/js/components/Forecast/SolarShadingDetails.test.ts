import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, expect, test, vi } from "vite-plus/test";
import api from "@/api";
import { status } from "./__fixtures__/solarShading";

const setOption = vi.fn();
vi.mock("@/api", () => ({ default: { get: vi.fn() } }));
vi.mock("./echarts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./echarts")>()),
  registerTouchTooltip: vi.fn(),
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

const { default: SolarShadingDetails } = await import("./SolarShadingDetails.vue");

afterEach(() => {
  vi.resetAllMocks();
});

const global = {
  mocks: {
    $t: (key: string, args?: Record<string, unknown>) =>
      `${key}${args ? JSON.stringify(args) : ""}`,
    $i18n: { locale: "en" },
  },
};

const mountWith = async (data: unknown) => {
  vi.mocked(api.get).mockResolvedValue({ data });
  const wrapper = mount(SolarShadingDetails, { global });
  await flushPromises();
  return wrapper;
};

type HeatmapOption = {
  series: {
    type: string;
    data: { value: number[]; itemStyle: { color: string; opacity: number } }[];
  }[];
  tooltip: { formatter: (p: { dataIndex: number }) => string };
};
const lastOption = () => setOption.mock.calls.at(-1)?.[0] as HeatmapOption;

test("learned but not enabled: map, status and no active claim", async () => {
  const wrapper = await mountWith(status());
  expect(api.get).toHaveBeenCalledExactlyOnceWith("energyplan/solarshading");
  expect(wrapper.get('[data-testid="solar-shading-state"]').text()).toBe(
    "forecast.energy.shading.notActive"
  );
  const line = wrapper.get('[data-testid="solar-shading-status"]').text();
  expect(line).toContain('forecast.energy.shading.statusInactive{"days":43,"slots":2091}');
  expect(line).toContain("forecast.energy.shading.updated");
  expect(wrapper.text()).toContain('forecast.energy.shading.fallbackDaily{"factor":"0.87"}');
  expect(wrapper.find('[data-testid="solar-shading-chart"]').exists()).toBe(true);

  const option = lastOption();
  expect(option.series[0]!.type).toBe("heatmap");
  expect(option.series[0]!.data.map((d) => d.value)).toEqual([
    [10, 7, 0.31],
    [5, 5, 1.09],
    [1, 0, 0.35],
  ]);
  // shaded and better-than-forecast bins get different hues
  expect(option.series[0]!.data[0]!.itemStyle.color).not.toBe(
    option.series[0]!.data[1]!.itemStyle.color
  );
  const tip = option.tooltip.formatter({ dataIndex: 0 });
  expect(tip).toContain("forecast.energy.shading.tooltipPosition");
  expect(tip).toContain("0.31");
  expect(tip).toContain('forecast.energy.shading.tooltipSlots{"slots":22,"weight":"17.0"}');
  expect(tip).toContain("forecast.energy.shading.source.learned");
});

test("active", async () => {
  const wrapper = await mountWith(status({ enabled: true, active: true, fallback: "none" }));
  expect(wrapper.get('[data-testid="solar-shading-state"]').text()).toBe(
    "forecast.energy.shading.active"
  );
  expect(wrapper.text()).toContain("forecast.energy.shading.statusActive");
  expect(wrapper.text()).toContain("forecast.energy.shading.fallbackNone");
});

test("unavailable without coordinates: reason, no chart", async () => {
  const wrapper = await mountWith(
    status({ enabled: true, available: false, reason: "coordinates", bins: [] })
  );
  expect(wrapper.get('[data-testid="solar-shading-state"]').text()).toBe(
    "forecast.energy.shading.notActive"
  );
  expect(wrapper.text()).toContain("forecast.energy.shading.unavailable.coordinates");
  expect(wrapper.find('[data-testid="solar-shading-chart"]').exists()).toBe(false);
  expect(wrapper.text()).not.toContain("forecast.energy.shading.fallback");
  expect(wrapper.text()).not.toContain("forecast.energy.shading.explain");
  expect(setOption).not.toHaveBeenCalled();
});

test("load failure is shown, not an empty map", async () => {
  vi.mocked(api.get).mockRejectedValue(new Error("offline"));
  const wrapper = mount(SolarShadingDetails, { global });
  expect(wrapper.text()).toContain("forecast.energy.shading.loading");
  await flushPromises();
  expect(wrapper.text()).toContain("forecast.energy.shading.loadError");
  expect(wrapper.find('[data-testid="solar-shading-status"]').exists()).toBe(false);
});

test("bins borrowed from neighbours are drawn fainter than learned ones", async () => {
  const s = status();
  s.bins[1] = { ...s.bins[1]!, source: "neighbour" };
  await mountWith(s);
  const data = lastOption().series[0]!.data;
  expect(data.map((d) => d.itemStyle.opacity)).toEqual([1, 0.45, 1]);
});
