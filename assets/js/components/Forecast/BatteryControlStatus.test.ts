import { mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, test, vi } from "vite-plus/test";
import BatteryControlStatus from "./BatteryControlStatus.vue";
import { BATTERY_MODE, OPTIMIZER_VETO_REASON, type OptimizerDecision } from "@/types/evcc";

const global = {
  mocks: {
    $t: (key: string, args?: Record<string, unknown>) =>
      `${key} ${args ? JSON.stringify(args) : ""}`,
    $i18n: { locale: "en" },
  },
};
const start = "2026-10-03T11:50:00Z";
const decision: OptimizerDecision = {
  mode: BATTERY_MODE.HOLD,
  chargeVetoed: true,
  vetoReason: OPTIMIZER_VETO_REASON.DAMPING,
  updated: start,
  validFor: 600_000,
};

describe("battery control status", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(start);
  });
  afterEach(() => vi.useRealTimers());

  test("distinguishes actual command from a legacy pending confirmation", () => {
    const wrapper = mount(BatteryControlStatus, {
      props: { automatic: true, decision, appliedMode: BATTERY_MODE.NORMAL },
      global,
    });
    expect(wrapper.text()).toContain('"mode":"forecast.optimizer.modeNormal');
    expect(wrapper.text()).toContain("forecast.energy.control.confirming");
    expect(wrapper.text()).not.toContain("forecast.energy.control.eligibleIn");
    wrapper.unmount();
  });

  test("expires without another server update", async () => {
    const wrapper = mount(BatteryControlStatus, { props: { automatic: true, decision }, global });
    await vi.advanceTimersByTimeAsync(600_000);
    expect(wrapper.text()).toContain("forecast.energy.control.stale");
    expect(wrapper.text()).not.toContain("forecast.energy.control.confirming");
    wrapper.unmount();
  });

  test("advisory and offline never present a pending action as active", async () => {
    const wrapper = mount(BatteryControlStatus, { props: { automatic: false, decision }, global });
    expect(wrapper.text()).toContain("forecast.energy.control.advisory");
    expect(wrapper.text()).not.toContain("forecast.energy.control.confirming");
    await wrapper.setProps({ automatic: true, offline: true });
    expect(wrapper.text()).toContain("forecast.energy.control.offline");
    expect(wrapper.text()).not.toContain("forecast.energy.control.confirming");
    wrapper.unmount();
  });
  test("counts down to eligibility, never promises charging at zero", async () => {
    const wrapper = mount(BatteryControlStatus, {
      props: {
        automatic: true,
        decision: {
          ...decision,
          appliedMode: 2,
          candidateMode: 3,
          pendingMode: 3,
          pendingSince: start,
          eligibleAt: "2026-10-03T11:55:00Z",
        },
      },
      global,
    });
    expect(wrapper.text()).toContain('"time":"5:00"');
    expect(wrapper.text()).toContain('"mode":"forecast.optimizer.modeHold');
    expect(wrapper.text()).toContain('"mode":"forecast.optimizer.modeCharge');
    await vi.advanceTimersByTimeAsync(120_000);
    expect(wrapper.text()).toContain('"time":"3:00"');
    await vi.advanceTimersByTimeAsync(180_000);
    expect(wrapper.text()).toContain("forecast.energy.control.awaitingCheck");
    expect(wrapper.text()).not.toContain("forecast.energy.control.eligibleIn");
    await wrapper.setProps({
      decision: {
        ...decision,
        mode: 3,
        appliedMode: 3,
        chargeVetoed: false,
        vetoReason: undefined,
      },
    });
    expect(wrapper.text()).not.toContain("forecast.energy.control.pending");
    expect(wrapper.text()).not.toContain("forecast.energy.control.awaitingCheck");
    wrapper.unmount();
  });

  test("a changed candidate restarts the displayed countdown and explains why", async () => {
    const wrapper = mount(BatteryControlStatus, {
      props: {
        automatic: true,
        decision: { ...decision, pendingMode: 3, eligibleAt: "2026-10-03T11:55:00Z" },
      },
      global,
    });
    await vi.advanceTimersByTimeAsync(60_000);
    await wrapper.setProps({
      decision: {
        ...decision,
        pendingMode: 2,
        eligibleAt: "2026-10-03T11:56:00Z",
        resetReason: "candidateChanged",
      },
    });
    expect(wrapper.text()).toContain('"time":"5:00"');
    expect(wrapper.text()).toContain("forecast.energy.control.candidateChanged");
    wrapper.unmount();
  });

  test("mode zero is unknown, and expiry also hides economic details", async () => {
    const wrapper = mount(BatteryControlStatus, {
      props: {
        automatic: true,
        decision: {
          ...decision,
          appliedMode: 0,
          expiresAt: "2026-10-03T11:51:00Z",
          economics: {
            bestAction: "charge",
            selectedAction: "hold",
            reason: "economicTie",
            regretGap: 0.000014,
            materiality: 0.001,
            evaluations: [],
          },
        },
      },
      global,
    });
    expect(wrapper.text()).toContain("forecast.energy.control.unknownMode");
    expect(wrapper.text()).not.toContain("forecast.optimizer.modeNormal");
    expect(wrapper.text()).toContain("0.000014");
    expect(wrapper.text()).toContain("forecast.energy.control.economicTie");
    await vi.advanceTimersByTimeAsync(60_000);
    expect(wrapper.find("details").exists()).toBe(false);
    wrapper.unmount();
  });

  test("no pending action has no countdown even with a leftover timestamp", () => {
    const wrapper = mount(BatteryControlStatus, {
      props: {
        automatic: true,
        decision: { ...decision, vetoReason: undefined, eligibleAt: "2026-10-03T11:55:00Z" },
      },
      global,
    });
    expect(wrapper.text()).not.toContain("forecast.energy.control.pending");
    expect(wrapper.text()).not.toContain("forecast.energy.control.eligibleIn");
    wrapper.unmount();
  });
  test("fresh device command takes precedence over the site request", async () => {
    const wrapper = mount(BatteryControlStatus, {
      props: {
        automatic: true,
        appliedMode: BATTERY_MODE.CHARGE,
        decision: { ...decision, appliedMode: 2 },
      },
      global,
    });
    expect(wrapper.text()).toContain("forecast.energy.control.applied");
    expect(wrapper.text()).toContain('"mode":"forecast.optimizer.modeHold');
    expect(wrapper.text()).not.toContain("forecast.optimizer.modeCharge");
    await wrapper.setProps({ decision: { ...decision, appliedMode: 0 } });
    expect(wrapper.text()).toContain("forecast.energy.control.unknownMode");
    expect(wrapper.text()).not.toContain("forecast.optimizer.modeCharge");
    await vi.advanceTimersByTimeAsync(600_000);
    expect(wrapper.text()).toContain("forecast.energy.control.siteMode");
    expect(wrapper.text()).toContain('"mode":"forecast.optimizer.modeCharge');
    expect(wrapper.text()).not.toContain("forecast.energy.control.applied");
    wrapper.unmount();
  });
  test("pending expiry ends confirmation without hiding a fresh device command", async () => {
    const wrapper = mount(BatteryControlStatus, {
      props: {
        automatic: true,
        decision: {
          ...decision,
          appliedMode: 2,
          pendingMode: 3,
          eligibleAt: "2026-10-03T11:51:00Z",
          pendingExpiresAt: "2026-10-03T11:52:00Z",
          expiresAt: "2026-10-03T11:56:00Z",
        },
      },
      global,
    });
    await vi.advanceTimersByTimeAsync(120_000);
    expect(wrapper.text()).toContain("forecast.energy.control.pendingExpired");
    expect(wrapper.text()).toContain('"mode":"forecast.optimizer.modeHold');
    expect(wrapper.text()).toContain("forecast.energy.control.applied");
    expect(wrapper.text()).not.toContain("forecast.energy.control.awaitingCheck");
    expect(wrapper.text()).not.toContain("forecast.energy.control.eligibleIn");
    expect(wrapper.text()).not.toContain("forecast.energy.control.stale");
    wrapper.unmount();
  });
});
