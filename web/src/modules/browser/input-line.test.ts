import { expect, it } from "vitest";
import { inputLine } from "./input-line";

type E = { type: string; d?: number };

it("joins moves and scrolls waiting in line, and keeps everything else in order", async () => {
  const sent: E[] = [];
  let release!: () => void;
  const first = new Promise<void>((r) => (release = r));
  const send = inputLine<E>(
    (e) => (sent.push(e), sent.length === 1 ? first : Promise.resolve()),
    (a, b) => (a.type !== b.type ? undefined : a.type === "wheel" ? { type: "wheel", d: (a.d ?? 0) + (b.d ?? 0) } : a.type === "moved" ? b : undefined),
    (e) => e.type === "moved" || e.type === "wheel",
  );
  send({ type: "key" }); // slow to answer
  send({ type: "moved", d: 1 });
  send({ type: "moved", d: 2 }); // replaces the one waiting
  send({ type: "pressed" });
  send({ type: "moved", d: 3 }); // after the press: its own turn
  send({ type: "wheel", d: 5 });
  send({ type: "wheel", d: 7 }); // adds up
  const done = send({ type: "released" });
  release();
  await done;
  expect(sent).toEqual([{ type: "key" }, { type: "moved", d: 2 }, { type: "pressed" }, { type: "moved", d: 3 }, { type: "wheel", d: 12 }, { type: "released" }]);
});
