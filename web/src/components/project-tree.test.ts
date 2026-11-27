import { afterEach, expect, test, vi } from "vitest";

// The API module reads the page's address as it loads.
vi.hoisted(() => Object.assign(globalThis, { location: { origin: "http://localhost" } }));
import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { landed, newest } from "@/components/project-tree";
import { cached } from "@/lib/host-cache";
import { ThreadSchema } from "@/gen/inaba/v1/host_pb";

afterEach(() => vi.unstubAllGlobals());

test("landed keeps the list, and the one shown when nothing changed", () => {
  const writes: string[] = [];
  vi.stubGlobal("localStorage", { getItem: () => null, setItem: (k: string) => writes.push(k) });
  const list = [create(ThreadSchema, { id: "a", title: "one" })];
  const shown = landed("h", "threads", ThreadSchema, list)(undefined);
  expect(shown).toBe(list);
  expect(cached("h", "threads", ThreadSchema)).toBe(list);
  expect(writes).toHaveLength(1);
  // The same again, as a new answer: the shown list stays, and nothing is stored again.
  const again = [create(ThreadSchema, { id: "a", title: "one" })];
  expect(landed("h", "threads", ThreadSchema, again)(shown)).toBe(shown);
  expect(writes).toHaveLength(1);
  const renamed = [create(ThreadSchema, { id: "a", title: "two" })];
  expect(landed("h", "threads", ThreadSchema, renamed)(shown)).toBe(renamed);
  expect(writes).toHaveLength(2);
});

test("newest orders threads the same way whatever order the host sent them in", () => {
  const at = (id: string, ms: number) => create(ThreadSchema, { id, createdAt: timestampFromMs(ms) });
  const order = (ts: ReturnType<typeof at>[]) => newest(ts).map((t) => t.id);
  expect(order([at("b", 1000), at("c", 2000), at("a", 1000)])).toEqual(["c", "a", "b"]);
  expect(order([at("a", 1000), at("b", 1000), at("c", 2000)])).toEqual(["c", "a", "b"]);
});
