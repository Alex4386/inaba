import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { EventSchema, TextDelta_Kind } from "@/gen/inaba/v1/host_pb";
import { apply, applyAll, type Log } from "./thread-log";

const time = { seconds: 1_700_000_000n, nanos: 0 };
const events = [
  create(EventSchema, { seq: 1n, time, payload: { case: "userMessage", value: { text: "hi" } } }),
  create(EventSchema, { seq: 2n, time, payload: { case: "turnStarted", value: {} } }),
  create(EventSchema, { seq: 3n, time, payload: { case: "textDelta", value: { kind: TextDelta_Kind.THOUGHT, text: "hm" } } }),
  create(EventSchema, { seq: 4n, time, payload: { case: "toolCall", value: { id: "t", title: "ls", status: "pending" } } }),
  create(EventSchema, { seq: 5n, time, payload: { case: "toolCall", value: { id: "t", status: "completed", output: "a" } } }),
  create(EventSchema, { seq: 6n, time, payload: { case: "textDelta", value: { text: "he" } } }),
  create(EventSchema, { seq: 7n, time, payload: { case: "textDelta", value: { text: "llo" } } }),
  create(EventSchema, { seq: 8n, time, payload: { case: "turnEnded", value: { stopReason: "end_turn" } } }),
];
const empty: Log = { items: [], lastSeq: 0n };

describe("applyAll", () => {
  it("folds a batch as one event at a time would", () => {
    let one = empty;
    for (const e of events) one = apply(one, e);
    const batched = applyAll(applyAll(empty, events.slice(0, 3)), events.slice(3));
    expect(batched).toEqual(one);
  });

  it("skips what a resumed stream sends again", () => {
    const once = applyAll(empty, events);
    expect(applyAll(applyAll(empty, events.slice(0, 5)), events.slice(2))).toEqual(once);
  });
});
