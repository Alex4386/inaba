// Unsent text and sent prompts, kept per thread in this browser. Storage can be full,
// blocked or missing (private windows), so every access falls back to nothing.

const draftKey = (threadId: string) => `inaba.draft.${threadId}`;
const historyKey = (threadId: string) => `inaba.history.${threadId}`;

/** How many sent prompts a thread remembers for recall. */
export const historyCap = 50;
// localStorage holds about 5MB for the whole app, shared by every thread, so history stays small:
// pasted logs are not worth recalling, and a thread's history keeps to a total size.
export const historyEntryChars = 10_000;
export const historyTotalChars = 100_000;
// shortcut: drafts and history of deleted threads, and of a signed-out user, stay in storage; clear inaba.draft.* / inaba.history.* on thread delete and logout if that matters.

export function loadDraft(threadId: string): string {
  try {
    return localStorage.getItem(draftKey(threadId)) ?? "";
  } catch {
    return "";
  }
}

/** Keeps the thread's unsent text; empty text forgets it. */
export function saveDraft(threadId: string, text: string) {
  try {
    if (text) localStorage.setItem(draftKey(threadId), text);
    else localStorage.removeItem(draftKey(threadId));
  } catch {
    // Losing a draft to full storage is better than failing to type.
  }
}

/** The thread's sent prompts, newest first. */
export function loadHistory(threadId: string): string[] {
  try {
    const list: unknown = JSON.parse(localStorage.getItem(historyKey(threadId)) ?? "[]");
    return Array.isArray(list) ? list.filter((p): p is string => typeof p === "string") : [];
  } catch {
    return [];
  }
}

/**
 * The history with a newly sent prompt on top: a repeat of the newest is kept once, a prompt too long to
 * be worth storing is left out, and the oldest fall off past the count or the total size.
 */
export function withPrompt(history: string[], prompt: string): string[] {
  const list = (prompt.length > historyEntryChars ? history : [prompt, ...(history[0] === prompt ? history.slice(1) : history)]).slice(0, historyCap);
  let total = 0;
  const keep = list.findIndex((p) => (total += p.length) > historyTotalChars);
  return keep === -1 ? list : list.slice(0, keep);
}

export function pushHistory(threadId: string, prompt: string) {
  let list = withPrompt(loadHistory(threadId), prompt);
  // On a full quota, give up the oldest half until it fits, so recall keeps working with less.
  while (list.length > 0) {
    try {
      localStorage.setItem(historyKey(threadId), JSON.stringify(list));
      return;
    } catch {
      list = list.slice(0, Math.floor(list.length / 2));
    }
  }
  // Recall is a convenience; sending must not fail over it.
}

/**
 * What ↑ walks through, newest first: the prompts sent from this browser, then the thread's own user
 * messages (sent before this browser kept any, or from elsewhere), each once and within the cap.
 */
export function recallList(stored: string[], thread: string[]): string[] {
  return [...new Set([...stored, ...thread].filter((p) => p && p.length <= historyEntryChars))].slice(0, historyCap);
}

/**
 * One step through the history from the entry shown (undefined for the empty draft):
 * "older" goes back in time, "newer" forward and finally to the empty draft.
 * Undefined means there is nowhere to go, so the key should do its usual thing.
 */
export function recallStep(history: string[], index: number | undefined, direction: "older" | "newer"): { index?: number; text: string } | undefined {
  if (direction === "older") {
    const next = (index ?? -1) + 1;
    return next < history.length ? { index: next, text: history[next] } : undefined;
  }
  if (index === undefined) return undefined;
  return index === 0 ? { text: "" } : { index: index - 1, text: history[index - 1] };
}
