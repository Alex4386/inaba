import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import { useEffect, useSyncExternalStore } from "react";
import { notifications } from "@/api";
import { type Snooze, SnoozeKind } from "@/gen/inaba/v1/server_pb";
import { showError } from "@/lib/errors";

// The account's snoozes: a snoozed thread or project stays quiet about finished turns, while
// questions, approvals and failures still notify. The server keeps them, per account.

export type SnoozePreset = "hour" | "tomorrow" | "week" | "forever";
export const snoozePresets: SnoozePreset[] = ["hour", "tomorrow", "week", "forever"];

/** When a preset wakes, from now, in local time; undefined for never. */
export function snoozeUntil(preset: SnoozePreset, now = new Date()): Date | undefined {
  switch (preset) {
    case "hour":
      return new Date(now.getTime() + 3_600_000);
    // The next calendar day at 09:00 on the wall clock, whatever the time now and across DST.
    case "tomorrow":
      return new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1, 9, 0);
    case "week":
      return new Date(now.getFullYear(), now.getMonth(), now.getDate() + 7, now.getHours(), now.getMinutes());
    case "forever":
      return undefined;
  }
}

/** A snooze that matches: when it wakes, or null for never. */
export type Muted = { until: Date | null };

const awake = (s: Snooze, now: number) => s.until !== undefined && timestampDate(s.until).getTime() <= now;

// One copy per host for every menu and list that shows them, so a snooze set in one shows in all.
const byHost = new Map<string, Snooze[]>();
const listeners = new Set<() => void>();
let version = 0;
const subscribe = (l: () => void) => (listeners.add(l), () => void listeners.delete(l));
// The newest read of each host; an older one answering late is dropped.
const reads = new Map<string, number>();
/** Reads a host's snoozes again, for every place that shows them. */
export const reloadSnoozes = (hostId: string) => {
  const n = (reads.get(hostId) ?? 0) + 1;
  reads.set(hostId, n);
  return notifications
    .listSnoozes({ hostId })
    .then((r) => r.snoozes, () => [] as Snooze[])
    .then((list) => {
      if (reads.get(hostId) !== n) return;
      byHost.set(hostId, list);
      version++;
      for (const l of listeners) l();
    });
};

/** The account's snoozes on a host, with what mutes a thread or a project, and setting and clearing them. */
export function useSnoozes(hostId: string) {
  useSyncExternalStore(subscribe, () => version);
  // Read once per host; a menu opening does not read again. Setting and clearing read again.
  useEffect(() => {
    if (!reads.has(hostId)) void reloadSnoozes(hostId);
  }, [hostId]);
  const snoozes = byHost.get(hostId) ?? [];
  const find = (kind: SnoozeKind, id: string): Muted | undefined => {
    const s = snoozes.find((x) => x.kind === kind && x.targetId === id && !awake(x, Date.now()));
    return s && { until: s.until ? timestampDate(s.until) : null };
  };
  return {
    /** The thread's own snooze. */
    thread: (id: string) => find(SnoozeKind.THREAD, id),
    project: (id: string) => find(SnoozeKind.PROJECT, id),
    /** Whether a thread is muted, by its own snooze or its project's. */
    muted: (threadId: string, projectId?: string) => find(SnoozeKind.THREAD, threadId) ?? (projectId ? find(SnoozeKind.PROJECT, projectId) : undefined),
    set: (kind: SnoozeKind, targetId: string, preset: SnoozePreset) => {
      const until = snoozeUntil(preset);
      return notifications.setSnooze({ hostId, kind, targetId, until: until && timestampFromDate(until) }).then(() => reloadSnoozes(hostId), showError);
    },
    clear: (kind: SnoozeKind, targetId: string) => notifications.clearSnooze({ hostId, kind, targetId }).then(() => reloadSnoozes(hostId), showError),
  };
}

export type Snoozes = ReturnType<typeof useSnoozes>;
