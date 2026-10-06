import { beforeEach, describe, expect, test, vi } from "vite-plus/test";
import { nextTick } from "vue";

const KEY = "settings_plan_history_hours";

describe("plan history window setting", () => {
  beforeEach(() => {
    window.localStorage.clear();
    vi.resetModules();
  });

  test("defaults to 24 h and ignores values that are not an offered option", async () => {
    const { default: settings, parsePlanHistoryHours } = await import("./settings");
    expect(settings.planHistoryHours).toBe(24);
    for (const value of [undefined, null, "", "abc", "7", "-6", "96"])
      expect(parsePlanHistoryHours(value)).toBe(24);
    expect(parsePlanHistoryHours("0")).toBe(0);
    expect(parsePlanHistoryHours("48")).toBe(48);
  });

  test("a choice persists per viewer, Off included", async () => {
    window.localStorage.setItem(KEY, "6");
    const { default: settings } = await import("./settings");
    expect(settings.planHistoryHours).toBe(6);
    settings.planHistoryHours = 0;
    await nextTick();
    expect(window.localStorage.getItem(KEY)).toBe("0");
    vi.resetModules();
    const { default: reloaded } = await import("./settings");
    expect(reloaded.planHistoryHours).toBe(0);
  });
});
