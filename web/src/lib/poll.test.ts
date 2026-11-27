import { afterEach, expect, test, vi } from "vitest";
import { poll } from "@/lib/poll";

afterEach(() => (vi.useRealTimers(), vi.unstubAllGlobals()));

function page() {
  const listeners = new Set<() => void>();
  const doc = {
    hidden: false,
    addEventListener: (_: string, f: () => void) => listeners.add(f),
    removeEventListener: (_: string, f: () => void) => listeners.delete(f),
    show: (hidden: boolean) => ((doc.hidden = hidden), listeners.forEach((f) => f())),
  };
  vi.stubGlobal("document", doc);
  return doc;
}

test("poll waits for each call to settle, and rests while the page is hidden", async () => {
  vi.useFakeTimers();
  const doc = page();
  let calls = 0;
  let settle = () => {};
  const stop = poll(() => (calls++, new Promise<void>((done) => (settle = done))), 1_000);
  expect(calls).toBe(1);
  // A slow call: no second one goes out while it is still out.
  await vi.advanceTimersByTimeAsync(5_000);
  expect(calls).toBe(1);
  settle();
  await vi.advanceTimersByTimeAsync(1_000);
  expect(calls).toBe(2);
  settle();
  doc.show(true);
  await vi.advanceTimersByTimeAsync(5_000);
  expect(calls).toBe(2);
  doc.show(false);
  expect(calls).toBe(3);
  // Shown again with a call out: still one at a time.
  doc.show(true);
  doc.show(false);
  expect(calls).toBe(3);
  stop();
  settle();
  await vi.advanceTimersByTimeAsync(5_000);
  expect(calls).toBe(3);
});
