import { shallowMount, flushPromises } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import { beforeEach, describe, expect, test, vi } from "vite-plus/test";
import Optimize from "./Optimize.vue";
import Forecast from "./Forecast.vue";
import Config from "./Config.vue";
import MoreMenu from "../components/BottomTabs/MoreMenu.vue";
import EnergyPlanCard from "../components/Forecast/EnergyPlanCard.vue";
import EnergySettings from "../components/Forecast/EnergySettings.vue";
import SavingsLedgerCard from "../components/Forecast/SavingsLedgerCard.vue";
import SavingsLedgerDecisions from "../components/Forecast/SavingsLedgerDecisions.vue";
import SavingsLedgerWaterfall from "../components/Forecast/SavingsLedgerWaterfall.vue";
import api from "@/api";
import store from "@/store";
import liveSample from "../components/Forecast/__fixtures__/ledgerLiveSample";

vi.mock("@/api", () => ({ default: { get: vi.fn(), post: vi.fn() } }));

const mocks = {
  $t: (key: string) => key,
  $te: () => true,
  $i18n: { locale: "en" },
};
const navigation = async (path: string) => {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/optimize", component: Optimize },
      { path: "/forecast", component: Forecast },
      { path: "/config", component: Config },
      { path: "/log", component: { template: "<div />" } },
    ],
  });
  await router.push(path);
  return router;
};
beforeEach(() => {
  store.reset();
  Object.assign(store.state, { optimizer: true, experimental: true });
  vi.mocked(api.get).mockReset();
  vi.mocked(api.post).mockReset();
  vi.mocked(api.get).mockResolvedValue({ status: 200, data: liveSample });
});

describe("energy planning placement", () => {
  test("a ledger response arriving on another tab does not initialize hidden charts", async () => {
    let complete!: (response: { status: number; data: typeof liveSample }) => void;
    vi.mocked(api.get).mockReturnValueOnce(
      new Promise((resolve) => {
        complete = resolve;
      })
    );
    const router = await navigation("/optimize?tab=results");
    const wrapper = shallowMount(Optimize, {
      global: {
        plugins: [router],
        mocks,
        renderStubDefaultSlot: true,
        stubs: { SavingsLedgerCard: false },
      },
    });
    await router.push("/optimize?tab=plan");
    complete({ status: 200, data: liveSample });
    await flushPromises();
    const ledger = wrapper.getComponent(SavingsLedgerCard);
    expect(ledger.findComponent(SavingsLedgerWaterfall).exists()).toBe(false);
    expect(wrapper.getComponent(SavingsLedgerDecisions).props("active")).toBe(false);
    await router.push("/optimize?tab=results");
    expect(ledger.findComponent(SavingsLedgerWaterfall).exists()).toBe(true);
    expect(api.get).toHaveBeenCalledOnce();
    wrapper.unmount();
  });
  test("Plan is the default, Results is lazy, and browser history preserves owner and selection", async () => {
    const router = await navigation("/optimize");
    const wrapper = shallowMount(Optimize, {
      global: {
        plugins: [router],
        mocks,
        renderStubDefaultSlot: true,
        stubs: { RouterLink: false, SavingsLedgerCard: false },
      },
    });
    const plan = wrapper.getComponent(EnergyPlanCard).vm;
    expect(wrapper.findComponent(SavingsLedgerCard).exists()).toBe(false);
    expect(api.get).not.toHaveBeenCalled();
    await router.push("/optimize?tab=results");
    await flushPromises();
    const ledger = wrapper.getComponent(SavingsLedgerCard);
    expect(api.get).toHaveBeenCalledTimes(1);
    expect(wrapper.findComponent(SavingsLedgerDecisions).exists()).toBe(true);
    expect(ledger.get('[data-testid="ledger-settlement-basis"]').text()).toBe(
      "forecast.energy.settlementUnavailable"
    );
    await ledger.setData({ headline: "perSlot" });
    ledger.vm.page(-1);
    await flushPromises();
    expect(api.get).toHaveBeenCalledTimes(2);
    const window = ledger.vm.win;
    await router.push("/optimize?tab=diagnostics");
    expect(wrapper.getComponent(EnergyPlanCard).vm).toBe(plan);
    expect(ledger.props("active")).toBe(false);
    expect(ledger.findComponent(SavingsLedgerWaterfall).exists()).toBe(false);
    router.back();
    await flushPromises();
    expect(router.currentRoute.value.query["tab"]).toBe("results");
    expect(wrapper.getComponent(SavingsLedgerCard).vm).toBe(ledger.vm);
    expect(ledger.vm.headline).toBe("perSlot");
    expect(ledger.vm.win).toEqual(window);
    expect(ledger.findComponent(SavingsLedgerWaterfall).exists()).toBe(true);
    expect(api.get).toHaveBeenCalledTimes(2);
    expect(api.post).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  test("direct Results works without forecasts, optimizer or successful plan", async () => {
    Object.assign(store.state, { optimizer: false });
    const router = await navigation("/optimize?tab=results");
    const wrapper = shallowMount(Optimize, {
      global: {
        plugins: [router],
        mocks,
        renderStubDefaultSlot: true,
        stubs: { SavingsLedgerCard: false },
      },
    });
    await flushPromises();
    expect(wrapper.getComponent(SavingsLedgerCard).props("active")).toBe(true);
    expect(api.get).toHaveBeenCalledTimes(1);
    expect(api.get).toHaveBeenCalledWith("savingsledger", expect.anything());
    expect(api.post).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  test("Forecast has source-independent links and no duplicated plan or results", async () => {
    const router = await navigation("/forecast");
    const wrapper = shallowMount(Forecast, {
      global: {
        plugins: [router],
        mocks,
        renderStubDefaultSlot: true,
        stubs: { RouterLink: false },
      },
    });
    expect(wrapper.get('a[href="/optimize"]').text()).toBe("forecast.energy.viewPlan");
    expect(wrapper.get('a[href="/optimize?tab=results"]').text()).toBe(
      "forecast.energy.viewResults"
    );
    expect(wrapper.findComponent(EnergyPlanCard).exists()).toBe(false);
    expect(wrapper.findComponent(SavingsLedgerCard).exists()).toBe(false);
    expect(api.get).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  test("unknown tab falls back to Plan and enabled pending optimizer remains discoverable", async () => {
    const router = await navigation("/optimize?tab=unknown");
    const global = {
      plugins: [router],
      mocks,
      renderStubDefaultSlot: true,
      stubs: { RouterLink: false, Teleport: true },
    };
    const wrapper = shallowMount(Optimize, { global });
    expect(wrapper.vm.activeTab).toBe("plan");
    const menu = shallowMount(MoreMenu, {
      props: { optimizer: true, experimental: false },
      global,
    });
    expect(menu.get('a[href="/optimize"]').text()).toContain("forecast.energy.pageTitle");
    expect(menu.props("evopt")).toBeUndefined();
    menu.unmount();
    wrapper.unmount();
  });

  test("Config offers the same settings editor without an optimizer result or battery", async () => {
    vi.mocked(api.get).mockResolvedValue({ status: 404 });
    const router = await navigation("/config#integrations");
    const open = vi.fn();
    const wrapper = shallowMount(Config, {
      global: {
        plugins: [router],
        mocks,
        renderStubDefaultSlot: true,
        stubs: {
          DeviceCard: { template: '<div><slot name="tags" /></div>' },
          EnergySettings: {
            ...EnergySettings,
            template: "<div />",
            methods: { ...EnergySettings.methods, open },
          },
        },
      },
    });
    await flushPromises();
    const button = wrapper
      .findAll("button")
      .find((button) => button.text() === "forecast.energy.configure")!;
    expect(button).toBeDefined();
    expect(wrapper.getComponent(EnergySettings).props("devices")).toEqual([]);
    await button.trigger("click");
    expect(open).toHaveBeenCalledOnce();
    expect(api.post).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  test("decisions retain table and selected interval while their chart is hidden", async () => {
    const wrapper = shallowMount(SavingsLedgerDecisions, {
      props: { decisions: liveSample.decisions || [], active: false },
      global: { mocks, renderStubDefaultSlot: true },
    });
    expect(wrapper.find('[data-testid="savings-ledger-decisions-timeline"]').exists()).toBe(false);
    const selectedTs = liveSample.decisions?.[0]?.ts;
    await wrapper.setData({ view: "table", selectedTs });
    await wrapper.setProps({ active: true });
    expect(wrapper.vm.view).toBe("table");
    expect(wrapper.vm.selectedTs).toBe(selectedTs);
    await wrapper.setProps({ active: false });
    expect(wrapper.vm.view).toBe("table");
    wrapper.unmount();
  });
});
