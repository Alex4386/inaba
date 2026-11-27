// What was last read from each host, for a view to show at once while it reads it again: on a
// slow link the sidebar's lists, a thread's header and the providers are there before the host
// answers. Kept in memory and in localStorage, so a reload starts from them too.
import { fromJsonString, type JsonValue, type DescMessage, type MessageShape, toJson } from "@bufbuild/protobuf";

const memory = new Map<string, unknown>();
const KEY = "inaba.cache.";
const BIG = 16 << 10;
// Whose cache this is: what one account read from a host is not shown to the next one on this browser.
let user = "";

/** Sets the signed-in account every entry is kept under; the workspace sets it as it mounts. */
export function cacheUser(id: string) {
  user = id;
}

/** Forgets every account's entries, in memory and stored, as one signs out. */
export function clearCache() {
  memory.clear();
  try {
    for (const k of Object.keys(localStorage)) if (k.startsWith(KEY)) localStorage.removeItem(k);
  } catch {
    // Unavailable: nothing was stored.
  }
}

/** The value last kept under key for this host, if any. */
export function cached<D extends DescMessage>(hostId: string, key: string, schema: D): MessageShape<D>[] | undefined {
  const id = `${user}/${hostId}/${key}`;
  if (memory.has(id)) return memory.get(id) as MessageShape<D>[];
  try {
    const raw = localStorage.getItem(KEY + id);
    if (!raw) return undefined;
    const list = (JSON.parse(raw) as JsonValue[]).map((v) => fromJsonString(schema, JSON.stringify(v), { ignoreUnknownFields: true }));
    memory.set(id, list);
    return list;
  } catch {
    return undefined;
  }
}

/** Keeps a host's list under key; persist: false keeps it in memory only, for what is large or changes by the second. */
export function keep<D extends DescMessage>(hostId: string, key: string, schema: D, list: MessageShape<D>[], persist = true) {
  const id = `${user}/${hostId}/${key}`;
  memory.set(id, list);
  if (!persist) return;
  try {
    // A value this long is a picture, such as a project's icon: a few would fill the browser's
    // storage, so it is left out here and comes from the host again after a reload.
    const slim = (_: string, v: unknown) => (typeof v === "string" && v.length > BIG ? undefined : v);
    localStorage.setItem(KEY + id, JSON.stringify(list.map((m) => toJson(schema, m)), slim));
  } catch {
    // Full or unavailable: memory still has it.
  }
}
