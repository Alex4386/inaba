/** What a dock tab shows. */
export type Surface = "changes" | "files" | "browser" | "shell";
/** A tab: its surface, and for a surface that opens many times, which one, as in shell-2. */
export type TabId = string;

/** The id a newly opened surface gets: the next number free among the open tabs. Terminals, file trees and pages
 * open as many times as wanted; the changes panel shows the thread's one diff scope, so it always gets the same id
 * and opening it again only moves and focuses the one already open. */
export function newTabId(surface: Surface, open: TabId[]): TabId {
  // A layout saved before there could be only one may still hold changes-2; reuse it so no second tab appears.
  if (surface === "changes") return open.find((tab) => tab.startsWith("changes-")) ?? "changes-1";
  let n = 1;
  while (open.includes(`${surface}-${n}`)) n++;
  return `${surface}-${n}`;
}

/** tabs with from moved to where to is. */
export function moveTab<T>(tabs: T[], from: T, to: T): T[] {
  const rest = tabs.filter((x) => x !== from);
  const at = rest.indexOf(to);
  // Moving right lands after the target, moving left before it, as the pointer passes it.
  rest.splice(tabs.indexOf(from) < tabs.indexOf(to) ? at + 1 : at, 0, from);
  return rest;
}

/** A dock: whether it shows, its tabs in the user's order, and the one in front. */
export type DockState = { open: boolean; tabs: TabId[]; active?: TabId };
/** Every dock by name. A tab lives in one dock at a time. */
export type Docks<D extends string = string> = Record<D, DockState>;

/** The dock a tab is in, if any. */
export function dockOf<D extends string>(docks: Docks<D>, tab: TabId): D | undefined {
  return (Object.keys(docks) as D[]).find((d) => docks[d].tabs.includes(tab));
}

/** Docks with a tab taken out of whichever holds it. */
export function withoutTab<D extends string>(docks: Docks<D>, tab: TabId): Docks<D> {
  const next = { ...docks };
  for (const d of Object.keys(docks) as D[]) {
    const dock = docks[d];
    if (dock.tabs.includes(tab)) next[d] = { ...dock, tabs: dock.tabs.filter((x) => x !== tab), active: dock.active === tab ? undefined : dock.active };
  }
  return next;
}

/** Docks with a tab placed in one dock, open and in front, and out of every other. */
export function placeTab<D extends string>(docks: Docks<D>, dock: NoInfer<D>, tab: TabId): Docks<D> {
  if (docks[dock].tabs.includes(tab)) return { ...docks, [dock]: { ...docks[dock], open: true, active: tab } };
  const next = withoutTab(docks, tab);
  return { ...next, [dock]: { open: true, tabs: [...next[dock].tabs, tab], active: tab } };
}

/** Every tab open in any dock. */
export const allTabs = (docks: Docks) => Object.values(docks).flatMap((d) => d.tabs);

/** Docks keeping only the first changes tab, for a layout saved before there could be only one. */
export function oneChanges<D extends string>(docks: Docks<D>): Docks<D> {
  const extra = allTabs(docks).filter((tab) => tab.startsWith("changes-")).slice(1);
  return extra.reduce((next, tab) => withoutTab(next, tab), docks);
}
