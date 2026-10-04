import { shallowMount } from "@vue/test-utils";
import { describe, expect, test } from "vite-plus/test";
import DateNavigator from "./DateNavigator.vue";

const months = (year: number) =>
  shallowMount(DateNavigator, {
    props: {
      year,
      month: 1,
      startDate: new Date(2025, 10, 15),
      today: new Date(2026, 2, 2),
      showMonth: true,
      showYear: true,
    },
    global: { mocks: { $i18n: { locale: "en" } } },
  }).vm.monthOptions.map((option) => option.value);

describe("date navigator month picker", () => {
  test("lists no future months and no months before the start date", () => {
    expect(months(2026)).toEqual([1, 2, 3]);
    expect(months(2025)).toEqual([11, 12]);
  });
});
