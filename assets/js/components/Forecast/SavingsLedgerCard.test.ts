import { shallowMount, config, flushPromises } from "@vue/test-utils";
import { describe, expect, test, vi, beforeEach, afterEach } from "vite-plus/test";
import en from "../../../../i18n/en.json";

// api is mocked wholesale (not just api.get) so importing SavingsLedgerCard.vue never
// pulls in the real axios instance - that instance reads window.location at module init
// (assets/js/api.ts) and its error interceptor pulls in the auth modal/restart modules,
// neither of which this test needs or wants to set up.
vi.mock("@/api", () => ({ default: { get: vi.fn() } }));

// eslint-disable-next-line import/first
import api from "@/api";
// eslint-disable-next-line import/first
import SavingsLedgerCard from "./SavingsLedgerCard.vue";

// minimal $t/$te that walk en.json so tests assert on real English text, matching the
// pattern in Vehicles/Status.test.ts and Optimize/OptimizeHeader.test.ts. Also expands
// {placeholder} interpolation - several of this card's strings use it (heroLabel,
// coverage, refuseTitle's neighbours).
const lookup = (key: string): string | undefined => {
  const v = key.split(".").reduce<any>((o, k) => o?.[k], en);
  return typeof v === "string" ? v : undefined;
};
config.global.mocks["$t"] = (key: string, params?: Record<string, unknown>) => {
  const v = lookup(key);
  if (typeof v !== "string") return key;
  if (!params) return v;
  return Object.entries(params).reduce(
    (s, [k, val]) => s.replaceAll(`{${k}}`, String(val)),
    v
  );
};
config.global.mocks["$te"] = (key: string) => lookup(key) !== undefined;
config.global.mocks["$i18n"] = { locale: "en-US" };
// Card.vue's title/refusal/content markup all live inside <Card>'s default slot - a
// shallow stub doesn't render slot content unless told to, which silently hid this
// test's own markup behind an empty <card-stub/> the first time this ran.
config.global.renderStubDefaultSlot = true;

// fixed, already slot-aligned "now" - every isAtPresent check in the component reads
// `new Date()` directly, so the window seeded via $route.query below must be built
// against this same instant for isAtPresent to come out the way each test expects.
const NOW = new Date("2026-08-24T00:00:00.000Z");

const ledgerStub = {
  from: "2026-08-21T10:30:00.000Z",
  to: "2026-08-24T00:00:00.000Z",
  realised: {
    settled: { perSlot: 12.34, periodAverage: 10.0 },
    coverage: { validSlots: 100, totalSlots: 100, fraction: 1 },
    note: "realised note",
  },
  decisions: [],
};

function mountCard(query: Record<string, string>) {
  // shallow: this test is about which top-level branch (loading/refusal/content) the
  // card's own template picks and how many requests it issues - not about the internals
  // of Card/SelectGroup/DateNavigatorButton/SavingsLedgerChain/SavingsLedgerDecisions,
  // which is what mount() would additionally exercise.
  return shallowMount(SavingsLedgerCard, {
    global: { mocks: { $route: { query } } },
  });
}

beforeEach(() => {
  vi.setSystemTime(NOW);
});

afterEach(() => {
  vi.useRealTimers();
  vi.mocked(api.get).mockReset();
});

describe("SavingsLedgerCard auto-clamp on the default/present window", () => {
  test("a 422 with earliest at the present window clamps and issues a second request that succeeds", async () => {
    vi.mocked(api.get)
      .mockResolvedValueOnce({
        status: 422,
        data: {
          error: "no tariff data before 2026-08-21T12:30:00+02:00",
          earliest: "2026-08-21T12:30:00+02:00",
        },
      })
      .mockResolvedValueOnce({ status: 200, data: ledgerStub });

    const wrapper = mountCard({
      from: "2026-08-17T00:00:00.000Z",
      to: "2026-08-24T00:00:00.000Z", // == NOW, so isAtPresent is true
    });

    await flushPromises(); // first (422) request
    await flushPromises(); // clamped win reassignment -> second (200) request

    expect(api.get).toHaveBeenCalledTimes(2);
    const secondCallParams = vi.mocked(api.get).mock.calls[1]![1] as any;
    expect(secondCallParams.params.from).toBe("2026-08-21T10:30:00.000Z"); // ceil-aligned earliest
    expect(secondCallParams.params.to).toBe("2026-08-24T00:00:00.000Z"); // unchanged

    expect(wrapper.find('[data-testid="savings-ledger-content"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="savings-ledger-refuse"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="savings-ledger-hero-value"]').text()).toContain("10");
  });

  test("a 422 with earliest while paged into the past renders the plain refusal and does not clamp", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({
      status: 422,
      data: {
        error: "no tariff data before 2026-08-21T12:30:00+02:00",
        earliest: "2026-08-21T12:30:00+02:00",
      },
    });

    const wrapper = mountCard({
      from: "2026-08-01T00:00:00.000Z",
      to: "2026-08-08T00:00:00.000Z", // well before NOW -> isAtPresent is false
    });

    await flushPromises();

    expect(api.get).toHaveBeenCalledTimes(1); // no auto-retry
    expect(wrapper.find('[data-testid="savings-ledger-refuse"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="savings-ledger-refuse-body"]').text()).toBe(
      "no tariff data before 2026-08-21T12:30:00+02:00"
    );
    expect(wrapper.find('[data-testid="savings-ledger-content"]').exists()).toBe(false);
  });
});
