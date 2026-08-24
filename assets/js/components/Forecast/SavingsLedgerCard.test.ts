import { shallowMount, config, flushPromises } from "@vue/test-utils";
import { nextTick } from "vue";
import { describe, expect, test, vi, beforeEach, afterEach } from "vite-plus/test";
import en from "../../../../i18n/en.json";

// api is mocked wholesale so importing the card never pulls in the real axios instance,
// which reads window.location at module init and drags in the auth modal via its
// error interceptor
vi.mock("@/api", () => ({ default: { get: vi.fn() } }));

// eslint-disable-next-line import/first
import api from "@/api";
// eslint-disable-next-line import/first
import SavingsLedgerCard from "./SavingsLedgerCard.vue";
// eslint-disable-next-line import/first
import liveSample from "./__fixtures__/ledgerLiveSample";
// eslint-disable-next-line import/first
import { EV_TIMING_NOTE } from "./savingsLedgerChain";

// minimal $t/$te that walk en.json so tests assert on real English text, with
// {placeholder} interpolation, which several of this card's strings use
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
// Card.vue's markup lives inside <Card>'s default slot, and a shallow stub does not
// render slot content unless told to
config.global.renderStubDefaultSlot = true;

// The card's period is seeded from the clock. This instant is already slot-aligned and
// puts the default window at 2026-08-17T00:00Z..2026-08-24T00:00Z, which the clamp tests
// assert against. Every isAtPresent check reads `new Date()`, so it must match.
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

function mountCard() {
  // shallow: these tests are about which top-level branch the card's template picks and
  // how many requests it issues, not about its children's internals
  return shallowMount(SavingsLedgerCard);
}

// paging is the only thing that makes isAtPresent false, which the auto-clamps below
// are gated on
const pageBack = async (wrapper: any) => {
  (wrapper.vm as any).page(-1);
  await flushPromises();
};

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

    const wrapper = mountCard();

    await flushPromises(); // first (422) request
    await flushPromises(); // clamped win reassignment -> second (200) request

    expect(api.get).toHaveBeenCalledTimes(2);
    const secondCallParams = vi.mocked(api.get).mock.calls[1]![1] as any;
    expect(secondCallParams.params.from).toBe("2026-08-21T10:30:00.000Z"); // ceil-aligned earliest
    expect(secondCallParams.params.to).toBe("2026-08-24T00:00:00.000Z"); // unchanged

    expect(wrapper.find('[data-testid="savings-ledger-content"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="savings-ledger-refuse"]').exists()).toBe(false);
    // no chain in this stub: the strip falls back to the one measured figure and
    // shows no baseline or saved column it cannot honestly compute
    expect(wrapper.find('[data-testid="savings-ledger-detail-paid"]').text()).toContain("10");
    expect(wrapper.find('[data-testid="savings-ledger-detail-baseline"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="savings-ledger-detail-saved"]').exists()).toBe(false);
  });

  // a successful response can still be one the diagram cannot be drawn over: a tariff
  // table older than the battery makes a default window ACCEPTED but computes the chain
  // over far fewer slots than the realised figure, and every figure drawn is the chain's
  test("narrows the present window to where the chain can actually be drawn", async () => {
    vi.mocked(api.get)
      .mockResolvedValueOnce({
        status: 200,
        data: { ...ledgerStub, chainEarliest: "2026-08-21T12:45:00+02:00" },
      })
      .mockResolvedValueOnce({ status: 200, data: ledgerStub });

    mountCard();

    await flushPromises();
    await flushPromises();

    expect(api.get).toHaveBeenCalledTimes(2);
    const secondCallParams = vi.mocked(api.get).mock.calls[1]![1] as any;
    expect(secondCallParams.params.from).toBe("2026-08-21T10:45:00.000Z");
    expect(secondCallParams.params.to).toBe("2026-08-24T00:00:00.000Z");
  });

  // the two clamps can be SEQUENTIAL: a default window starting before both the tariff
  // history and the battery. Sharing one flag lets the first consume the only attempt.
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

    const wrapper = mountCard();

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
    vi.mocked(api.get)
      .mockResolvedValueOnce({ status: 200, data: ledgerStub }) // the mount fetch
      .mockResolvedValueOnce({
        // this sits INSIDE the paged window, so the clamp would succeed and isAtPresent is
        // the only thing declining it
        status: 200,
        data: { ...ledgerStub, chainEarliest: "2026-08-14T02:00:00+02:00" },
      });

    const wrapper = mountCard();
    await flushPromises();
    await pageBack(wrapper); // now well before NOW -> isAtPresent is false

    // no third request: the chosen window renders as-is, never silently narrowed
    expect(api.get).toHaveBeenCalledTimes(2);
    const secondCallParams = vi.mocked(api.get).mock.calls[1]![1] as any;
    expect(secondCallParams.params.from).toBe("2026-08-10T00:00:00.000Z");
    expect(wrapper.find('[data-testid="savings-ledger-content"]').exists()).toBe(true);
  });

  // A user who paged back into a genuine refusal must see that refusal, not get silently
  // moved to a period they never asked for. The clamp here WOULD succeed, so isAtPresent
  // is the only thing preventing it.
  test("a 422 with earliest while paged into the past renders the plain refusal and does not clamp", async () => {
    vi.mocked(api.get)
      .mockResolvedValueOnce({ status: 200, data: ledgerStub }) // the mount fetch
      .mockResolvedValueOnce({
        status: 422,
        data: {
          error: "no tariff data before 2026-08-14T02:00:00+02:00",
          earliest: "2026-08-14T02:00:00+02:00",
        },
      });

    const wrapper = mountCard();
    await flushPromises();
    await pageBack(wrapper); // now well before NOW -> isAtPresent is false

    expect(api.get).toHaveBeenCalledTimes(2); // no auto-retry
    expect(wrapper.find('[data-testid="savings-ledger-refuse"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="savings-ledger-refuse-body"]').text()).toBe(
      "no tariff data before 2026-08-14T02:00:00+02:00"
    );
    expect(wrapper.find('[data-testid="savings-ledger-content"]').exists()).toBe(false);
  });
});

// liveSample's Control is inside its period's own measurement noise, so the card must
// not call it a loss. overspendSample is the same payload with a quiet meter, which the
// overspend clause needs to be exercised at all.
function overspendSample() {
  const s = JSON.parse(JSON.stringify(liveSample));
  s.chain.meterResidual = { sumKWh: -0.02, absSumKWh: 0.15, slots: 84, eurBand: 0.05 };
  return s;
}

describe("SavingsLedgerCard presentation", () => {
  // these tests are about what the card renders, not about clamping, so the clock is set
  // where the default window already starts after liveSample's chainEarliest and every
  // test below expects exactly one request
  beforeEach(() => {
    vi.setSystemTime(new Date("2026-08-28T12:00:00.000Z"));
  });

  const detail = (wrapper: any, key: string) =>
    wrapper.find(`[data-testid="savings-ledger-detail-${key}"]`);

  test("binds the info icon to the title's last word without splitting the title", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard();
    await flushPromises();

    const title = wrapper.find('[data-testid="savings-ledger-title"]');
    expect(title.text()).toContain("What the system did with your money");
    expect(title.element.textContent).toContain("money\u00a0");
  });

  test("renders the three-figure strip from the chain, at the default headline", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard();
    await flushPromises();

    expect(api.get).toHaveBeenCalledTimes(1);
    expect(detail(wrapper, "baseline").text()).toBe("€6.67");
    expect(detail(wrapper, "paid").text()).toBe("€0.60");
    expect(detail(wrapper, "saved").text()).toBe("€6.06 (91%)");
    expect(detail(wrapper, "saved").classes()).toContain("text-primary");
  });

  test("keeps the estimate marker and coverage visible without interaction", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard();
    await flushPromises();

    const caption = wrapper.find('[data-testid="savings-ledger-caption"]').text();
    expect(caption).toContain("battery and control are estimated");
    // the valid/total parenthetical stays: fraction is 0 for two different reasons
    // (no slots at all vs. no valid slots) and only the counts disambiguate them
    expect(caption).toContain("43.8% of slots (84 of 192)");
  });

  test("names a loss a loss and never clamps it to zero", async () => {
    const loss = JSON.parse(JSON.stringify(liveSample));
    loss.chain.worlds[3].settled.periodAverage = 8.0; // W0 is 6.6656
    loss.chain.contributions[2].settled.periodAverage = -5.0;

    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: loss });
    const wrapper = mountCard();
    await flushPromises();

    const saved = detail(wrapper, "saved");
    expect(wrapper.text()).toContain("cost you");
    expect(saved.text()).toBe("€1.33 (20%)");
    expect(saved.classes()).toContain("text-danger");
  });

  test("hands the decisions rows upward instead of letting a second card re-fetch them", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard();
    await flushPromises();

    expect(api.get).toHaveBeenCalledTimes(1);
    const emitted = wrapper.emitted("update:decisions");
    expect(emitted).toHaveLength(2);
    expect(emitted![0]![0]).toBeNull();
    expect(emitted![1]![0]).toHaveLength(42); // ledgerLiveSample's recorded slots
  });

  // control_slots is written whenever the optimizer runs, with no battery check, so a
  // PV-and-loadpoint site records rows too - all "unknown"/"unknown", which would tell
  // such a site its nonexistent battery was in "Normal operation". chain.batteryPhysics
  // is present exactly when there IS a battery, so that is the test.
  test("emits no decisions for a site whose chain shows it has no battery", async () => {
    const noBattery = JSON.parse(JSON.stringify(liveSample));
    delete noBattery.chain.batteryPhysics;
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: noBattery });

    const wrapper = mountCard();
    await flushPromises();

    const emitted = wrapper.emitted("update:decisions")!;
    expect(noBattery.decisions.length).toBeGreaterThan(0); // the rows were there to emit
    expect(emitted[emitted.length - 1]![0]).toBeNull();
    expect(wrapper.find('[data-testid="savings-ledger-content"]').exists()).toBe(true);
  });

  // the converse: an absent chain means a site that HAS a battery whose physics could
  // not be derived, and those rows are real decisions
  test("still emits decisions when the chain itself is unavailable", async () => {
    const noChain = JSON.parse(JSON.stringify(liveSample));
    delete noChain.chain;
    noChain.chainUnavailable = "not enough battery history to derive capacity";
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: noChain });

    const wrapper = mountCard();
    await flushPromises();

    const emitted = wrapper.emitted("update:decisions")!;
    expect(emitted[emitted.length - 1]![0]).toHaveLength(42);
  });

  // Labels and values are two passes over the same list, so .row's own wrap puts every
  // label on one grid line and every value on the next and alignment survives a value
  // that wraps. Asserted as DOM order, which is what the CSS depends on.
  test("every label precedes every value, so the figures share one grid row", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard();
    await flushPromises();

    const cols = wrapper.find('[data-testid="savings-ledger-details"]').element.children;
    const hasValue = [...cols].map((c) => c.querySelector("[data-testid]") !== null);
    expect(hasValue).toEqual([false, false, false, true, true, true]);
  });

  test("takes the decisions card down while the next period is still loading", async () => {
    vi.mocked(api.get)
      .mockResolvedValueOnce({ status: 200, data: liveSample })
      // a distinct object: `ledger` is watched by identity, so the same reference twice
      // would make the second response a no-op
      .mockResolvedValueOnce({ status: 200, data: JSON.parse(JSON.stringify(liveSample)) });

    const wrapper = mountCard();
    await flushPromises();
    expect(wrapper.emitted("update:decisions")).toHaveLength(2);

    // the card below must not go on showing the previous period's ticks and euro deltas
    (wrapper.vm as any).page(-1);
    await wrapper.vm.$nextTick();
    const midFlight = wrapper.emitted("update:decisions")!;
    expect(midFlight).toHaveLength(3);
    expect(midFlight[2]![0]).toBeNull();
    expect(wrapper.find('[data-testid="savings-ledger-loading"]').exists()).toBe(true);

    await flushPromises();
    const landed = wrapper.emitted("update:decisions")!;
    expect(landed).toHaveLength(4);
    expect(landed[3]![0]).toHaveLength(42);
  });

  test("takes the decisions card down again when the period is refused", async () => {
    vi.mocked(api.get)
      .mockResolvedValueOnce({ status: 200, data: liveSample })
      .mockResolvedValueOnce({ status: 400, data: { error: "range unaligned" } });

    const wrapper = mountCard();
    await flushPromises();
    (wrapper.vm as any).page(-1);
    await flushPromises();

    const emitted = wrapper.emitted("update:decisions");
    expect(emitted).toHaveLength(4);
    expect(emitted![3]![0]).toBeNull();
    expect(wrapper.find('[data-testid="savings-ledger-refuse"]').exists()).toBe(true);
  });

  test("names a Control overspend under the chart even when the period headlines a saving", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: overspendSample() });
    const wrapper = mountCard();
    await flushPromises();

    // the strip legitimately reads a large saving while the Control step itself lost money
    expect(detail(wrapper, "saved").text()).toBe("€6.06 (91%)");
    const clause = wrapper.find('[data-testid="savings-ledger-control-overspend"]');
    expect(clause.exists()).toBe(true);
    expect(clause.text()).toContain("control cost €0.41 against its baseline");
    expect(clause.classes()).toContain("text-danger");
  });

  // the threshold for asserting a direction is the period's own published uncertainty,
  // not a hardcoded half-cent two orders of magnitude below it
  test("refuses to call a Control figure inside the period's noise a loss", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard();
    await flushPromises();

    expect(wrapper.find('[data-testid="savings-ledger-control-overspend"]').exists()).toBe(false);

    const clause = wrapper.find('[data-testid="savings-ledger-control-inside-noise"]');
    expect(clause.exists()).toBe(true);
    // the figure is still named, beside the band that makes its sign unusable
    expect(clause.text()).toContain("control moved €0.41");
    expect(clause.text()).toContain("€0.63");
    expect(clause.text()).toContain("too small to call");
    expect(clause.classes()).not.toContain("text-danger");
  });

  test("says nothing about Control when Control saved money", async () => {
    const saved = overspendSample();
    saved.chain.contributions[2].settled = { perSlot: 0.5, periodAverage: 0.5 };

    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: saved });
    const wrapper = mountCard();
    await flushPromises();

    expect(wrapper.find('[data-testid="savings-ledger-control-overspend"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="savings-ledger-control-inside-noise"]').exists()).toBe(
      false
    );
  });

  test("names the EV-charge-timing non-attribution under the chart, not only in the modal", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard();
    await flushPromises();

    const line = wrapper.find('[data-testid="savings-ledger-ev-timing"]');
    expect(line.exists()).toBe(true);
    expect(line.text()).toContain("cheaper hour");
    expect((wrapper.vm as any).notes).toContain(EV_TIMING_NOTE);
  });

  test("drops the EV-timing line for a site whose payload never mentions it", async () => {
    const noEv = JSON.parse(JSON.stringify(liveSample));
    noEv.chain.notes = noEv.chain.notes.filter((n: string) => n !== EV_TIMING_NOTE);

    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: noEv });
    const wrapper = mountCard();
    await flushPromises();

    expect(wrapper.find('[data-testid="savings-ledger-ev-timing"]').exists()).toBe(false);
  });

  // when the chain covers materially fewer slots than the period, every figure in the
  // strip above is the chain's W3 over its own subset, not the period's bill. The caption
  // leads with the diagram's coverage and the period's real figure is named.
  test("leads with the diagram's coverage and names the period's real bill when they diverge", async () => {
    const diverged = JSON.parse(JSON.stringify(liveSample));
    diverged.realised.coverage = { validSlots: 584, totalSlots: 635, fraction: 584 / 635 };
    diverged.realised.settled = { perSlot: 35.746, periodAverage: 34.6859 };
    diverged.chain.coverage = { validSlots: 254, totalSlots: 635, fraction: 254 / 635 };

    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: diverged });
    const wrapper = mountCard();
    await flushPromises();

    const caption = wrapper.find('[data-testid="savings-ledger-caption"]').text();
    expect(caption.indexOf("diagram over 40.0%")).toBeGreaterThan(-1);
    // the diagram's coverage comes BEFORE the realised one, because the figures above it
    // are the diagram's
    expect(caption.indexOf("diagram over 40.0%")).toBeLessThan(caption.indexOf("92.0% of slots"));

    const warning = wrapper.find('[data-testid="savings-ledger-diagram-subset"]');
    expect(warning.exists()).toBe(true);
    expect(warning.text()).toContain("40.0%");
    expect(warning.text()).toContain("€34.69"); // what the period actually cost
  });

  // one dropped read is a difference, but not a reason to raise a standing warning whose
  // two euro figures differ by cents
  test("says nothing about a subset over a one-slot difference", async () => {
    const barely = JSON.parse(JSON.stringify(liveSample));
    barely.realised.coverage = { validSlots: 670, totalSlots: 672, fraction: 670 / 672 };
    barely.chain.coverage = { validSlots: 669, totalSlots: 672, fraction: 669 / 672 };

    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: barely });
    const wrapper = mountCard();
    await flushPromises();

    expect(wrapper.find('[data-testid="savings-ledger-diagram-subset"]').exists()).toBe(false);
    // the caption still reports BOTH coverages: only the banner is held back
    expect(wrapper.find('[data-testid="savings-ledger-caption"]').text()).toContain("99.6%");
  });

  test("says nothing about a subset when the diagram covers the same slots as the period", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard();
    await flushPromises();

    expect(wrapper.find('[data-testid="savings-ledger-diagram-subset"]').exists()).toBe(false);
  });

  // realised.note appends its own disclosure to the invoice caveat, so it is not
  // string-equal to chain.notes[0] and a Set-based dedupe renders it twice
  test("renders the shared invoice caveat once even when one side has appended to it", async () => {
    const appended = JSON.parse(JSON.stringify(liveSample));
    appended.realised.note = `${appended.chain.notes[0]}; no feed-in price was recorded for 416 of the slots behind this figure`;

    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: appended });
    const wrapper = mountCard();
    await flushPromises();

    const notes = (wrapper.vm as any).notes as string[];
    const invoiceLines = notes.filter((n) => n.startsWith("prices only the grid tariff rate"));
    expect(invoiceLines).toHaveLength(1);
    // the LONGER form survives: the appended disclosure is never the thing dropped
    expect(invoiceLines[0]).toContain("416 of the slots");
  });

  test("offers the caveats behind an info control rather than dropping them", async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ status: 200, data: liveSample });
    const wrapper = mountCard();
    await flushPromises();

    expect(wrapper.find('[data-testid="savings-ledger-info-icon"]').exists()).toBe(true);
    const notes = (wrapper.vm as any).notes as string[];
    expect(notes).toHaveLength(11);
    expect(new Set(notes).size).toBe(notes.length);
    expect(notes).toContain(liveSample.realised.note);
  });
});

describe("SavingsLedgerCard overlapping requests", () => {
  // Paging twice quickly leaves two requests in flight, and the second `loading = true`
  // is a no-op, so without a sequence gate whichever response lands LAST wins - which for
  // ordinary HTTP can be the first period's, under the second period's label.
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

    const wrapper = mountCard();
    await flushPromises();

    const vm = wrapper.vm as any;
    vm.page(-1);
    await nextTick();
    vm.page(-1);
    await nextTick();
    expect(api.get).toHaveBeenCalledTimes(3);

    resolveSecond({ status: 200, data: stub(22) });
    await flushPromises();
    resolveFirst({ status: 200, data: stub(11) });
    await flushPromises();

    expect(wrapper.find('[data-testid="savings-ledger-detail-paid"]').text()).toContain("22");
    expect(wrapper.find('[data-testid="savings-ledger-detail-paid"]').text()).not.toContain("11");
    expect(vm.loading).toBe(false);
  });
});
