import { create } from "@bufbuild/protobuf";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ProjectSchema } from "@/gen/inaba/v1/host_pb";

describe("host-cache", () => {
  beforeEach(() => {
    const store = new Map<string, string>();
    vi.stubGlobal("localStorage", { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => store.set(k, v) });
    vi.resetModules();
  });

  it("keeps a list across reloads, leaving out pictures too big to store", async () => {
    const { keep } = await import("./host-cache");
    const icon = `data:image/png;base64,${"A".repeat(40_000)}`;
    keep("h", "projects", ProjectSchema, [create(ProjectSchema, { id: "p", name: "one", icon })]);
    // A fresh page: only what was stored.
    vi.resetModules();
    const { cached } = await import("./host-cache");
    const [p] = cached("h", "projects", ProjectSchema) ?? [];
    expect(p).toMatchObject({ id: "p", name: "one", icon: "" });
  });

  it("keeps each account's entries apart, and forgets them all on sign-out", async () => {
    // Stored keys are the object's own, as Object.keys sees them on the browser's storage.
    const store: Record<string, string> = {};
    Object.defineProperties(store, {
      getItem: { value: (k: string) => store[k] ?? null },
      setItem: { value: (k: string, v: string) => (store[k] = v) },
      removeItem: { value: (k: string) => delete store[k] },
    });
    vi.stubGlobal("localStorage", store);
    const { cacheUser, cached, clearCache, keep } = await import("./host-cache");
    cacheUser("a");
    keep("h", "projects", ProjectSchema, [create(ProjectSchema, { id: "p" })]);
    store.other = "kept";
    cacheUser("b");
    expect(cached("h", "projects", ProjectSchema)).toBeUndefined();
    cacheUser("a");
    expect(cached("h", "projects", ProjectSchema)).toHaveLength(1);
    clearCache();
    expect(cached("h", "projects", ProjectSchema)).toBeUndefined();
    expect(Object.keys(store)).toEqual(["other"]);
  });
});
