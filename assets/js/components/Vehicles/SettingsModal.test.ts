import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, expect, test, vi } from "vite-plus/test";
import SettingsModal from "./SettingsModal.vue";
import GenericModal from "../Helper/GenericModal.vue";
import api from "@/api";
import type { Vehicle } from "@/types/evcc";

vi.mock("@/api", () => ({ default: { post: vi.fn(), delete: vi.fn() } }));
afterEach(() => vi.resetAllMocks());

const calypso = (overrides: Partial<Vehicle> = {}): Vehicle => ({
  name: "calypso",
  title: "Calypso",
  minSoc: 30,
  repeatingPlans: null,
  planStrategy: { continuous: false, precondition: 0 },
  features: [],
  capacity: 55,
  ...overrides,
});

const mountModal = async (vehicle: Vehicle) => {
  const wrapper = mount(SettingsModal, {
    props: { vehicles: [vehicle] },
    global: {
      stubs: {
        GenericModal: { template: "<div><slot /></div>" },
        VehicleIcon: true,
        RouterLink: { template: "<a><slot /></a>" },
      },
      mocks: {
        $t: (key: string, args?: Record<string, unknown>) =>
          `${key}${args ? JSON.stringify(args) : ""}`,
      },
    },
  });
  wrapper.getComponent(GenericModal).vm.$emit("open");
  await flushPromises();
  return wrapper;
};

test("cheap band offers off and levels above the minimum charge, and posts the choice", async () => {
  const wrapper = await mountModal(calypso());
  const select = wrapper.get('[data-testid="cheap-band-soc"]');
  const values = select.findAll("option").map((option) => Number(option.element.value));
  expect(values[0]).toBe(0);
  expect(Math.min(...values.slice(1))).toBe(35);
  expect(wrapper.find('[data-testid="cheap-band-min-run"]').exists()).toBe(false);
  await select.setValue("50");
  expect(api.post).toHaveBeenCalledWith("vehicles/calypso/cheapbandsoc/50");
});

test("minimum charging block appears with an active band and keeps an unusual stored value", async () => {
  const wrapper = await mountModal(calypso({ cheapBandSoc: 50, cheapBandMinRun: 20 }));
  const select = wrapper.get('[data-testid="cheap-band-min-run"]');
  expect(select.element).toHaveProperty("value", "20");
  await select.setValue("60");
  expect(api.post).toHaveBeenCalledWith("vehicles/calypso/cheapbandminrun/60");
});
