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
// eslint-disable-next-line import/first
import { EV_TIMING_NOTE } from "./savingsLedgerChain";

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

  // N0: a successful response can still be one the diagram cannot be drawn over. The
  // tariffs table on this site starts 2026-08-17 while the battery was commissioned on
  // 2026-08-21, so a default 7-day window is ACCEPTED, computes the realised figure over
  // 584 slots and the chain over 254 - and every figure the card draws is the chain's.
  test("narrows the present window to where the chain can actually be drawn", async () => {
    vi.mocked(api.get)
      .mockResolvedValueOnce({
        status: 200,
        data: { ...ledgerStub, chainEarliest: "2026-08-21T12:45:00+02:00" },
      })
      .mockResolvedValueOnce({ status: 200, data: ledgerStub });

    mountCard({
      from: "2026-08-17T00:00:00.000Z",
      to: "2026-08-24T00:00:00.000Z", // == NOW, so isAtPresent is true
    });

    await flushPromises();
    await flushPromises();

    expect(api.get).toHaveBeenCalledTimes(2);
    const secondCallParams = vi.mocked(api.get).mock.calls[1]![1] as any;
    expect(secondCallParams.params.from).toBe("2026-08-21T10:45:00.000Z");
    expect(secondCallParams.params.to).toBe("2026-08-24T00:00:00.000Z");
  });

  // D1: the two clamps are SEQUENTIAL on this site - the default window starts before the
  // tariff history AND before the battery was commissioned. They used to share one
  // hasAutoClamped flag, so the 422 clamp consumed the only attempt and the chain clamp
  // never ran: the user was left on the 635-slot window with the strip reading "you paid
  // EUR 4.60" against a real bill of EUR 34.69. Exactly the live sequence, replayed.
  test("clamps to the tariff start on a 422 and then again to chainEarliest on the 200", async () => {
    vi.mocked(api.get)
      .mockResolvedValueOnce({
        status: 422,
        data: {
          error: "no tariff data before 2026-08-17T20:30:00+02:00",
          earliest: "2026-08-17T20:30:00+02:00",
        },
      })
      .mockResolvedValueOnce({
        status: 200,
        data: { ...ledgerStub, chainEarliest: "2026-08-21T12:45:00+02:00" },
      })
      .mockResolvedValueOnce({ status: 200, data: ledgerStub });

    const wrapper = mountCard({
      from: "2026-08-17T00:00:00.000Z",
      to: "2026-08-24T00:00:00.000Z", // == NOW, so isAtPresent is true
    });

    await flushPromises(); // 1st (422) -> clamp to the tariff start
    await flushPromises(); // 2nd (200, chainEarliest) -> clamp to the chain start
    await flushPromises(); // 3rd (200)

    expect(api.get).toHaveBeenCalledTimes(3);
    const from = (i: number) => (vi.mocked(api.get).mock.calls[i]![1] as any).params.from;
    expect(from(1)).toBe("2026-08-17T18:30:00.000Z"); // tariff start
    expect(from(2)).toBe("2026-08-21T10:45:00.000Z"); // chain start
    expect(wrapper.find('[data-testid="savings-ledger-content"]').exists()).toBe(true);
  });

  test("never narrows to chainEarliest on a window the user explicitly paged to", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({
      status: 200,
      data: { ...ledgerStub, chainEarliest: "2026-08-21T12:45:00+02:00" },
    });

    const wrapper = mountCard({
      from: "2026-08-01T00:00:00.000Z",
      to: "2026-08-08T00:00:00.000Z", // well before NOW -> isAtPresent is false
    });

    await flushPromises();

    expect(api.get).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[data-testid="savings-ledger-content"]').exists()).toBe(true);
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

// liveSample's Control (-EUR 0.41 at the periodAverage lens) is SMALLER than that
// period's own measurement noise (meterResidual.eurBand, EUR 0.625), so the card must not
// call it a loss. overspendSample is the same payload with a quiet meter, which is what
// the overspend clause needs to be exercised at all.
function overspendSample() {
  const s = JSON.parse(JSON.stringify(liveSample));
  s.chain.meterResidual = { sumKWh: -0.02, absSumKWh: 0.15, slots: 84, eurBand: 0.05 };
  return s;
}

describe("SavingsLedgerCard presentation", () => {
  // the real API response the diagram was built against
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

  // F2: control_slots is written whenever the optimizer is enabled and sponsored, with
  // no battery check, so a PV-and-loadpoint site records rows too - all of them
  // "unknown"/"unknown", because batteryModeCandidate has no controllable battery to
  // iterate. Rendering them told such a site its nonexistent battery was in "Normal
  // operation". chain.batteryPhysics is present exactly when there IS a battery
  // (computeChainFromSlots derives it only under set.HasBattery), so that is the test.
  test("emits no decisions for a site whose chain shows it has no battery", async () => {
    const noBattery = JSON.parse(JSON.stringify(liveSample));
    delete noBattery.chain.batteryPhysics;
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: noBattery });

    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    const emitted = wrapper.emitted("update:decisions")!;
    expect(noBattery.decisions.length).toBeGreaterThan(0); // the rows were there to emit
    expect(emitted[emitted.length - 1]![0]).toBeNull();
    // the card itself is unaffected: a battery-less chain is still a real chain
    expect(wrapper.find('[data-testid="savings-ledger-content"]').exists()).toBe(true);
  });

  // the converse: an absent chain is chainUnavailable, a site that HAS a battery whose
  // physics could not be derived. Those rows are real decisions and must still show.
  test("still emits decisions when the chain itself is unavailable", async () => {
    const noChain = JSON.parse(JSON.stringify(liveSample));
    delete noChain.chain;
    noChain.chainUnavailable = "not enough battery history to derive capacity";
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: noChain });

    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    const emitted = wrapper.emitted("update:decisions")!;
    expect(emitted[emitted.length - 1]![0]).toHaveLength(noChain.decisions.length);
  });

  // F6: the three figures used to be bottom-aligned by a full-height flex column whose
  // label absorbed the slack - which only holds while all three values are one line
  // tall, and the third ("EUR 1,234.56 (91%)") is the longest of the three in a col-4 at
  // 390px. Labels and values are now two passes over the same list, so .row's own wrap
  // puts every label on one grid line and every value on the next: alignment survives a
  // value that wraps. Asserted as DOM order, the thing the CSS depends on.
  test("every label precedes every value, so the figures share one grid row", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    const cols = wrapper.find('[data-testid="savings-ledger-details"]').element.children;
    const hasValue = [...cols].map((c) => c.querySelector("[data-testid]") !== null);
    expect(hasValue).toEqual([false, false, false, true, true, true]);
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
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: overspendSample() });
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

  // N2: the threshold for asserting a direction used to be a hardcoded half-cent, a
  // hundred times below the uncertainty the same payload publishes - so the card rendered
  // "the controller cost you €0.41" in the danger colour under a residual worth €0.63.
  test("refuses to call a Control figure inside the period's noise a loss", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    expect(wrapper.find('[data-testid="savings-ledger-control-overspend"]').exists()).toBe(false);

    const clause = wrapper.find('[data-testid="savings-ledger-control-inside-noise"]');
    expect(clause.exists()).toBe(true);
    // the figure is still named - never hidden, never rounded to zero - beside the band
    // that makes its sign unusable
    expect(clause.text()).toContain("control moved €0.41");
    expect(clause.text()).toContain("€0.63");
    expect(clause.text()).toContain("too small to call");
    expect(clause.classes()).not.toContain("text-danger");
  });

  test("says nothing about Control when Control saved money", async () => {
    const saved = overspendSample();
    // W2 -> W3 now favours the real controller at both lenses
    saved.chain.contributions[2].settled = { perSlot: 0.5, periodAverage: 0.5 };

    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: saved });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    expect(wrapper.find('[data-testid="savings-ledger-control-overspend"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="savings-ledger-control-inside-noise"]').exists()).toBe(
      false
    );
  });

  test("names the EV-charge-timing non-attribution under the chart, not only in the modal", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    const line = wrapper.find('[data-testid="savings-ledger-ev-timing"]');
    expect(line.exists()).toBe(true);
    expect(line.text()).toContain("cheaper hour");
    // the API's own full sentence is still handed to the modal, never dropped
    expect((wrapper.vm as any).notes).toContain(EV_TIMING_NOTE);
  });

  test("drops the EV-timing line for a site whose payload never mentions it", async () => {
    const noEv = JSON.parse(JSON.stringify(liveSample));
    noEv.chain.notes = noEv.chain.notes.filter((n: string) => n !== EV_TIMING_NOTE);

    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: noEv });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    expect(wrapper.find('[data-testid="savings-ledger-ev-timing"]').exists()).toBe(false);
  });

  // N0: when the chain covers materially fewer slots than the period, every figure in the
  // strip above is the chain's - "you paid EUR 0.60" is its W3 over its own subset, not
  // the period's bill. The caption has to lead with the diagram's coverage, and the
  // period's real figure has to be named rather than implied wrongly.
  test("leads with the diagram's coverage and names the period's real bill when they diverge", async () => {
    const diverged = JSON.parse(JSON.stringify(liveSample));
    diverged.realised.coverage = { validSlots: 584, totalSlots: 635, fraction: 584 / 635 };
    diverged.realised.settled = { perSlot: 35.746, periodAverage: 34.6859 };
    diverged.chain.coverage = { validSlots: 254, totalSlots: 635, fraction: 254 / 635 };

    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: diverged });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    const caption = wrapper.find('[data-testid="savings-ledger-caption"]').text();
    expect(caption.indexOf("diagram over 40.0%")).toBeGreaterThan(-1);
    // the diagram's coverage comes BEFORE the realised one, because the figures above
    // it are the diagram's
    expect(caption.indexOf("diagram over 40.0%")).toBeLessThan(caption.indexOf("92.0% of slots"));

    const warning = wrapper.find('[data-testid="savings-ledger-diagram-subset"]');
    expect(warning.exists()).toBe(true);
    expect(warning.text()).toContain("40.0%");
    expect(warning.text()).toContain("€34.69"); // what the period actually cost
  });

  // D7: "materially fewer" was the comment, "any difference at all" was the code. One
  // dropped PV read is a difference; it is not a reason to raise a standing warning whose
  // two euro figures differ by cents.
  test("says nothing about a subset over a one-slot difference", async () => {
    const barely = JSON.parse(JSON.stringify(liveSample));
    barely.realised.coverage = { validSlots: 670, totalSlots: 672, fraction: 670 / 672 };
    barely.chain.coverage = { validSlots: 669, totalSlots: 672, fraction: 669 / 672 };

    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: barely });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    expect(wrapper.find('[data-testid="savings-ledger-diagram-subset"]').exists()).toBe(false);
    // the caption still reports BOTH coverages - nothing is hidden, only the banner is
    // held back until the two figures actually say different things
    expect(wrapper.find('[data-testid="savings-ledger-caption"]').text()).toContain("99.6%");
  });

  test("says nothing about a subset when the diagram covers the same slots as the period", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    expect(wrapper.find('[data-testid="savings-ledger-diagram-subset"]').exists()).toBe(false);
  });

  // N5: realised.note now appends the static-feed-in disclosure to the invoice caveat, so
  // it is no longer string-equal to chain.notes[0] and a Set-based dedupe rendered the
  // invoice sentence twice.
  test("renders the shared invoice caveat once even when one side has appended to it", async () => {
    const appended = JSON.parse(JSON.stringify(liveSample));
    appended.realised.note = `${appended.chain.notes[0]}; no feed-in price was recorded for 416 of the slots behind this figure`;

    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: appended });
    const wrapper = mountCard(PRESENT_WINDOW);
    await flushPromises();

    const notes = (wrapper.vm as any).notes as string[];
    const invoiceLines = notes.filter((n) => n.startsWith("prices only the grid tariff rate"));
    expect(invoiceLines).toHaveLength(1);
    // the LONGER form survives - the appended disclosure is never the thing dropped
    expect(invoiceLines[0]).toContain("416 of the slots");
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
