import { shallowMount } from "@vue/test-utils";
import { describe, expect, test } from "vite-plus/test";
import PriceChart from "./PriceChart.vue";
import { OPTIMIZER_VETO_REASON } from "@/types/evcc";

describe("price chart control annotation", () => {
  test.each([
    [3, undefined, "forecast.optimizer.modeCharge"],
    [
      2,
      OPTIMIZER_VETO_REASON.DAMPING,
      "forecast.optimizer.modeHold (forecast.optimizer.reasonDamping)",
    ],
    [0, undefined, undefined],
  ] as const)("decodes wire mode %s", (mode, vetoReason, expected) => {
    const wrapper = shallowMount(PriceChart, {
      props: {
        grid: [],
        chartWidth: 600,
        endDate: new Date(Date.now() + 3600_000),
        optimizerAutomatic: true,
        optimizerDecision: {
          mode,
          vetoReason,
          chargeVetoed: !!vetoReason,
          updated: new Date().toISOString(),
          validFor: 360_000,
        },
      },
      global: { mocks: { $t: (key: string) => key, $i18n: { locale: "en" } } },
    });
    expect(wrapper.vm.optimizerDecisionLabel).toBe(expected);
    expect(
      wrapper.vm.markLineData.some(
        (line) => (line["label"] as { formatter?: string } | undefined)?.formatter === expected
      )
    ).toBe(expected !== undefined);
    wrapper.unmount();
  });
});
