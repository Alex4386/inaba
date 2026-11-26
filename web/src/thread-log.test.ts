import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { EventSchema, TextDelta_Kind } from "./gen/inaba/v1/host_pb";
import { apply, blocks } from "./lib/thread-log";

const ev = (seq: number, payload: object) => create(EventSchema, { seq: BigInt(seq), payload } as never);
const text = (seq: number, t: string) => ev(seq, { case: "textDelta", value: { kind: TextDelta_Kind.MESSAGE, text: t } });

describe("apply", () => {
  it("gives work after an ended turn a turn of its own, keeping the ended turn's answer in view", () => {
    const events = [
      ev(1, { case: "userMessage", value: { text: "review" } }),
      ev(2, { case: "turnStarted", value: {} }),
      ev(3, { case: "toolCall", value: { id: "a", title: "Workflow", status: "pending" } }),
      text(4, "started two agents"),
      ev(5, { case: "turnEnded", value: { stopReason: "end_turn" } }),
      // The workflow finished; the agent reports on its own.
      text(6, "checking"),
      ev(7, { case: "toolCall", value: { id: "b", title: "git status" } }),
      ev(8, { case: "toolCall", value: { id: "a", status: "completed" } }),
      text(9, "the results"),
    ];
    const log = events.reduce(apply, { items: [], lastSeq: 0n });
    const shown = blocks(log.items).filter((b) => "type" in b && b.type === "assistant").map((b) => (b as { text: string }).text);
    expect(shown).toEqual(["started two agents", "the results"]);
    expect(log.items.filter((it) => it.type === "turn")).toHaveLength(2);
  });
});
