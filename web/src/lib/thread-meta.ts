// A thread kept current from its own event stream: most events say all they change, so the
// thread need not be read again after each, which over a slow link kept it busy for a whole turn.
import { type Event, type Thread, ThreadStatus } from "@/gen/inaba/v1/host_pb";

// The host's titleFrom: a title is its first line, cut short.
const titleFrom = (text: string) => {
  const line = [...(text.trim().split("\n")[0] ?? "")];
  return line.length > 80 ? line.slice(0, 79).join("") + "…" : line.join("");
};

/** What an event changes on its thread, as the host's own bookkeeping does; undefined when it changes nothing kept here. */
function change(thread: Thread, { payload: p }: Event): Partial<Thread> | undefined {
  switch (p.case) {
    case "optionsChanged":
      return { options: p.value.options };
    case "commandsChanged":
      return { commands: p.value.commands };
    case "usageChanged":
      return { usage: p.value.usage };
    case "layoutChanged":
      return { layout: p.value.layout };
    case "modeChanged":
      return { mode: p.value.mode };
    case "providerChanged":
      return { provider: p.value.provider };
    case "threadTitle": {
      // Someone's own name for the thread stays.
      const title = titleFrom(p.value.title);
      return thread.named || !title ? undefined : { title };
    }
    case "turnStarted":
      return { status: ThreadStatus.RUNNING, activity: "", lastTurnFailed: false };
    case "turnEnded":
      return { status: ThreadStatus.IDLE, activity: "", lastTurnFailed: p.value.error !== "" };
    case "permissionRequest":
      return { status: ThreadStatus.WAITING };
    case "permissionResolved":
      return thread.status === ThreadStatus.WAITING ? { status: ThreadStatus.RUNNING } : undefined;
    case "toolCall":
      return p.value.title ? { activity: p.value.title } : undefined;
  }
}

/** The thread with the events after its lastSeq applied; the same object when none changes it. */
export function withEvents(thread: Thread, events: Event[]): Thread {
  let next = thread;
  for (const e of events) {
    if (e.seq <= next.lastSeq) continue;
    const c = change(next, e);
    if (c) next = { ...next, ...c, lastSeq: e.seq };
  }
  return next;
}

/** Whether the thread has to be read again for what events change but do not carry: the queue, background agents, a rewind. */
export function needsRead(events: Event[]): boolean {
  return events.some(({ payload: p }) => {
    switch (p.case) {
      case "userMessage":
      case "turnEnded":
      case "rewound":
      case "providerChanged":
        return true;
      case "toolCall":
        return p.value.agents.length > 0;
    }
    return false;
  });
}
