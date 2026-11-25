import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { useEffect, useState, useSyncExternalStore } from "react";
import type { Thread } from "@/gen/inaba/v1/host_pb";
import { PresenceService } from "@/gen/inaba/v1/server_pb";

// What the user last saw of each thread, as the seq of the newest event on screen. The server keeps
// the marks per account; this module loads them once per host and keeps them current locally, so the
// sidebar and the open thread share one copy.

const presence = createClient(PresenceService, createConnectTransport({ baseUrl: location.origin }));

// Host id to thread id to seq. A host is missing until its marks have loaded.
const marks = new Map<string, Map<string, bigint>>();
const asked = new Set<string>();
const listeners = new Set<() => void>();
let version = 0;
const changed = () => {
  version++;
  for (const l of listeners) l();
};
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

function load(hostId: string) {
  if (asked.has(hostId)) return;
  asked.add(hostId);
  presence.listThreadSeen({ hostId }).then(
    (r) => {
      const m = marks.get(hostId) ?? new Map<string, bigint>();
      // A mark made here while the list was on its way is kept when it is newer.
      for (const s of r.threads) if (s.seq > (m.get(s.threadId) ?? 0n)) m.set(s.threadId, s.seq);
      marks.set(hostId, m);
      changed();
    },
    // Without the marks nothing shows as unread, rather than everything.
    () => {},
  );
}

/** Records that the user saw a thread up to seq; marks only move forward. */
export function markSeen(hostId: string, threadId: string, seq: bigint) {
  const m = marks.get(hostId);
  if (seq <= 0n || (m?.get(threadId) ?? 0n) >= seq) return;
  m?.set(threadId, seq);
  changed();
  presence.markThreadSeen({ hostId, threadId, seq }).then((s) => {
    const now = marks.get(hostId);
    if (now && s.seq > (now.get(threadId) ?? 0n)) {
      now.set(threadId, s.seq);
      changed();
    }
  }, () => {});
}

/** The unread state of a host's threads. */
export function useSeen(hostId: string) {
  useSyncExternalStore(subscribe, () => version);
  useEffect(() => load(hostId), [hostId]);
  return {
    /** A thread whose conversation moved past the user's mark; one never opened is unread once it has any.
     * Bookkeeping events (layout, options) move lastSeq only, so someone opening a thread leaves it read. */
    unread: (th: Thread) => {
      const m = marks.get(hostId);
      return m !== undefined && th.activitySeq > (m.get(th.id) ?? 0n);
    },
    // shortcut: kept in this tab only, since the server's marks only move forward; a reload reads it as seen again. Add a server reset when unread should last.
    markUnread: (th: Thread) => {
      const m = marks.get(hostId);
      if (!m || th.activitySeq <= 0n) return;
      m.set(th.id, th.activitySeq - 1n);
      changed();
    },
  };
}

/** Marks a thread seen up to seq while it is on screen and the page is visible, a moment after it settles. */
export function useMarkSeen(hostId: string, threadId: string, seq: bigint | undefined) {
  const [visible, setVisible] = useState(() => document.visibilityState === "visible");
  useEffect(() => {
    const on = () => setVisible(document.visibilityState === "visible");
    document.addEventListener("visibilitychange", on);
    return () => document.removeEventListener("visibilitychange", on);
  }, []);
  useEffect(() => {
    if (!visible || seq === undefined) return;
    // Debounced, so a stream of events sends one mark.
    const timer = setTimeout(() => markSeen(hostId, threadId, seq), 1_000);
    return () => clearTimeout(timer);
  }, [hostId, threadId, seq, visible]);
}
