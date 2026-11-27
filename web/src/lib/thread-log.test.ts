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

  it("folds a log read from partway as the whole would, once the rest is read in", () => {
    const rewound = create(EventSchema, { seq: 9n, time, payload: { case: "rewound", value: { toSeq: 1n } } });
    const all = [...events, rewound];
    // From inside the tool call: its update and a rewind above where it begins show nothing.
    expect(applyAll(empty, all.slice(4)).items.some((it) => it.type === "tool" || it.type === "user")).toBe(false);
    expect(applyAll(empty, all.slice(4, 8)).items.filter((it) => it.type === "assistant")).toHaveLength(1);
    // From the start, the same update is kept with its call.
    expect(applyAll(empty, events).items.find((it) => it.type === "tool")).toMatchObject({ status: "completed", output: "a" });
  });

  it("skips what a resumed stream sends again", () => {
    const once = applyAll(empty, events);
    expect(applyAll(applyAll(empty, events.slice(0, 5)), events.slice(2))).toEqual(once);
  });
});
