import { shallowMount, config, flushPromises } from "@vue/test-utils";
import { nextTick } from "vue";
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
// eslint-disable-next-line import/first
import liveSample from "./__fixtures__/ledgerLiveSample";

// minimal $t/$te that walk en.json so tests assert on real English text, matching the
// pattern in Vehicles/Status.test.ts and Optimize/OptimizeHeader.test.ts. Also expands
// {placeholder} interpolation - several of this card's strings use it (coverageShort,
// chainUnavailable, the coverage-divergence note).
const lookup = (key: string): string | undefined => {
  const v = key.split(".").reduce<any>((o, k) => o?.[k], en);
  return typeof v === "string" ? v : undefined;
};
config.global.mocks["$t"] = (key: string, params?: Record<string, unknown>) => {
  const v = lookup(key);
  if (typeof v !== "string") return key;
  if (!params) return v;
  return Object.entries(params).reduce((s, [k, val]) => s.replaceAll(`{${k}}`, String(val)), v);
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
  // of Card/SelectGroup/DateNavigatorButton/SavingsLedgerWaterfall/SavingsLedgerInfoModal,
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
    // no chain in this stub: the strip falls back to the one measured figure
    // (realised, at the default periodAverage headline) and shows no baseline or
    // saved column it cannot honestly compute
    expect(wrapper.find('[data-testid="savings-ledger-detail-paid"]').text()).toContain("10");
    expect(wrapper.find('[data-testid="savings-ledger-detail-baseline"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="savings-ledger-detail-saved"]').exists()).toBe(false);
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

describe("SavingsLedgerCard presentation", () => {
  // the real API response the diagram was built against - a genuine Control overspend
  const PRESENT_WINDOW = {
    from: "2026-08-22T00:00:00.000Z",
    to: "2026-08-24T00:00:00.000Z", // == NOW
  };

  const detail = (wrapper: any, key: string) =>
    wrapper.find(`[data-testid="savings-ledger-detail-${key}"]`);

  test("renders the three-figure strip from the chain, at the default headline", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    expect(api.get).toHaveBeenCalledTimes(1);
    // periodAverage lens: W0 6.6656, W3 0.6019, saved 6.0637 (91 % of W0)
    expect(detail(wrapper, "baseline").text()).toBe("€6.67");
    expect(detail(wrapper, "paid").text()).toBe("€0.60");
    expect(detail(wrapper, "saved").text()).toBe("€6.06 (91%)");
    expect(detail(wrapper, "saved").classes()).toContain("text-primary");
  });

  test("keeps the estimate marker and coverage visible without interaction", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    // ADR-011 rules 3 and 7, in one line under the diagram
    const caption = wrapper.find('[data-testid="savings-ledger-caption"]').text();
    expect(caption).toContain("battery and control are estimated");
    // the valid/total parenthetical stays: LedgerCoverage.fraction is 0 for two
    // different reasons (no slots at all vs. no valid slots), and only the counts
    // disambiguate them
    expect(caption).toContain("43.8% of slots (84 of 192)");
  });

  test("names a loss a loss and never clamps it to zero", async () => {
    // same payload, but the period ends up costing more than the W0 baseline
    const loss = JSON.parse(JSON.stringify(liveSample));
    loss.chain.worlds[3].settled.periodAverage = 8.0; // W0 is 6.6656
    loss.chain.contributions[2].settled.periodAverage = -5.0;

    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: loss });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    const saved = detail(wrapper, "saved");
    expect(wrapper.text()).toContain("cost you");
    expect(saved.text()).toBe("€1.33 (20%)");
    expect(saved.classes()).toContain("text-danger");
  });

  test("hands the decisions rows upward instead of letting a second card re-fetch them", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    expect(api.get).toHaveBeenCalledTimes(1);
    const emitted = wrapper.emitted("update:decisions");
    // null the moment the request starts, then the rows once it lands
    expect(emitted).toHaveLength(2);
    expect(emitted![0]![0]).toBeNull();
    expect(emitted![1]![0]).toHaveLength(liveSample.decisions.length);
  });

  test("takes the decisions card down while the next period is still loading", async () => {
    vi.mocked(api.get)
      .mockResolvedValueOnce({ status: 200, data: liveSample })
      // a distinct object, not the same reference: `ledger` is watched by identity, and
      // handing back the very same payload would make the second response a no-op
      .mockResolvedValueOnce({ status: 200, data: JSON.parse(JSON.stringify(liveSample)) });

    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();
    expect(wrapper.emitted("update:decisions")).toHaveLength(2);

    // paging starts a new request: the card below must not go on showing the previous
    // period's ticks and euro deltas under the new period's label
    (wrapper.vm as any).page(-1);
    await wrapper.vm.$nextTick();
    const midFlight = wrapper.emitted("update:decisions")!;
    expect(midFlight).toHaveLength(3);
    expect(midFlight[2]![0]).toBeNull();
    expect(wrapper.find('[data-testid="savings-ledger-loading"]').exists()).toBe(true);

    await flushPromises();
    const landed = wrapper.emitted("update:decisions")!;
    expect(landed).toHaveLength(4);
    expect(landed[3]![0]).toHaveLength(liveSample.decisions.length);
  });

  test("takes the decisions card down again when the period is refused", async () => {
    vi.mocked(api.get)
      .mockResolvedValueOnce({ status: 200, data: liveSample })
      .mockResolvedValueOnce({ status: 400, data: { error: "range unaligned" } });

    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();
    (wrapper.vm as any).page(-1);
    await flushPromises();

    const emitted = wrapper.emitted("update:decisions");
    // loading-null, rows, loading-null, refusal-null
    expect(emitted).toHaveLength(4);
    expect(emitted![3]![0]).toBeNull();
    expect(wrapper.find('[data-testid="savings-ledger-refuse"]').exists()).toBe(true);
  });

  test("names a Control overspend under the chart even when the period headlines a saving", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    // the strip legitimately reads "saved EUR 6.06 (91 %)" on this payload while the
    // Control step itself lost EUR 0.41 against its own baseline
    expect(detail(wrapper, "saved").text()).toBe("€6.06 (91%)");
    const clause = wrapper.find('[data-testid="savings-ledger-control-overspend"]');
    expect(clause.exists()).toBe(true);
    expect(clause.text()).toContain("control cost €0.41 against its baseline");
    expect(clause.classes()).toContain("text-danger");
  });

  test("says nothing about Control when Control saved money", async () => {
    const saved = JSON.parse(JSON.stringify(liveSample));
    // W2 -> W3 now favours the real controller at both lenses
    saved.chain.contributions[2].settled = { perSlot: 0.5, periodAverage: 0.5 };

    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: saved });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    expect(wrapper.find('[data-testid="savings-ledger-control-overspend"]').exists()).toBe(false);
  });

  test("names the EV-charge-timing non-attribution under the chart, not only in the modal", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    const line = wrapper.find('[data-testid="savings-ledger-ev-timing"]');
    expect(line.exists()).toBe(true);
    expect(line.text()).toContain("cheaper hour");
    // the API's own full sentence is still handed to the modal, never dropped
    expect((wrapper.vm as any).notes).toContain(
      liveSample.chain!.notes!.find((n) => n.startsWith("EV charge timing"))
    );
  });

  test("drops the EV-timing line for a site whose payload never mentions it", async () => {
    const noEv = JSON.parse(JSON.stringify(liveSample));
    noEv.chain.notes = noEv.chain.notes.filter(
      (n: string) => !n.startsWith("EV charge timing is not attributed")
    );

    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: noEv });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    expect(wrapper.find('[data-testid="savings-ledger-ev-timing"]').exists()).toBe(false);
  });

  test("offers the caveats behind an info control rather than dropping them", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    expect(wrapper.find('[data-testid="savings-ledger-info-icon"]').exists()).toBe(true);
    // realised.note and chain.notes[0] are the same sentence - deduped, not dropped
    const notes = (wrapper.vm as any).notes as string[];
    expect(notes).toHaveLength(liveSample.chain!.notes!.length);
    expect(new Set(notes).size).toBe(notes.length);
    expect(notes).toContain(liveSample.realised.note);
  });
});

describe("SavingsLedgerCard overlapping requests", () => {
  // Paging twice quickly leaves two requests in flight. The second one's `loading = true`
  // is a no-op (already true), so nothing re-hides the content, and whichever response
  // lands LAST used to win - which for ordinary HTTP can be the first period's. The card
  // then showed period A's euros under period B's label, permanently.
  test("a superseded response never overwrites the newer period's figures", async () => {
    const stub = (paid: number) => ({
      ...ledgerStub,
      realised: { ...ledgerStub.realised, settled: { perSlot: paid, periodAverage: paid } },
    });

    let resolveFirst: (v: unknown) => void = () => {};
    let resolveSecond: (v: unknown) => void = () => {};

    vi.mocked(api.get)
      .mockResolvedValueOnce({ status: 200, data: stub(10) }) // the mount fetch
      .mockReturnValueOnce(new Promise((r) => (resolveFirst = r)) as any)
      .mockReturnValueOnce(new Promise((r) => (resolveSecond = r)) as any);

    const wrapper = mountCard({
      from: "2026-08-22T00:00:00.000Z",
      to: "2026-08-24T00:00:00.000Z", // == NOW
    });
    await flushPromises();

    const vm = wrapper.vm as any;
    vm.page(-1);
    await nextTick();
    vm.page(-1);
    await nextTick();
    expect(api.get).toHaveBeenCalledTimes(3);

    // out-of-order landing: the NEWER request answers first, the older one straggles in
    resolveSecond({ status: 200, data: stub(22) });
    await flushPromises();
    resolveFirst({ status: 200, data: stub(11) });
    await flushPromises();

    expect(wrapper.find('[data-testid="savings-ledger-detail-paid"]').text()).toContain("22");
    expect(wrapper.find('[data-testid="savings-ledger-detail-paid"]').text()).not.toContain("11");
    expect(vm.loading).toBe(false);
  });
});
