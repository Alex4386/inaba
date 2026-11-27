import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { EventSchema, ThreadSchema, ThreadStatus } from "@/gen/inaba/v1/host_pb";
import { needsRead, withEvents } from "./thread-meta";

const ev = (seq: bigint, payload: MessageInitShape<typeof EventSchema>["payload"]) => create(EventSchema, { seq, payload });

describe("withEvents", () => {
  it("applies what events carry, only past the thread's lastSeq", () => {
    const thread = create(ThreadSchema, { id: "a", title: "first", lastSeq: 5n, status: ThreadStatus.IDLE });
    const next = withEvents(thread, [
      ev(4n, { case: "turnStarted", value: {} }),
      ev(6n, { case: "turnStarted", value: {} }),
      ev(7n, { case: "toolCall", value: { id: "t", title: "ls" } }),
      ev(8n, { case: "threadTitle", value: { title: "  named\nmore" } }),
      ev(9n, { case: "textDelta", value: { text: "hi" } }),
    ]);
    expect(next).toMatchObject({ status: ThreadStatus.RUNNING, activity: "ls", title: "named", lastSeq: 8n });
    expect(withEvents(next, [ev(10n, { case: "turnEnded", value: { error: "boom" } })])).toMatchObject({ status: ThreadStatus.IDLE, activity: "", lastTurnFailed: true, lastSeq: 10n });
  });

  it("keeps the same object when nothing changes, and a name someone gave", () => {
    const thread = create(ThreadSchema, { title: "mine", named: true, lastSeq: 1n });
    expect(withEvents(thread, [ev(2n, { case: "threadTitle", value: { title: "theirs" } }), ev(3n, { case: "textDelta", value: {} })])).toBe(thread);
  });
});

describe("needsRead", () => {
  it("asks for a read only for what events do not carry", () => {
    expect(needsRead([ev(1n, { case: "optionsChanged", value: {} }), ev(2n, { case: "toolCall", value: { id: "t" } })])).toBe(false);
    expect(needsRead([ev(1n, { case: "userMessage", value: { text: "hi" } })])).toBe(true);
    expect(needsRead([ev(1n, { case: "toolCall", value: { id: "t", agents: [{ id: "x" }] } })])).toBe(true);
  });
});
