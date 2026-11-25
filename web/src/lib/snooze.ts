import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import { useCallback, useEffect, useState } from "react";
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

/** The account's snoozes on a host, with what mutes a thread or a project, and setting and clearing them. */
export function useSnoozes(hostId: string) {
  const [snoozes, setSnoozes] = useState<Snooze[]>([]);
  const load = useCallback(() => notifications.listSnoozes({ hostId }).then((r) => setSnoozes(r.snoozes), () => setSnoozes([])), [hostId]);
  useEffect(() => {
    void load();
  }, [load]);

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
      return notifications.setSnooze({ hostId, kind, targetId, until: until && timestampFromDate(until) }).then(load, showError);
    },
    clear: (kind: SnoozeKind, targetId: string) => notifications.clearSnooze({ hostId, kind, targetId }).then(load, showError),
  };
}

export type Snoozes = ReturnType<typeof useSnoozes>;
