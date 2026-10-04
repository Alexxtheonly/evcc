import { shallowMount } from "@vue/test-utils";
import { describe, expect, test } from "vite-plus/test";
import DateNavigator from "./DateNavigator.vue";

const mountNavigator = (year: number, month = 1, showMonth = true) =>
  shallowMount(DateNavigator, {
    props: {
      year,
      month,
      startDate: new Date(2025, 10, 15),
      today: new Date(2026, 2, 2),
      showMonth,
      showYear: true,
    },
    global: { mocks: { $i18n: { locale: "en" } } },
  });
const months = (year: number) => mountNavigator(year).vm.monthOptions.map((option) => option.value);

describe("date navigator month picker", () => {
  test("lists no future months and no months before the start date", () => {
    expect(months(2026)).toEqual([1, 2, 3]);
    expect(months(2025)).toEqual([11, 12]);
  });

  test("a year switch clamps the month into the new year's options", () => {
    const march = mountNavigator(2026, 3);
    march.vm.emitYear(2025);
    expect(march.emitted("update-date")?.at(-1)?.[0]).toEqual({ year: 2025, month: 11 });
    const december = mountNavigator(2025, 12);
    december.vm.emitNextYear();
    expect(december.emitted("update-date")?.at(-1)?.[0]).toEqual({ year: 2026, month: 3 });
    const yearOnly = mountNavigator(2026, 3, false);
    yearOnly.vm.emitPrevYear();
    expect(yearOnly.emitted("update-date")?.at(-1)?.[0]).toEqual({ year: 2025, month: undefined });
  });
});
