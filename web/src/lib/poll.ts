/**
 * Calls refresh now, and again ms after each call settles rather than on a fixed beat, so on a
 * slow link calls do not pile up and an older answer cannot land after a newer one. None go out
 * while the page is hidden; showing it again calls at once. Returns what stops it.
 */
export function poll(refresh: () => Promise<unknown>, ms: number): () => void {
  let live = true;
  let busy = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const tick = () => {
    clearTimeout(timer);
    timer = undefined;
    if (!live || busy || document.hidden) return;
    busy = true;
    void refresh()
      .catch(() => {})
      .finally(() => {
        busy = false;
        if (live && !document.hidden) timer = setTimeout(tick, ms);
      });
  };
  const onVisibility = () => timer === undefined && tick();
  document.addEventListener("visibilitychange", onVisibility);
  tick();
  return () => {
    live = false;
    clearTimeout(timer);
    document.removeEventListener("visibilitychange", onVisibility);
  };
}
