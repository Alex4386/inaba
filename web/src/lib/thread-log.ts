// How a thread's events become what the thread view shows.
import { toJson } from "@bufbuild/protobuf";
import { ValueSchema } from "@bufbuild/protobuf/wkt";
import { type Attachment, type Event, type Plan_Entry, type PermissionRequest_Option, TextDelta_Kind, ThreadMode, type WorkflowAgent } from "@/gen/inaba/v1/host_pb";
import type { MessageKey } from "@/lib/i18n";

export type Item =
  | { type: "user"; text: string; at: number; seq: bigint; attachments: Attachment[] }
  | { type: "assistant"; text: string; at: number; seq: bigint }
  | { type: "thought" | "error"; text: string; at: number }
  // The agent's plan for the turn, as it last stood.
  | { type: "plan"; entries: Plan_Entry[] }
  // A line of the UI's own, kept as a message key so it follows the language setting.
  | { type: "note"; key: MessageKey; values?: Record<string, string> }
  // inaba's own message carrying on a turn the host's restart cut short, in place of a person's.
  | { type: "restarted"; at: number }
  // agents are those of a background workflow the call started, such as an ultracode run.
  | { type: "tool"; id: string; title: string; kind: string; status: string; input: string; output: string; agents: WorkflowAgent[] }
  | { type: "permission"; requestId: string; title: string; options: PermissionRequest_Option[]; answer?: string }
  | { type: "raw"; method: string; json: string }
  // What a turn changed in the folder, as the two snapshots around it.
  | { type: "changes"; before: string; after: string; files: number; additions: number; deletions: number }
  // Marks where a turn began and, once it is over, when it ended. An own turn is work the agent
  // did after its turn had ended, such as reporting a background task: the log has no turn for it.
  | { type: "turn"; startedAt: number; endedAt?: number; own?: boolean };

export const millis = (event: Event) => (event.time ? Number(event.time.seconds) * 1000 + event.time.nanos / 1e6 : Date.now());

/**
 * Gives work the agent does after its turn ended a turn of its own, so it does not fold the
 * ended turn's answer away among its steps.
 * shortcut: the own turn shows as done while the agent still works, as nothing says when it stops;
 * have the host mark these turns if the agent ever reports their end.
 */
function continueTurn(items: Item[], at: number) {
  const i = items.findLastIndex((it) => it.type === "turn");
  const turn = items[i] as Extract<Item, { type: "turn" }> | undefined;
  if (turn?.endedAt === undefined) return;
  if (turn.own) items[i] = { ...turn, endedAt: at };
  else items.push({ type: "turn", startedAt: at, endedAt: at, own: true });
}

export type Log = { items: Item[]; lastSeq: bigint };

/** Folds one event into the transcript. Events at or before lastSeq were already applied. */
export function apply(log: Log, event: Event): Log {
  if (event.seq <= log.lastSeq) return log;
  const items = [...log.items];
  const p = event.payload;
  switch (p.case) {
    case "userMessage":
      if (p.value.resume) items.push({ type: "restarted", at: millis(event) });
      else items.push({ type: "user", text: p.value.text, at: millis(event), seq: event.seq, attachments: p.value.attachments });
      break;
    case "rewound": {
      // Everything from the message rewound to onward is no longer part of the conversation.
      const i = items.findIndex((it) => it.type === "user" && it.seq === p.value.toSeq);
      if (i >= 0) items.length = i;
      // One note for rewinds in a row, and none when no conversation is left above it.
      while (items.at(-1)?.type === "note" && (items.at(-1) as { key: MessageKey }).key === "chat.rewound") items.pop();
      if (items.some((it) => it.type === "user")) items.push({ type: "note", key: "chat.rewound" });
      break;
    }
    case "providerChanged":
      items.push({ type: "note", key: "chat.providerChanged", values: { from: p.value.from, to: p.value.provider } });
      break;
    case "textDelta": {
      continueTurn(items, millis(event));
      const type = p.value.kind === TextDelta_Kind.THOUGHT ? "thought" : "assistant";
      const last = items.at(-1);
      // Consecutive chunks of the same kind are one message.
      if (last?.type === type) items[items.length - 1] = { ...last, text: last.text + p.value.text } as Item;
      else items.push(type === "assistant" ? { type, text: p.value.text, at: millis(event), seq: event.seq } : { type, text: p.value.text, at: millis(event) });
      break;
    }
    case "toolCall": {
      // Late updates to a tool from an ended turn stay with it; only a new tool continues.
      if (!items.some((it) => it.type === "tool" && it.id === p.value.id)) continueTurn(items, millis(event));
      const i = items.findIndex((it) => it.type === "tool" && it.id === p.value.id);
      const old = i >= 0 ? (items[i] as Extract<Item, { type: "tool" }>) : undefined;
      // Updates carry only what changed.
      const next = {
        type: "tool" as const,
        id: p.value.id,
        title: p.value.title || old?.title || "",
        kind: p.value.kind || old?.kind || "",
        status: p.value.status || old?.status || "",
        input: p.value.input || old?.input || "",
        output: p.value.output || old?.output || "",
        agents: p.value.agents.length ? p.value.agents : (old?.agents ?? []),
      };
      if (i >= 0) items[i] = next;
      else items.push(next);
      break;
    }
    case "permissionRequest":
      items.push({ type: "permission", requestId: p.value.requestId, title: p.value.title, options: p.value.options });
      break;
    case "permissionResolved": {
      const i = items.findIndex((it) => it.type === "permission" && it.requestId === p.value.requestId);
      if (i >= 0) items[i] = { ...(items[i] as Extract<Item, { type: "permission" }>), answer: p.value.optionId || "cancelled" };
      break;
    }
    case "turnStarted":
      items.push({ type: "turn", startedAt: millis(event) });
      break;
    case "turnEnded": {
      const i = items.findLastIndex((it) => it.type === "turn");
      if (i >= 0) items[i] = { ...(items[i] as Extract<Item, { type: "turn" }>), endedAt: millis(event) };
      if (p.value.error) items.push({ type: "error", text: p.value.error, at: millis(event) });
      // Cut short by the host's restart and going on: the next turn says so.
      else if (p.value.stopReason && p.value.stopReason !== "end_turn" && p.value.stopReason !== "host_restarted") items.push({ type: "note", key: "chat.turnEnded", values: { reason: p.value.stopReason } });
      break;
    }
    case "plan": {
      // A plan is sent whole each time it changes; the turn shows it once, as it stands.
      const turn = items.findLastIndex((it) => it.type === "user");
      const i = items.findLastIndex((it) => it.type === "plan");
      if (i > turn) items[i] = { type: "plan", entries: p.value.entries };
      else items.push({ type: "plan", entries: p.value.entries });
      break;
    }
    case "turnChanges":
      items.push({ type: "changes", before: p.value.before, after: p.value.after, files: p.value.files, additions: p.value.additions, deletions: p.value.deletions });
      break;
    case "modeChanged":
      items.push({ type: "note", key: p.value.mode === ThreadMode.TERMINAL ? "chat.toTerminal" : "chat.fromTerminal" });
      break;
    case "raw":
      items.push({ type: "raw", method: p.value.method, json: p.value.params ? JSON.stringify(toJson(ValueSchema, p.value.params), null, 2) : "" });
      break;
  }
  return { items, lastSeq: event.seq };
}

/** Folds events that arrived together, for one render for the lot rather than one each. */
export const applyAll = (log: Log, events: Event[]) => events.reduce(apply, log);

export type Turn = Extract<Item, { type: "turn" }>;
export type Block = Item | { turn: Turn; work: Item[] };

// A workflow's agents stay in view, as they go on after the turn that started them.
export const isWork = (item: Item) => item.type === "thought" || (item.type === "tool" && item.agents.length === 0) || item.type === "raw";

/**
 * Lays a transcript out for reading. Within each turn, the steps the agent took
 * (thinking, tool calls, and what it said along the way) become one foldable block,
 * and the turn's last message stands on its own as the answer. Questions to the
 * user, errors and notes always stay in view.
 */
export function blocks(items: Item[]): Block[] {
  const out: Block[] = [];
  for (let i = 0; i < items.length; i++) {
    const item = items[i];
    if (item.type !== "turn") {
      out.push(item);
      continue;
    }
    let end = i + 1;
    while (end < items.length && items[end].type !== "turn" && items[end].type !== "user") end++;
    const rest = items.slice(i + 1, end);
    const answer = item.endedAt === undefined ? -1 : rest.findLastIndex((it) => it.type === "assistant");
    const work = rest.filter((it, j) => j !== answer && (isWork(it) || it.type === "assistant"));
    if (work.some(isWork) || item.endedAt === undefined) out.push({ turn: item, work });
    else work.length = 0;
    // What is not folded stays in its original order.
    out.push(...rest.filter((it, j) => j === answer || !work.includes(it)));
    i = end - 1;
  }
  return out;
}
