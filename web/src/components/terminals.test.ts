import { expect, test, vi } from "vitest";

// The API module reads the page's address as it loads.
vi.hoisted(() => Object.assign(globalThis, { location: { origin: "http://localhost" } }));
import type { HostLink } from "@/api";
import { terminalListing } from "@/components/terminals";

test("one listing per host, and an older answer landing late does not undo a newer one", async () => {
  const answers: ((r: { terminals: { id: string }[] }) => void)[] = [];
  const link = { hostTerminals: { listHostTerminals: () => new Promise((r) => answers.push(r)) } } as unknown as HostLink;
  const shared = terminalListing(link);
  expect(terminalListing(link)).toBe(shared);
  const heard = vi.fn();
  shared.subs.add(heard);
  const older = shared.load();
  await new Promise((r) => setTimeout(r, 2));
  const newer = shared.load();
  answers[1]({ terminals: [{ id: "new" }] });
  await newer;
  answers[0]({ terminals: [{ id: "old" }] });
  await older;
  expect(shared.now.terminals?.map((t) => t.id)).toEqual(["new"]);
  expect(heard).toHaveBeenCalledTimes(1);
});
