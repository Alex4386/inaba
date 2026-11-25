import { expect, test, vi } from "vitest";

// The API clients need a browser's location; these tests only do the time maths.
vi.mock("@/api", () => ({ notifications: {} }));
process.env.TZ = "America/New_York";
const { snoozeUntil } = await import("@/lib/snooze");

test("until tomorrow is the next calendar day at 09:00, on either side of midnight", () => {
  expect(snoozeUntil("tomorrow", new Date(2026, 0, 5, 23, 59))).toEqual(new Date(2026, 0, 6, 9, 0));
  expect(snoozeUntil("tomorrow", new Date(2026, 0, 6, 0, 1))).toEqual(new Date(2026, 0, 7, 9, 0));
  expect(snoozeUntil("tomorrow", new Date(2026, 0, 31, 23, 30))).toEqual(new Date(2026, 1, 1, 9, 0));
});

test("until tomorrow keeps 09:00 on the wall clock across a DST change", () => {
  // Clocks in New York go forward at 02:00 on 8 March 2026.
  const woke = snoozeUntil("tomorrow", new Date(2026, 2, 7, 23, 30))!;
  expect([woke.getDate(), woke.getHours(), woke.getMinutes()]).toEqual([8, 9, 0]);
  expect(woke.getTime() - new Date(2026, 2, 7, 23, 30).getTime()).toBe(8.5 * 3_600_000);
});

test("an hour is an hour, and forever has no end", () => {
  const now = new Date(2026, 5, 1, 12, 0);
  expect(snoozeUntil("hour", now)!.getTime() - now.getTime()).toBe(3_600_000);
  expect(snoozeUntil("forever", now)).toBeUndefined();
});
