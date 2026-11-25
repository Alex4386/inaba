// inaba's service worker: it only shows push notifications and opens the thread one points at.
// Plain JavaScript in public/, so Vite copies it to /ui/sw.js unchanged.

self.addEventListener("push", (event) => {
  let m = {};
  try {
    m = event.data ? event.data.json() : {};
  } catch {
    // Still show one: a push that shows nothing counts against the site in Chrome.
  }
  event.waitUntil(
    self.registration.showNotification(m.title || "inaba", {
      body: m.body || "",
      // The thread and kind of event, so a newer note of the same kind replaces the older one,
      // with renotify so the replacement still sounds; a browser shows a same-tag one silently otherwise.
      tag: m.tag || undefined,
      renotify: Boolean(m.tag),
      // A question waits for its answer, so it stays on screen until handled.
      requireInteraction: Boolean(m.high),
      icon: "/ui/favicon.svg",
      data: { url: m.url || "/ui/" },
    }),
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const url = event.notification.data?.url || "/ui/";
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((tabs) => {
      // An open app tab moves to the thread itself, so its state and connection are kept; the
      // API reference at /ui/docs.html is no app tab.
      // shortcut: a tab still on the sign-in screen drops the move, as only the workspace listens; have it ack and fall back to openWindow if that bites.
      const tab = tabs.find((c) => {
        const path = new URL(c.url).pathname;
        return path.startsWith("/ui/") && !path.startsWith("/ui/docs");
      });
      if (!tab) return self.clients.openWindow(url);
      tab.postMessage({ navigate: url });
      return tab.focus();
    }),
  );
});

// The browser replaced the subscription; tell the server the new one. Not every browser fires
// this, so the app also registers its subscription again each time it loads.
// shortcut: the old endpoint's device row stays until a push to it answers gone; send the old endpoint along for the server to drop if stale rows confuse people.
self.addEventListener("pushsubscriptionchange", (event) => {
  const options = event.oldSubscription?.options ?? event.newSubscription?.options;
  // Without the old options there is no server key to subscribe with; the person turns push on again.
  if (!event.newSubscription && !options?.applicationServerKey) return;
  event.waitUntil(
    (event.newSubscription ? Promise.resolve(event.newSubscription) : self.registration.pushManager.subscribe(options)).then((sub) => {
      const json = sub.toJSON();
      return fetch("/inaba.v1.NotificationService/RegisterDevice", {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ platform: "PUSH_PLATFORM_WEB", endpoint: json.endpoint, p256dh: json.keys?.p256dh, auth: json.keys?.auth, label: "Browser" }),
      });
    }),
  );
});
