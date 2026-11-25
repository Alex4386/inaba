import { useCallback, useEffect, useState } from "react";
import { notifications } from "@/api";
import { type PushDevice, PushPlatform } from "@/gen/inaba/v1/server_pb";

// Push notifications on this browser: the service worker in public/sw.js shows them, and the
// server keeps the subscription as one of the account's devices.

export type PushSupport = "ok" | "unsupported" | "denied" | "needs-install";

const ios = () => /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
const installed = () => matchMedia("(display-mode: standalone)").matches || (navigator as { standalone?: boolean }).standalone === true;

/** Whether this browser can get push notifications, and if not, why. */
export function pushSupport(): PushSupport {
  // iOS offers web push only to a site added to the Home Screen, and hides PushManager elsewhere.
  if (ios() && !installed()) return "needs-install";
  if (!window.isSecureContext || !("serviceWorker" in navigator) || !("PushManager" in window) || !("Notification" in window)) return "unsupported";
  return Notification.permission === "denied" ? "denied" : "ok";
}

/** What to call this device in the list: "Chrome on macOS". */
// shortcut: the label is stored in English, as is the service worker's "Browser"; store the browser and OS apart and word them in the UI when it matters.
export function deviceLabel(ua = navigator.userAgent): string {
  const browser = /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "Browser";
  const os = /iPhone|iPad|iPod/.test(ua) ? "iOS" : /Android/.test(ua) ? "Android" : /Mac OS X/.test(ua) ? "macOS" : /Windows/.test(ua) ? "Windows" : /Linux/.test(ua) ? "Linux" : "";
  return os ? `${browser} on ${os}` : browser;
}

// applicationServerKey as bytes: not every browser takes the base64url string.
function bytes(base64url: string): Uint8Array<ArrayBuffer> {
  const raw = atob(base64url.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(base64url.length / 4) * 4, "="));
  return Uint8Array.from(raw, (c) => c.charCodeAt(0));
}

const registration = () => navigator.serviceWorker.register("/ui/sw.js", { scope: "/ui/" });

// No support check: a subscription made before notifications were blocked still exists, and
// sign-out has to end it.
async function subscription(): Promise<PushSubscription | null> {
  if (!window.isSecureContext || !("serviceWorker" in navigator)) return null;
  const reg = await navigator.serviceWorker.getRegistration("/ui/");
  return (await reg?.pushManager.getSubscription()) ?? null;
}

// Which account this browser's subscription was last given to, and its endpoint, so a load
// only registers it again when either changed.
const SYNCED_KEY = "inaba.push";
const remember = (value: string) => {
  try {
    if (value) localStorage.setItem(SYNCED_KEY, value);
    else localStorage.removeItem(SYNCED_KEY);
  } catch {
    // Then the next load registers it again, which is harmless.
  }
};

async function register(sub: PushSubscription, userId: string) {
  const json = sub.toJSON();
  await notifications.registerDevice({ platform: PushPlatform.WEB, endpoint: sub.endpoint, p256dh: json.keys?.p256dh ?? "", auth: json.keys?.auth ?? "", label: deviceLabel() });
  remember(`${userId} ${sub.endpoint}`);
}

const sameKey = (a: ArrayBuffer | null | undefined, b: Uint8Array) => !!a && a.byteLength === b.length && new Uint8Array(a).every((x, i) => x === b[i]);

/**
 * Turns push on for this browser, and says whether it did: false when the person did not allow
 * notifications. Call it straight from a click: browsers ask for permission only then.
 */
export async function enablePush(userId: string): Promise<boolean> {
  if ((await Notification.requestPermission()) !== "granted") return false;
  const reg = await registration();
  await navigator.serviceWorker.ready;
  const key = bytes((await notifications.getPushConfig({})).vapidPublicKey);
  let sub = await reg.pushManager.getSubscription();
  // One made for another server key, as after the server's data was reset, gets nothing pushed: replace it.
  if (sub && !sameKey(sub.options.applicationServerKey, key)) {
    await sub.unsubscribe();
    sub = null;
  }
  sub ??= await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
  await register(sub, userId);
  return true;
}

/** Turns push off for this browser: the subscription ends and the server forgets the device. */
export async function disablePush(devices?: PushDevice[]) {
  const sub = await subscription();
  if (!sub) return;
  try {
    const listed = devices ?? (await notifications.listDevices({})).devices;
    const mine = listed.find((d) => d.endpoint === sub.endpoint);
    if (mine) await notifications.deleteDevice({ id: mine.id });
  } finally {
    // Even if the server could not be told: an ended subscription reaches no one, and its row
    // goes once the push service answers gone.
    await sub.unsubscribe();
    remember("");
  }
}

/**
 * Registers this browser's subscription again when its endpoint changed since this account
 * registered it, as pushsubscriptionchange does not reach every browser. A subscription another
 * known account left behind, say when its session ran out without a sign-out, is ended: the person
 * now here never turned push on, and must not see that account's notifications. A device
 * removed from another one stays removed.
 */
export async function syncPush(userId: string) {
  const sub = await subscription();
  if (!sub) return;
  let synced = "";
  try {
    synced = localStorage.getItem(SYNCED_KEY) ?? "";
  } catch {
    // Without storage there is no telling whose it is; it stays as it is.
    return;
  }
  const [owner, endpoint] = synced.split(" ");
  // No owner on record, as on the first load since this was added or after storage was cleared:
  // most likely it is this person's own, so it is kept and given to them rather than ended.
  if (owner && owner !== userId) {
    await sub.unsubscribe();
    remember("");
  } else if (owner !== userId || endpoint !== sub.endpoint) await register(sub, userId);
}

/**
 * Closes the notifications about a thread once it is on screen: a question stays up until
 * handled otherwise, and the person now sees the thread itself.
 */
export async function closeNotifications(threadId: string) {
  if (!threadId || !window.isSecureContext || !("serviceWorker" in navigator)) return;
  const reg = await navigator.serviceWorker.getRegistration("/ui/");
  // Tags are "<thread>/<event>", as the server makes them.
  for (const n of (await reg?.getNotifications()) ?? []) if (n.tag.startsWith(`${threadId}/`)) n.close();
}

/** The account's devices, this browser's endpoint among them when it is subscribed, and whether it is. */
export function usePushDevices() {
  const [devices, setDevices] = useState<PushDevice[]>();
  const [endpoint, setEndpoint] = useState("");
  const refresh = useCallback(
    () =>
      Promise.all([notifications.listDevices({}), subscription().catch(() => null)]).then(([r, sub]) => {
        setDevices(r.devices);
        setEndpoint(sub?.endpoint ?? "");
      }),
    [],
  );
  useEffect(() => {
    void refresh().catch(() => setDevices([]));
  }, [refresh]);
  return { devices, endpoint, enabled: endpoint !== "" && Boolean(devices?.some((d) => d.endpoint === endpoint)), refresh };
}
