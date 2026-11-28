import {
  ArrowDownIcon,
  BotIcon,
  CheckIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  DownloadIcon,
  ExternalLinkIcon,
  QuoteIcon,
  CircleDashedIcon,
  CircleDotIcon,
  FolderTreeIcon,
  ListChecksIcon,
  SplitIcon,
  BrainIcon,
  FileDiffIcon,
  Undo2Icon,
  GitBranchIcon,
  XIcon,
  HandIcon,
  UsersIcon,
  ChevronRightIcon,
  CircleAlertIcon,
  CircleStopIcon,
  CopyIcon,
  FilePenLineIcon,
  FileTextIcon,
  GlobeIcon,
  PencilIcon,
  SearchIcon,
  ShieldQuestionIcon,
  MoreHorizontalIcon,
  PanelBottomIcon,
  PanelTopIcon,
  PanelRightIcon,
  PlusIcon,
  RotateCwIcon,
  SquareTerminalIcon,
  Trash2Icon,
  WrenchIcon,
  FileIcon,
} from "lucide-react";
import { timestampMs } from "@bufbuild/protobuf/wkt";
import { bytes } from "@/components/metrics";
import { useDefaultLayout } from "react-resizable-panels";
import { createContext, type FormEvent, memo, type ReactNode, type RefObject, useCallback, useContext, useEffect, useLayoutEffect, useMemo, useReducer, useRef, useState } from "react";
import { toast } from "sonner";
import { useMarkSeen } from "@/lib/seen";
import type { HostLink } from "./api";
import {
  type Attachment,
  type Event,
  type GitStatus,
  type Plan_Entry,
  type Project,
  type Provider,
  type Thread,
  type WorkflowStep,
  GitStatusSchema,
  ProjectSchema,
  ProviderSchema,
  ThreadMode,
  ThreadSchema,
  ThreadStatus,
} from "./gen/inaba/v1/host_pb";
import { equals } from "@bufbuild/protobuf";
import { cached, keep } from "@/lib/host-cache";
import { loadDraft, saveDraft } from "@/lib/drafts";
import { needsRead, withEvents } from "@/lib/thread-meta";
import { TerminalView } from "./Terminal";
import { ChangesPanel, LineCounts, type TurnScope } from "@/components/changes-panel";

import { WhereCard } from "@/components/where-card";
import { allTabs, dockOf, isBrowserTool, type Docks, type DockState, moveTab, newTabId, oneChanges, placeTab, type Surface, type TabId, withoutTab } from "@/lib/tabs";
import { CLIENT, PeopleStack, usePresent, WatchingBanner } from "@/components/presence";
import { type Host, PresenceService } from "./gen/inaba/v1/server_pb";
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { FilesPanel } from "@/components/files-panel";
import { BrowserPanel } from "@/modules/browser/BrowserPanel";
import { AttachmentImage, Composer, type Draft } from "@/components/composer";
import { ChatMap, type Exchange } from "@/components/chat-map";
import { QueueList } from "@/components/queue-list";
import { ConfirmDialog, RenameDialog } from "@/components/dialogs";
import { Logo } from "@/components/logo";
import { Markdown } from "@/components/markdown";
import { Badge } from "@/components/reui/badge";
import { ProjectIcon, threadFailed, threadName, threadState, threadWorking } from "@/components/project-tree";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuCheckboxItem, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuSub, DropdownMenuSubContent, DropdownMenuSubTrigger, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useIsMobile } from "@/hooks/use-mobile";
import { errorText, showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { bindingOf, formatBinding, matches, useKeybindings } from "@/lib/keybindings";
import { cn, pathNames } from "@/lib/utils";
import { matchOffsets, planMarkdown, quoteMarkdown } from "@/lib/thread-text";
import { applyAll, blocks, type Block, type Item, isWork, type Log, millis, type Turn } from "@/lib/thread-log";


export function ThreadView({
  link,
  host,
  threadId,
  onEnded,
  onOpen,
  onProject,
  modules,
  admin = false,
  stealth = false,
}: {
  link: HostLink;
  /** The host the thread runs on, for the info card. */
  host?: Host;
  threadId: string;
  onEnded: () => void;
  /** Shows another thread, such as a fork of this one. */
  onOpen: (threadId: string) => void;
  /** Shows the thread's project: every thread in it. */
  onProject?: (project: Project) => void;
  /** The host's modules that are on, whose parts the thread shows. */
  modules: Set<string>;
  /** An administrator may take the driver seat from anyone, and hand it to anyone. */
  admin?: boolean;
  /** An administrator in stealth mode: never takes a free seat by itself. */
  stealth?: boolean;
}) {
  const { t } = useI18n();
  const { people, clients, seat, take, release, give, refresh: askSeat } = usePresent();
  // The one browser whose size the thread's terminals and pages take, and whose hands are on them;
  // everyone else watches until they take over. Part of the shared layout, so a takeover reaches all at once.
  const [driver, setDriver] = useState<string>();
  // Not before the server has answered for this thread: a duplicated tab learns its own id only then.
  const driving = seat !== undefined && driver === CLIENT;
  // Not yet known to drive or watch: the server has not answered, or the seat is free and about to be taken by itself.
  // The composer takes typing meanwhile; what is sent waits for the answer.
  // The seat is read before the driver state follows it, so the server naming this browser counts as pending too.
  const pending = !driving && (seat === undefined || seat.driver === CLIENT || (!seat.driver && !driver && !stealth && clients?.length === 0));
  // The server settles who drives (so of two asking at once, the first wins), and the layout carries its
  // answer to everyone at once. When this browser last learned who drives, so an answer the server gave
  // before that does not undo it.
  const learned = useRef(0);
  const now$ = useRef({ driver, driving });
  now$.current = { driver, driving };
  // Takes in what the server settled after this browser asked something of the seat.
  const settled = (s: { driver: string } | undefined) => {
    if (!s) return s;
    learned.current = performance.now();
    setDriver(s.driver || undefined);
    return s;
  };
  const grab = (from: string) => take(from).then(settled);
  const askToTake = (from: string) => grab(from).then((s) => s && s.driver !== CLIENT && toast.error(t("drive.denied")));
  const [ending, setEnding] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const [git, setGit] = useState<GitStatus>();
  // The two docks beside the thread, each with its tabs in the user's order and the one shown.
  // A tab lives in one dock at a time.
  // The card saying where the thread runs, shown over the conversation's corner.
  const [info, setInfo] = useState(false);
  // The turns that changed files, and the one the changes panel shows; none shows what is not committed.
  const [turns, setTurns] = useState<TurnScope[]>([]);
  const [diffScope, setDiffScope] = useState<string>();
  const [docks, setDocks] = useState<Docks<Dock>>(() => Object.fromEntries(DOCKS.map((d) => [d, { open: false, tabs: [] }])) as unknown as Docks<Dock>);
  const updateDock = (dock: Dock, change: (d: DockState) => DockState) => setDocks((all) => ({ ...all, [dock]: change(all[dock]) }));
  // Files tabs with unsaved edits: closing, moving or hiding one unmounts it, so that asks first.
  const unsaved = useRef(new Set<TabId>());
  // The file each files tab shows, to name the tab by it.
  const [fileOf, setFileOf] = useState<Record<TabId, string | undefined>>({});
  // The title of the page each browser tab shows, as its tab's name.
  const [titleOf, setTitleOf] = useState<Record<TabId, string | undefined>>({});
  const [discarding, setDiscarding] = useState<() => void>();
  const guard = (tabs: TabId[], go: () => void) => {
    if (!tabs.some((x) => unsaved.current.has(x))) return go();
    setDiscarding(() => () => {
      for (const x of tabs) unsaved.current.delete(x);
      go();
    });
  };
  const toggleDock = (dock: Dock) => guard(docks[dock].open ? docks[dock].tabs : [], () => updateDock(dock, (d) => ({ ...d, open: !d.open })));
  // Opens a surface in a dock: another terminal, file tree or page each time, and the one diff moved there.
  // What this browser opens, it shows: on a phone, which keeps its docks hidden, too.
  const reveal = (dock: Dock) => setPeek((p) => (p[dock] ? p : { ...p, [dock]: true }));
  const openSurface = (dock: Dock, surface: Surface) => {
    reveal(dock);
    setDocks((all) => placeTab(all, dock, newTabId(surface, allTabs(all))));
  };
  // Takes a tab from whichever dock holds it into this one, keeping what it shows.
  const moveTo = (dock: Dock, tab: TabId) =>
    guard(dockOf(docks, tab) === dock ? [] : [tab], () => {
      reveal(dock);
      setDocks((all) => placeTab(all, dock, tab));
    });
  // A turn's changes show in the changes panel beside the conversation, opening it if the driver may.
  const showTurn = (turn: TurnScope) => {
    setDiffScope(turn.after);
    const tab = allTabs(now.current.docks).find((x) => surfaceOf(x) === "changes");
    if (tab) {
      const dock = dockOf(now.current.docks, tab);
      if (dock && now$.current.driving) {
        reveal(dock);
        updateDock(dock, (d) => ({ ...d, open: true, active: tab }));
      }
    } else if (now$.current.driving) openSurface("right", "changes");
  };
  // The tab being dragged, so closed docks show where it can be dropped.
  const [dragging, setDragging] = useState<TabId>();
  // A tab dropped into another dock leaves its old place before its drag ends, so that end may
  // never be heard; any drop, or an end heard anywhere, finishes the drag.
  useEffect(() => {
    if (!dragging) return;
    const done = () => setDragging(undefined);
    window.addEventListener("drop", done);
    window.addEventListener("dragend", done);
    return () => {
      window.removeEventListener("drop", done);
      window.removeEventListener("dragend", done);
    };
  }, [dragging]);
  // The page of the thread's browser each browser tab shows, shared with the layout.
  const [pageOf, setPageOf] = useState<Record<TabId, string>>({});
  // The docks and pages as they are now, for the browser's answers, which come between renders.
  const now = useRef({ docks, pageOf });
  now.current = { docks, pageOf };
  // Every page has a tab: one the agent opens gets a tab beside the browser tabs already open,
  // once no tab is still looking for a page of its own.
  const showPages = useCallback((pageIds: string[]) => {
    const { docks: all, pageOf: bound } = now.current;
    const browsers = allTabs(all).filter((tab) => surfaceOf(tab) === "browser");
    if (browsers.length === 0 || browsers.some((tab) => !bound[tab])) return;
    const shown = new Set(Object.values(bound));
    const free = pageIds.filter((id) => !shown.has(id));
    if (free.length === 0) return;
    const dock = DOCKS.find((d) => all[d].tabs.some((tab) => surfaceOf(tab) === "browser")) ?? DOCKS[0];
    let next = all;
    const ties: Record<TabId, string> = {};
    for (const id of free) {
      const tab = newTabId("browser", allTabs(next));
      ties[tab] = id;
      next = { ...next, [dock]: { ...next[dock], open: true, tabs: [...next[dock].tabs, tab] } };
    }
    // Kept here at once too, so the next answer before a render does not add them again.
    now.current = { docks: next, pageOf: { ...bound, ...ties } };
    setDocks(next);
    setPageOf((b) => ({ ...b, ...ties }));
  }, []);
  const closeTab = (tab: TabId) => guard([tab], () => {
    setDocks((all) => withoutTab(all, tab));
    // A browser tab's page closes with it.
    const page = pageOf[tab];
    if (page) {
      link.browser.navigate({ threadId, pageId: page, action: "close" }).catch(() => {});
      setPageOf(({ [tab]: _, ...rest }) => rest);
    }
    // A terminal's process ends with its tab.
    if (surfaceOf(tab) === "shell") link.shell.closeShell({ threadId, shellId: tab }).catch(() => {});
  });
  const isMobile = useIsMobile();
  // A phone has no room for every open dock: it starts with them hidden and shows one when asked,
  // opening it for everyone only when it drives and the dock is closed.
  const ownDocks = isMobile;
  const [peek, setPeek] = useState<Record<Dock, boolean>>({ right: false, bottom: false });
  const shown = (dock: Dock) => docks[dock].open && (!ownDocks || peek[dock]);
  const flipDock = (dock: Dock) => {
    if (!ownDocks) return toggleDock(dock);
    if (!docks[dock].open) toggleDock(dock);
    setPeek((p) => ({ ...p, [dock]: !docks[dock].open || !p[dock] }));
  };
  useEffect(() => {
    setPeek((p) => (DOCKS.some((d) => p[d] && !docks[d].open) ? { right: p.right && docks.right.open, bottom: p.bottom && docks.bottom.open } : p));
  }, [docks.right.open, docks.bottom.open]);
  // The sizes the user dragged the docks to, kept across threads and reloads.
  const rightLayout = useDefaultLayout({ id: "inaba-thread-side", panelIds: shown("right") ? ["main", "side"] : ["main"] });
  const bottomLayout = useDefaultLayout({ id: "inaba-thread-bottom", panelIds: shown("bottom") ? ["top", "bottom"] : ["top"] });
  // Every provider the host offers, for moving the thread to another between turns.
  const [providers, setProviders] = useState<Provider[]>(() => cached(link.hostId, "providers", ProviderSchema) ?? []);
  const [confirming, setConfirming] = useState(false);
  // Set while the thread moves between its chat and its terminal, which can take a while to start.
  const [switching, setSwitching] = useState(false);
  // The side chat shown beside the conversation, and whether /btw opened it as an aside.
  // shortcut: kept in this browser, not the shared layout, so watchers do not see it; add it to the layout if they should.
  const [side, setSide] = useState<{ id: string; aside: boolean }>();
  useEffect(() => setSide(undefined), [threadId]);
  // Counts terminal restarts, so the view attaches to the new process.
  const [terminalRun, setTerminalRun] = useState(0);
  const [exited, setExited] = useState(false);
  const direct = link.direct;

  // Why the thread could not be read, and how many reads in a row failed: one shown from the sidebar's copy is
  // read again until it is, as without that read it never takes the seat and held messages never go.
  const [loadError, setLoadError] = useState<string>();
  const [failures, setFailures] = useState(0);
  const { thread, fresh, take: setThread, refresh, onEvents } = useThread(link, threadId, (e) => {
    // One toast, not one per retry.
    if (!failures) showError(e);
    setLoadError(errorText(e));
    setFailures((n) => n + 1);
  });
  useEffect(() => {
    setLoadError(undefined);
    void refresh();
  }, [refresh]);
  useEffect(() => {
    if (!failures || fresh) return;
    const timer = setTimeout(() => void refresh(), Math.min(1500 * 2 ** (failures - 1), 30_000));
    return () => clearTimeout(timer);
  }, [failures, fresh, refresh]);
  const retry = () => (setLoadError(undefined), setFailures(0), void refresh());
  // Opening the thread marks it read; the conversation marks newer events as they arrive.
  useMarkSeen(link.hostId, threadId, thread?.lastSeq);
  // The thread's project, for the header's path and the file tree's first step: the sidebar's, read only when it has none.
  const projectId = thread?.projectId;
  const listed = projectId ? cached(link.hostId, "projects", ProjectSchema)?.find((p) => p.id === projectId) : undefined;
  const [fetched, setFetched] = useState<Project>();
  useEffect(() => {
    if (projectId && !listed) link.projects.listProjects({}).then((r) => setFetched(r.projects.find((p) => p.id === projectId)), () => setFetched(undefined));
  }, [link, projectId, Boolean(listed)]); // eslint-disable-line react-hooks/exhaustive-deps
  const project = listed ?? fetched;
  const projectName = project?.name;
  // Read beside the thread, not after it: the provider is found in the list by id once both are in.
  useEffect(() => {
    link.threads.listProviders({}).then((r) => {
      keep(link.hostId, "providers", ProviderSchema, r.providers);
      setProviders(r.providers);
    }, showError);
  }, [link]);
  const provider = providers.find((p) => p.id === thread?.provider);

  // What changed in the folder is worth another look whenever the thread comes to rest.
  const status = thread?.status;
  // A call made while the direct connection is still forming can fail once; one more try covers it.
  // A status like the one shown is kept as it is, so the changes panel does not read the whole diff again.
  const loadGit = useCallback(
    (known?: GitStatus) => {
      const set = (next?: GitStatus) => setGit((was) => (was && next && equals(GitStatusSchema, was, next) ? was : next));
      if (known) return set(known);
      const get = () => link.git.getStatus({ threadId });
      return get().then(set, () => new Promise((r) => setTimeout(r, 1500)).then(get).then(set, () => set(undefined)));
    },
    [link, threadId],
  );
  // Read on opening, beside the thread; after that, each time its status changes.
  useEffect(() => void loadGit(), [loadGit]);
  const gitFor = useRef(status);
  useEffect(() => {
    const was = gitFor.current;
    gitFor.current = status;
    if (was !== undefined && was !== status) void loadGit();
  }, [loadGit, status]);

  // The docks are the thread's, not this browser's: everyone in it sees the same screen.
  // The layout last sent or taken, so neither side echoes the other.
  const synced = useRef("");
  const layout = thread?.layout;
  useEffect(() => {
    if (!layout || layout === synced.current) return;
    // The sidebar's copy shows the docks at once; who drives is taken from the host's own.
    if (fresh) synced.current = layout;
    try {
      const { pages = {}, driver: d, ...saved } = JSON.parse(layout);
      // Only the known docks are taken, so a key this build does not know is ignored instead of dropping the layout.
      const shown = Object.fromEntries(DOCKS.map((dock) => [dock, Array.isArray(saved[dock]?.tabs) ? saved[dock] : { open: false, tabs: [] }])) as unknown as Docks<Dock>;
      // A layout saved when the changes panel could open twice keeps only its first; the driver sends the fix back.
      setDocks(oneChanges(shown));
      setPageOf(pages);
      if (!fresh) return;
      if (d !== now$.current.driver) {
        learned.current = performance.now();
        // The seat came free: ask at once, so a lone watcher takes it without waiting for the next beat.
        if (!d) askSeat();
      }
      setDriver(d);
    } catch {
      // A layout this version cannot read leaves the screen as it is.
    }
  }, [layout, fresh]);
  // Only the driver changes the screen everyone sees; a watcher follows it.
  const sendLayout = (d: string | undefined) => {
    const next = JSON.stringify({ ...docks, pages: pageOf, driver: d });
    synced.current = next;
    link.threads.setLayout({ threadId, layout: next }).catch(() => {});
  };
  useEffect(() => {
    const next = JSON.stringify({ ...docks, pages: pageOf, driver });
    // Not from what the sidebar last saw either: that layout may be old, or left out, and would undo everyone's.
    if (next === synced.current || !fresh || !driving) return;
    // Before the thread's own layout arrives, the empty one in hand is not a change to send.
    if (!synced.current && !driver && DOCKS.every((d) => !docks[d].open) && allTabs(docks).length === 0) return;
    const timer = setTimeout(() => {
      synced.current = next;
      link.threads.setLayout({ threadId, layout: next }).catch(() => {});
    }, 200);
    return () => clearTimeout(timer);
  }, [docks, pageOf, driver, driving, link, threadId, fresh]);
  // What the server says goes, unless this browser has heard newer since asking. A driver who left the
  // thread, closed the page or went quiet long enough for presence to forget them (half a minute) holds nothing.
  useEffect(() => {
    if (seat && seat.sentAt >= learned.current) setDriver(seat.driver || undefined);
  }, [seat]);
  // A free seat goes by itself only to a browser alone on the thread; with others watching, the first to ask gets it.
  // Only once the host has said who drove last, so its layout does not undo the seat just taken.
  const loaded = fresh;
  useEffect(() => {
    if (!stealth && loaded && seat && clients && seat.sentAt >= learned.current && !seat.driver && clients.length === 0) void grab("");
  }, [loaded, seat, clients, stealth]); // eslint-disable-line react-hooks/exhaustive-deps
  // Leaving the thread lets the seat go at once for everyone, not at their next beat.
  useEffect(
    () => () => {
      if (!now$.current.driving || !synced.current) return;
      try {
        const { driver: _, ...rest } = JSON.parse(synced.current);
        link.threads.setLayout({ threadId, layout: JSON.stringify(rest) }).catch(() => {});
      } catch {
        // Nothing readable to change; presence frees the seat soon enough.
      }
    },
    [link, threadId],
  );

  if (!thread)
    return loadError ? (
      <div className="m-auto grid justify-items-center gap-2 p-4 text-center">
        <p className="text-destructive text-sm">{loadError}</p>
        <Button size="sm" variant="outline" onClick={retry}>
          {t("common.retry")}
        </Button>
      </div>
    ) : (
      <Spinner className="text-muted-foreground m-auto" />
    );
  const terminal = thread.mode === ThreadMode.TERMINAL;
  const offered: Record<Surface, boolean> = { changes: Boolean(git?.repository), files: modules.has("files"), browser: modules.has("browser"), shell: modules.has("shell") };
  const pane = (tab: TabId) => {
    switch (surfaceOf(tab)) {
      case "changes":
        return <ChangesPanel
            link={link}
            threadId={threadId}
            status={git!}
            onStatus={loadGit}
            onClose={() => closeTab(tab)}
            readOnly={!driving}
            turns={turns}
            scope={diffScope}
            onScope={setDiffScope}
            restorable={thread.worktree && thread.status === ThreadStatus.IDLE}
            running={thread.status !== ThreadStatus.IDLE}
          />;
      case "files":
        return <FilesPanel link={link} threadId={threadId} root={fileRoot(thread, projectName)} onClose={() => closeTab(tab)} onDirty={(d) => void (d ? unsaved.current.add(tab) : unsaved.current.delete(tab))} onActive={(path) => setFileOf((f) => (f[tab] === path ? f : { ...f, [tab]: path }))} readOnly={!driving} />;
      case "browser":
        return (
          <BrowserPanel
            link={link}
            threadId={threadId}
            page={pageOf[tab]}
            taken={Object.entries(pageOf).flatMap(([t, id]) => (t === tab ? [] : [id]))}
            onBind={(id) => setPageOf((b) => ({ ...b, [tab]: id }))}
            onGone={() => {
              setDocks((all) => withoutTab(all, tab));
              setPageOf(({ [tab]: _, ...rest }) => rest);
            }}
            onPages={showPages}
            onTitle={(title) => setTitleOf((n) => (n[tab] === title ? n : { ...n, [tab]: title }))}
            driving={driving}
          />
        );
      case "shell":
        return <ShellPane link={link} threadId={threadId} shellId={tab} driving={driving} />;
    }
  };
  // A surface whose module was turned off, or a repository that went away, is not offered.
  const available = surfaces.filter((surface) => offered[surface]);
  const dockPanel = (dock: Dock) => {
    const d = docks[dock];
    const tabs = d.tabs.filter((tab) => offered[surfaceOf(tab)]);
    return (
      <SidePanel
        tabs={tabs}
        active={d.active && tabs.includes(d.active) ? d.active : tabs.at(-1)}
        available={driving ? available : []}
        onActive={(tab) => updateDock(dock, (x) => ({ ...x, active: tab }))}
        onOpen={(surface) => openSurface(dock, surface)}
        onClose={closeTab}
        onMove={(from, to) => updateDock(dock, (x) => ({ ...x, tabs: moveTab(x.tabs, from, to) }))}
        onHide={() => toggleDock(dock)}
        onAdopt={(tab) => moveTo(dock, tab)}
        targets={DOCKS.filter((d) => d !== dock).map((d) => ({ label: t(dockMoveLabel[d]), send: (tab: TabId) => moveTo(d, tab) }))}
        onDragging={setDragging}
        draggingTab={dragging}
        locked={!driving}
        fileOf={fileOf}
        titleOf={titleOf}
      >
        {pane}
      </SidePanel>
    );
  };
  // The header's branch button shows the changes, or hides the first changes tab when one is open.
  const changesTab = allTabs(docks).find((tab) => surfaceOf(tab) === "changes");
  const here = people.filter((p) => p.threadId === threadId);
  // Who holds the seat, when it is not this browser: someone else, or (no one else having it) oneself in another tab.
  const holder = driver && !driving ? here.find((p) => p.clients.includes(driver)) : undefined;
  // A free seat is anyone's; one's own other tab hands over; someone else's only an administrator takes.
  const mayTake = !driver || !holder || admin;
  const busy = thread.status !== ThreadStatus.IDLE;
  // An agent can move between its structured view and its own terminal; a plain terminal cannot.
  const canSwitch = provider?.structured && provider.terminal;
  const setMode = (mode: ThreadMode) => {
    if (switching) return;
    setSwitching(true);
    return link.threads
      .setMode({ threadId, mode })
      .then(() => {
        setExited(false);
        setTerminalRun((n) => n + 1);
      }, showError)
      .then(refresh)
      .finally(() => setSwitching(false));
  };

  // The agent's browser is seen once it uses it: its first browser tool opens a browser tab for the driver when none
  // is open, and that tab takes the agent's page. Only a call made just now, not one replayed as the thread opens,
  // and once a visit, so a tab closed on purpose stays closed.
  const browserShown = useRef(false);
  useEffect(() => {
    browserShown.current = false;
  }, [threadId]);
  const agentEvents = (events: Event[]) => {
    onEvents(events);
    if (browserShown.current || !now$.current.driving || !modules.has("browser")) return;
    if (allTabs(now.current.docks).some((tab) => surfaceOf(tab) === "browser")) return;
    const recent = Date.now() - 30_000;
    if (!events.some((e) => e.payload.case === "toolCall" && isBrowserTool(e.payload.value.title) && millis(e) > recent)) return;
    browserShown.current = true;
    openSurface("right", "browser");
  };
  const sideChat = side && <SideChat key={side.id} link={link} threadId={side.id} aside={side.aside} providers={providers} mentions={modules.has("files")} stealth={stealth} onOpen={onOpen} onSide={(id, aside) => setSide({ id, aside })} onClose={() => setSide(undefined)} />;
  const main = (
      <div className="@container relative flex h-full min-h-0 min-w-0 flex-1 flex-col">
        {/* While a tab is dragged, each closed dock offers a place on the conversation to drop it. */}
        {dragging &&
          DOCKS.filter((d) => !docks[d].open || (isMobile && d === "right")).map((d) => (
            <div
              key={d}
              className={cn("border-primary/60 bg-primary/10 text-primary motion-safe:animate-in motion-safe:fade-in-0 motion-safe:zoom-in-95 absolute z-20 flex items-center justify-center rounded-lg border-2 border-dashed text-xs font-medium", dropZone[d])}
              onDragOver={(e) => e.preventDefault()}
              onDrop={(e) => {
                e.preventDefault();
                moveTo(d, e.dataTransfer.getData(TAB_DRAG) || dragging);
                setDragging(undefined);
              }}
            >
              {t(dockMoveLabel[d])}
            </div>
          ))}
        {info && <WhereCard host={host} cwd={thread.cwd} git={git} connection={direct ? "direct" : "relayed"} provider={provider?.displayName || thread.provider} onChanges={git?.repository && driving ? () => openSurface("right", "changes") : undefined} className="absolute top-14 right-3 z-10" />}
        {/* Watching rather than driving: said over the conversation, with the way in when there is one. */}
        {seat && !driving && <WatchingBanner driver={driver} holder={holder} onTake={mayTake ? () => void askToTake(driver ?? "") : undefined} />}
        <header className="flex h-12 shrink-0 items-center gap-2 border-b px-3">
          <SidebarTrigger />
          {/* Where the thread sits, as the project's own pages show it: project / thread. */}
          {project && (
            <>
              <button type="button" className="text-muted-foreground hover:text-foreground flex min-w-0 shrink items-center gap-2 text-sm" onClick={() => onProject?.(project)}>
                <ProjectIcon project={project} />
                <span className="hidden truncate @md:inline">{project.name}</span>
              </button>
              <span className="text-muted-foreground">/</span>
            </>
          )}
          {threadWorking(thread) || switching ? (
            <Spinner className="text-muted-foreground shrink-0" />
          ) : threadFailed(thread) ? (
            <CircleAlertIcon className="text-destructive size-4 shrink-0" />
          ) : terminal ? (
            <SquareTerminalIcon className="text-muted-foreground size-4 shrink-0" />
          ) : (
            <BotIcon className="text-muted-foreground size-4 shrink-0" />
          )}
          <h1 className="truncate text-sm font-medium">{threadName(thread)}</h1>
          {/* Which agent and how this browser reaches the host are in the info card, not tags here. */}
          {/* Takes only the room the name leaves (no basis of its own), so it is cut before the name is. */}
          {(busy || threadWorking(thread) || threadFailed(thread)) && <span className={cn("hidden min-w-0 flex-1 basis-0 truncate text-xs @2xl:inline", threadFailed(thread) ? "text-destructive" : "text-muted-foreground")}>{threadState(thread, t)}</span>}
          <div className="ml-auto flex shrink-0 items-center gap-2">
            {/* Who else has this thread open. */}
            <PeopleStack people={here} label={driving ? t("drive.watchers") : undefined} />
            {git?.repository && (
              <Button
                size={isMobile ? "icon-sm" : "sm"}
                variant={changesTab ? "secondary" : "outline"}
                className="relative"
                title={t("git.changes")}
                aria-label={t("git.changes")}
                disabled={!driving}
                onClick={() => {
                  const dock = changesTab && dockOf(docks, changesTab);
                  // On a phone a hidden panel's tab is shown first, not closed unseen.
                  if (dock && !shown(dock)) {
                    reveal(dock);
                    updateDock(dock, (d) => ({ ...d, open: true, active: changesTab }));
                  } else if (changesTab) closeTab(changesTab);
                  else openSurface("right", "changes");
                }}
              >
                <GitBranchIcon data-icon={isMobile ? undefined : "inline-start"} />
                {/* On a phone only the icon, with a dot when there are changes; the panel it opens has the rest. */}
                {isMobile ? (
                  git.files.length > 0 && <span aria-hidden className="bg-primary absolute top-1 right-1 size-1.5 rounded-full" />
                ) : (
                  <>
                    <span className="hidden max-w-32 truncate @3xl:inline">{git.branch}</span>
                    {git.files.length > 0 && <LineCounts additions={git.additions} deletions={git.deletions} />}
                  </>
                )}
              </Button>
            )}
            {/* On a phone these are in the menu, as the header has no room for them. */}
            {!isMobile && (
              <Button size="icon-sm" variant={info ? "secondary" : "ghost"} aria-pressed={info} aria-label={t("thread.info")} title={t("thread.info")} onClick={() => setInfo((v) => !v)}>
                <PanelTopIcon />
              </Button>
            )}
            {!isMobile && (["bottom", "right"] as const).map((dock) => {
              const Icon = dock === "bottom" ? PanelBottomIcon : PanelRightIcon;
              return (
                <Button
                  key={dock}
                  size="icon-sm"
                  variant={shown(dock) ? "secondary" : "ghost"}
                  aria-pressed={shown(dock)}
                  aria-label={t(`dock.${dock}`)}
                  title={t(`dock.${dock}`)}
                  disabled={ownDocks ? !docks[dock].open : !driving}
                  onClick={() => (ownDocks ? setPeek((p) => ({ ...p, [dock]: !p[dock] })) : toggleDock(dock))}
                >
                  <Icon />
                </Button>
              );
            })}
            {terminal && exited && (
              <Button size="sm" variant="outline" disabled={!driving || switching} onClick={() => void setMode(ThreadMode.TERMINAL)}>
                <RotateCwIcon data-icon="inline-start" />
                <span className="hidden @xl:inline">{t("thread.restart")}</span>
              </Button>
            )}
            <DropdownMenu>
              <DropdownMenuTrigger render={<Button size="icon-sm" variant="ghost" />}>
                <MoreHorizontalIcon />
                <span className="sr-only">{t("project.more", { name: threadName(thread) })}</span>
              </DropdownMenuTrigger>
              <DropdownMenuContent className="w-auto whitespace-nowrap" align="end">
                {isMobile && (
                  <>
                    <DropdownMenuCheckboxItem checked={info} onCheckedChange={(on) => setInfo(on)}>
                      <PanelTopIcon />
                      {t("thread.info")}
                    </DropdownMenuCheckboxItem>
                    {(["bottom", "right"] as const).map((dock) => (
                      <DropdownMenuCheckboxItem
                        key={dock}
                        checked={shown(dock)}
                        disabled={!driving && !docks[dock].open}
                        onCheckedChange={() => flipDock(dock)}
                      >
                        {dock === "bottom" ? <PanelBottomIcon /> : <PanelRightIcon />}
                        {t(`dock.${dock}`)}
                      </DropdownMenuCheckboxItem>
                    ))}
                    <DropdownMenuSeparator />
                  </>
                )}
                <DropdownMenuItem disabled={!driving} onClick={() => setRenaming(true)}>
                  <PencilIcon />
                  {t("thread.rename")}
                </DropdownMenuItem>
                {/* The driver hands back control for someone watching to take. */}
                {driving && (clients?.length ?? 0) > 0 && (
                  <DropdownMenuItem onClick={() => void release().then((s) => s && (settled(s), sendLayout(undefined)))}>
                    <HandIcon />
                    {t("drive.release")}
                  </DropdownMenuItem>
                )}
                {admin && here.length > 0 && (
                  <DropdownMenuSub>
                    <DropdownMenuSubTrigger openOnHover={false}>
                      <UsersIcon />
                      {t("drive.giveTo")}
                    </DropdownMenuSubTrigger>
                    <DropdownMenuSubContent>
                      {here.map((p) => (
                        <DropdownMenuItem key={p.userId} disabled={p.clients.includes(driver ?? "")} onClick={() => void give(p.clients[0], driver ?? "").then((s) => s && (settled(s), sendLayout(s.driver || undefined)))}>
                          {p.name}
                        </DropdownMenuItem>
                      ))}
                    </DropdownMenuSubContent>
                  </DropdownMenuSub>
                )}
                {canSwitch && (
                  <DropdownMenuItem disabled={busy || !driving || switching} title={busy ? t("thread.busyHelp") : undefined} onClick={() => (terminal ? void setMode(ThreadMode.STRUCTURED) : setConfirming(true))}>
                    {terminal ? <BotIcon /> : <SquareTerminalIcon />}
                    {t(terminal ? "thread.backToChat" : "thread.openInTerminal")}
                  </DropdownMenuItem>
                )}
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" disabled={!driving} onClick={() => setEnding(true)}>
                  <Trash2Icon />
                  {t("thread.end")}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </header>
        {/* The sidebar's copy is shown, but the host keeps failing to say more. */}
        {!fresh && failures > 1 && loadError && (
          <div className="bg-destructive/10 flex items-center gap-2 border-b px-3 py-1.5 text-sm" role="alert">
            <p className="text-destructive min-w-0 flex-1 truncate">{loadError}</p>
            <Button size="sm" variant="outline" onClick={retry}>
              {t("common.retry")}
            </Button>
          </div>
        )}
        {terminal ? (
          <TerminalView key={terminalRun} link={link} threadId={threadId} driving={driving} onExit={() => setExited(true)} />
        ) : (
          // A wrapper that is always there, so opening the side chat never remounts the conversation.
          <div className="flex min-h-0 flex-1">
            <div className="flex min-w-0 flex-1 flex-col">
              <Chat link={link} thread={thread} busy={busy} refresh={refresh} onStatus={loadGit} onOpen={onOpen} mentions={modules.has("files")} provider={provider} providers={providers} locked={!driving} pending={pending} onEvents={agentEvents} onTurns={setTurns} onTurnChanges={showTurn} onSide={(id, aside) => setSide({ id, aside })} />
            </div>
            {sideChat && !isMobile && <div className="flex w-[min(32rem,45%)] shrink-0 flex-col border-l">{sideChat}</div>}
          </div>
        )}
        <Sheet open={Boolean(side) && isMobile} onOpenChange={(open) => !open && setSide(undefined)}>
          <SheetContent side="right" className="w-full gap-0 p-0 sm:max-w-md" showCloseButton={false}>
            <SheetTitle className="sr-only">{t(side?.aside ? "side.aside" : "side.title")}</SheetTitle>
            {isMobile && sideChat}
          </SheetContent>
        </Sheet>
        <ConfirmDialog
          open={confirming}
          onClose={() => setConfirming(false)}
          title={t("thread.switchTitle")}
          description={t("thread.switchDescription")}
          cancel={t("thread.stayInChat")}
          confirm={t("thread.openInTerminal")}
          onConfirm={() => setMode(ThreadMode.TERMINAL)}
        />
        <RenameDialog
          open={renaming}
          onClose={() => setRenaming(false)}
          title={t("thread.renameTitle")}
          name={threadName(thread)}
          onRename={(title) => link.threads.updateThread({ threadId, title }).then(setThread)}
        />
        <ConfirmDialog
          open={discarding !== undefined}
          onClose={() => setDiscarding(undefined)}
          title={t("files.discard")}
          description={t("files.discardHelp")}
          cancel={t("files.keepEditing")}
          confirm={t("files.discardConfirm")}
          destructive
          onConfirm={() => {
            discarding?.();
            setDiscarding(undefined);
          }}
        />
        <ConfirmDialog
          open={ending}
          onClose={() => setEnding(false)}
          title={t("thread.endTitle", { name: threadName(thread) })}
          description={t("thread.endDescription")}
          cancel={t("project.keep")}
          confirm={t("thread.end")}
          destructive
          onConfirm={() => link.threads.deleteThread({ threadId }).then(onEnded, showError)}
        />
      </div>
  );
  // The same tree whichever docks are open, so the conversation never remounts and loses its draft.
  // On a narrow screen the right dock is a sheet over the thread instead.
  const right = shown("right") && !isMobile;
  return (
    <ResizablePanelGroup id="inaba-thread-side" orientation="horizontal" className="min-h-0 flex-1" defaultLayout={rightLayout.defaultLayout} onLayoutChanged={rightLayout.onLayoutChanged}>
      <ResizablePanel id="main" minSize={360}>
        <ResizablePanelGroup id="inaba-thread-bottom" orientation="vertical" defaultLayout={bottomLayout.defaultLayout} onLayoutChanged={bottomLayout.onLayoutChanged}>
          <ResizablePanel id="top" minSize={200}>
            {main}
          </ResizablePanel>
          {shown("bottom") && (
            <>
              <ResizableHandle withHandle />
              <ResizablePanel id="bottom" defaultSize="35" minSize={120}>
                {dockPanel("bottom")}
              </ResizablePanel>
            </>
          )}
        </ResizablePanelGroup>
      </ResizablePanel>
      {right && (
        <>
          <ResizableHandle withHandle />
          <ResizablePanel id="side" defaultSize="40" minSize={280}>
            {dockPanel("right")}
          </ResizablePanel>
        </>
      )}
      <Sheet open={shown("right") && isMobile} onOpenChange={(open) => !open && (ownDocks ? setPeek((p) => ({ ...p, right: false })) : toggleDock("right"))}>
        <SheetContent side="right" className="w-full gap-0 p-0 sm:max-w-md" showCloseButton={false}>
          <SheetTitle className="sr-only">{t("dock.right")}</SheetTitle>
          {isMobile && dockPanel("right")}
        </SheetContent>
      </Sheet>
    </ResizablePanelGroup>
  );
}

// The docks beside the conversation; each has its place in the layout, and a tab can move between any of them.
const DOCKS = ["right", "bottom"] as const;
// Events read at a time going back; a thread opens at its last OPEN, back to where that turn began, so the newest turn shows sooner.
const PAGE = 1000;
const OPEN = 300;

/**
 * Keys for the conversation's blocks that stay with them as older turns are read in above, so
 * one does not take over another's state (a step log left open). One without a mark of its own
 * is keyed after the last block before it that has one.
 */
/**
 * Slides a strip's tabs ([data-tab]) from where they were to where a new order puts them, and fades in one
 * that joins it, so a reorder or a tab moved between docks is seen to happen. Nothing moves for a reader who
 * asked for less motion.
 */
function useSlide(strip: RefObject<HTMLElement | null>, order: string) {
  const was = useRef(new Map<string, number>());
  useLayoutEffect(() => {
    const el = strip.current;
    if (!el) return;
    const still = matchMedia("(prefers-reduced-motion: reduce)").matches;
    const now = new Map<string, number>();
    for (const tab of el.querySelectorAll<HTMLElement>("[data-tab]")) {
      // offsetLeft, unlike a bounding box, is where it is laid out, not where a running slide shows it.
      const x = tab.offsetLeft;
      const before = was.current.get(tab.dataset.tab!);
      now.set(tab.dataset.tab!, x);
      if (still) continue;
      if (before === undefined) {
        // Not on the strip's first drawing: only a tab that arrives later.
        if (was.current.size) tab.animate([{ opacity: 0, transform: "scale(0.9)" }, { opacity: 1, transform: "none" }], { duration: 160, easing: "ease-out" });
      } else if (before !== x) tab.animate([{ transform: `translateX(${before - x}px)` }, { transform: "none" }], { duration: 160, easing: "ease-out" });
    }
    was.current = now;
  }, [strip, order]);
}

/** What the chat map says of a message of the user's: its text, or what it attached. */
const exchangeText = (it: Extract<Item, { type: "user" }>) => it.text || it.attachments.map((a) => a.name || a.path).join(", ");

function keyed(list: Block[]): [string, Block][] {
  let mark = "top";
  let n = 0;
  const used = new Set<string>();
  return list.map((b) => {
    const own = "work" in b ? `s${b.turn.seq}` : "seq" in b ? `s${b.seq}` : b.type === "tool" ? `t${b.id}` : b.type === "permission" ? `p${b.requestId}` : undefined;
    // A provider reusing an id is no reason to break the list.
    if (own && !used.has(own)) return used.add(own), (mark = own), (n = 0), [own, b];
    return [`${mark}+${++n}`, b];
  });
}
type Dock = (typeof DOCKS)[number];
const dockMoveLabel = { right: "dock.toRight", bottom: "dock.toBottom" } as const;
// Where a closed dock offers to take a tab dragged over the conversation.
const dropZone: Record<Dock, string> = { right: "inset-y-0 right-0 w-28", bottom: "inset-x-0 bottom-0 h-24" };
const surfaceOf = (tab: TabId) => tab.split("-")[0] as Surface;
// The file tree's first step: the project's name, with the worktree's own folder after it when the thread has one;
// a thread outside any project, its folder's name.
const fileRoot = (thread: Thread, project?: string) => {
  const folder = pathNames(thread.cwd).at(-1) ?? "";
  return !project ? folder : thread.worktree ? `${project} (${folder})` : project;
};
const tabNumber = (tab: TabId) => tab.split("-")[1];
const sideTabTitle = { changes: "git.changes", files: "files.title", browser: "browser.title", shell: "shell.title" } as const;
const sideTabIcon = { changes: GitBranchIcon, files: FolderTreeIcon, browser: GlobeIcon, shell: SquareTerminalIcon };
// Every surface a dock can show, in the order offered; the "dock.<surface>" shortcut opens it from an empty dock.
const surfaces: Surface[] = ["browser", "shell", "files", "changes"];
// What a dragged tab carries, so the other dock can take it.
const TAB_DRAG = "application/x-inaba-tab";

/** A terminal in the thread's folder, one of as many as the user opens; it can start again once its shell exits. */
function ShellPane({ link, threadId, shellId, driving }: { link: HostLink; threadId: string; shellId: string; driving: boolean }) {
  const { t } = useI18n();
  // Counts restarts, so the view attaches to the new process.
  const [run, setRun] = useState(0);
  const [exited, setExited] = useState(false);
  return (
    <section className="relative flex h-full min-h-0 flex-col" aria-label={t("shell.title")}>
      <TerminalView key={run} link={link} threadId={threadId} shell={shellId} driving={driving} onExit={() => setExited(true)} />
      {exited && (
        <Button
          className="absolute top-2 right-2"
          size="sm"
          variant="secondary"
          disabled={!driving}
          onClick={() => {
            setExited(false);
            setRun((n) => n + 1);
          }}
        >
          <RotateCwIcon data-icon="inline-start" />
          {t("shell.restart")}
        </Button>
      )}
    </section>
  );
}


/** A dock's tabs, in an order the user drags. Each stays mounted once opened, so a terminal or page keeps its state while another tab is shown. Empty, it offers the surfaces to open. */
function SidePanel({
  tabs,
  active,
  available,
  onActive,
  onOpen,
  onClose,
  onMove,
  onHide,
  onAdopt,
  targets,
  onDragging,
  draggingTab,
  locked,
  fileOf,
  titleOf,
  children,
}: {
  tabs: TabId[];
  active?: TabId;
  available: Surface[];
  onActive: (tab: TabId) => void;
  onOpen: (surface: Surface) => void;
  onClose: (tab: TabId) => void;
  onMove: (from: TabId, to: TabId) => void;
  onHide: () => void;
  /** Takes in a tab dragged here from another dock. */
  onAdopt: (tab: TabId) => void;
  /** The other docks a tab's right-click menu can send it to. */
  targets: { label: string; send: (tab: TabId) => void }[];
  /** Says which of this dock's tabs is being dragged, and when the drag ends. */
  onDragging: (tab: TabId | undefined) => void;
  /** The tab being dragged, from any dock. */
  draggingTab?: TabId;
  /** Watching someone else drive: the dock shows what they show, and nothing here changes it. */
  locked: boolean;
  /** The file a files tab shows, to name it by. */
  fileOf: Record<TabId, string | undefined>;
  /** The page title of each browser tab. */
  titleOf: Record<TabId, string | undefined>;
  children: (tab: TabId) => ReactNode;
}) {
  const { t } = useI18n();
  useKeybindings();
  // A dragged tab of this dock's own is reordered; one from another dock is taken in.
  const own = draggingTab !== undefined && tabs.includes(draggingTab);
  // The tab whose right-click menu is open.
  const [menuFor, setMenuFor] = useState<TabId>();
  const strip = useRef<HTMLDivElement>(null);
  useSlide(strip, tabs.join("\n"));
  return (
    <Tabs
      value={active ?? null}
      onValueChange={(v) => !locked && onActive(v as TabId)}
      className="bg-background h-full min-h-0 gap-0"
      // A tab dragged from another dock lands anywhere on this one; one of this dock's own is reordered by its neighbours.
      onDragOver={(e) => {
        if (!own && e.dataTransfer.types.includes(TAB_DRAG)) e.preventDefault();
      }}
      onDrop={(e) => {
        const tab = e.dataTransfer.getData(TAB_DRAG);
        if (tab && !tabs.includes(tab)) {
          e.preventDefault();
          onAdopt(tab);
        }
      }}
    >
      {/* As tall as the thread's header, so the two lines meet. */}
      <div className="flex h-12 shrink-0 items-center gap-1 border-b px-2">
        <DropdownMenu>
          <DropdownMenuTrigger disabled={locked} render={<Button size="icon-sm" variant="ghost" aria-label={t("dock.add")} title={t("dock.add")} />}>
            <PlusIcon />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            {available.map((tab) => {
              const Icon = sideTabIcon[tab];
              return (
                <DropdownMenuItem key={tab} onClick={() => onOpen(tab)}>
                  <Icon />
                  {t(sideTabTitle[tab])}
                </DropdownMenuItem>
              );
            })}
          </DropdownMenuContent>
        </DropdownMenu>
        <TabsList
          ref={strip}
          variant="line"
          className="min-w-0 flex-1 justify-start gap-1 overflow-x-auto overflow-y-hidden group-data-horizontal/tabs:h-12"
        >
          {tabs.map((tab) => {
            const Icon = sideTabIcon[surfaceOf(tab)];
            return (
              <TabsTrigger
                key={tab}
                value={tab}
                data-tab={tab}
                // Where the dragged tab will land, held open while it is carried.
                className={cn("flex-none transition-opacity", draggingTab === tab && "opacity-40")}
                draggable={!locked}
                onDragStart={(e) => {
                  e.dataTransfer.effectAllowed = "move";
                  e.dataTransfer.setData(TAB_DRAG, tab);
                  onDragging(tab);
                }}
                onContextMenu={(e) => {
                  e.preventDefault();
                  if (!locked) setMenuFor(tab);
                }}
                onDragOver={(e) => {
                  const from = own ? draggingTab : undefined;
                  if (!from) return;
                  e.preventDefault();
                  // Not while it slides out of the way: it would pass back under the pointer and swap again.
                  if (from !== tab && !e.currentTarget.getAnimations().length) onMove(from, tab);
                }}
                onDragEnd={() => onDragging(undefined)}
              >
                <Icon />
                {/* Its right-click menu, hung from the tab itself. */}
                <DropdownMenu open={menuFor === tab} onOpenChange={(open) => !open && setMenuFor(undefined)}>
                  <DropdownMenuTrigger render={<span aria-hidden className="pointer-events-none absolute inset-0" />} />
                  <DropdownMenuContent align="start" className="w-auto whitespace-nowrap">
                    {targets.map((target) => (
                      <DropdownMenuItem key={target.label} onClick={() => target.send(tab)}>
                        {target.label}
                      </DropdownMenuItem>
                    ))}
                    <DropdownMenuItem onClick={() => onClose(tab)}>{t("palette.close")}</DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
                {fileOf[tab] ? (
                  <span className="max-w-40 truncate" title={fileOf[tab]}>
                    {fileOf[tab]!.split("/").at(-1)}
                  </span>
                ) : titleOf[tab] ? (
                  <span className="max-w-40 truncate" title={titleOf[tab]}>
                    {titleOf[tab]}
                  </span>
                ) : (
                  <>
                    {t(sideTabTitle[surfaceOf(tab)])}
                    {tabNumber(tab) && tabNumber(tab) !== "1" && <span className="text-muted-foreground tabular-nums">{tabNumber(tab)}</span>}
                  </>
                )}
                {!locked && <span
                  role="button"
                  tabIndex={-1}
                  aria-label={t("palette.close")}
                  className="hover:bg-muted rounded-sm opacity-60 hover:opacity-100"
                  onClick={(e) => {
                    e.stopPropagation();
                    onClose(tab);
                  }}
                >
                  <XIcon className="size-3.5" />
                </span>}
              </TabsTrigger>
            );
          })}
        </TabsList>
        <Button size="icon-sm" variant="ghost" disabled={locked} aria-label={t("dock.hide")} title={t("dock.hide")} onClick={onHide}>
          <XIcon />
        </Button>
      </div>
      {tabs.length === 0 && (
        <div
          className="flex min-h-0 flex-1 items-center justify-center outline-none"
          tabIndex={0}
          autoFocus
          onKeyDown={(e) => {
            const tab = available.find((x) => matches(e.nativeEvent, `dock.${x}`));
            if (tab) {
              e.preventDefault();
              onOpen(tab);
            }
          }}
        >
          <div className="grid w-56 gap-1">
            <p className="mb-2 text-center text-sm font-medium">{t("dock.openSurface")}</p>
            {available.map((tab) => {
              const Icon = sideTabIcon[tab];
              return (
                <Button key={tab} variant="ghost" className="justify-start" onClick={() => onOpen(tab)}>
                  <Icon data-icon="inline-start" />
                  {t(sideTabTitle[tab])}
                  <kbd className="text-muted-foreground bg-muted ml-auto rounded px-1.5 font-sans text-xs">{formatBinding(bindingOf(`dock.${tab}`))}</kbd>
                </Button>
              );
            })}
          </div>
        </div>
      )}
      {tabs.map((tab) => (
        <TabsContent key={tab} value={tab} keepMounted className="min-h-0 flex-1 data-[hidden]:hidden">
          {children(tab)}
        </TabsContent>
      ))}
    </Tabs>
  );
}

const presenceRpc = createClient(PresenceService, createConnectTransport({ baseUrl: location.origin }));

/**
 * Whether the side chat holds the driver seat of its own thread. It is there under a client id of its own, so
 * presence sees the pane like any browser on the fork: whoever else opens the fork finds it there and watches.
 */
function useSideSeat(hostId: string, threadId: string, stealth: boolean) {
  // Undefined until presence first answers.
  const [driving, setDriving] = useState<boolean>();
  useEffect(() => {
    let live = true;
    // Read at each beat: a duplicated tab may take a new CLIENT just after start.
    const client = () => `${CLIENT}.side`;
    const beat = (ask: { take?: boolean; release?: boolean } = {}) =>
      presenceRpc.heartbeat({ clientId: client(), hostId, threadId, projectId: "", visible: document.visibilityState === "visible", ...ask });
    const tick = () =>
      beat()
        .then(async (r) => {
          // A free seat goes by itself only to a pane alone on the thread, the same rule ThreadView follows.
          if (!r.driver && r.clients.length === 0 && !stealth) r = await beat({ take: true });
          if (live) setDriving(r.driver === client());
        })
        .catch(() => live && setDriving(false));
    void tick();
    const timer = setInterval(() => void tick(), 10_000);
    return () => {
      live = false;
      clearInterval(timer);
      // Lets the seat go at once, so someone alone on the fork takes it without waiting for presence to forget the pane.
      beat({ release: true }).catch(() => {});
    };
  }, [hostId, threadId, stealth]);
  return driving;
}

/**
 * A thread as the host last said, first as the sidebar last saw it, kept current by the events its conversation
 * streams: most say all they change, and the rest ask for one read a second later rather than one each.
 */
function useThread(link: HostLink, threadId: string, failed: (e: unknown) => void) {
  const [thread, setThread] = useState(() => cached(link.hostId, "threads", ThreadSchema)?.find((t) => t.id === threadId));
  // Whether the host has been read, not only the sidebar's copy.
  const [fresh, setFresh] = useState(false);
  const now = useRef(thread);
  now.current = thread;
  const failed$ = useRef(failed);
  failed$.current = failed;
  // The newest read, so an older one that lands late is dropped, and the events applied since, put back on a read
  // that left the host before they happened.
  const read = useRef(0n);
  const since = useRef<Event[]>([]);
  const take = useCallback((got: Thread) => {
    if (got.lastSeq < read.current) return;
    read.current = got.lastSeq;
    since.current = since.current.filter((e) => e.seq > got.lastSeq);
    setThread(withEvents(got, since.current));
    setFresh(true);
  }, []);
  const reading = useRef(0);
  const refresh = useCallback(() => {
    reading.current++;
    return link.threads
      .getThread({ threadId })
      .then(take, (e) => failed$.current(e))
      .finally(() => reading.current--);
  }, [link, threadId, take]);
  // The newest event that asked for a read, and the timer that makes it: one read at a time, a second after.
  const wanted = useRef(0n);
  const soon = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(soon.current), []);
  const onEvents = useCallback(
    (events: Event[]) => {
      // What the thread in hand already counts is history replayed, not news.
      const after = now.current?.lastSeq ?? 0n;
      const fresh = events.filter((e) => e.seq > after && e.seq > read.current);
      if (!fresh.length) return;
      since.current.push(...fresh);
      setThread((t) => t && withEvents(t, fresh));
      if (!needsRead(fresh)) return;
      wanted.current = fresh.at(-1)!.seq;
      const go = () => {
        soon.current = undefined;
        if (wanted.current <= read.current) return;
        // A read still on its way may have left before the event; look again once it is back.
        if (reading.current) soon.current = setTimeout(go, 1_000);
        else void refresh();
      };
      soon.current ??= setTimeout(go, 1_000);
    },
    [refresh],
  );
  return { thread, fresh, take, refresh, onEvents };
}

/** A side chat (a fork of the thread made by /side or /btw), shown beside the conversation it came from. */
function SideChat({
  link,
  threadId,
  aside,
  providers,
  mentions,
  stealth,
  onOpen,
  onSide,
  onClose,
}: {
  link: HostLink;
  threadId: string;
  aside: boolean;
  providers: Provider[];
  mentions: boolean;
  stealth: boolean;
  onOpen: (threadId: string) => void;
  onSide: (threadId: string, aside: boolean) => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  // The fork's own seat, not the parent's: a watcher there cannot act through this pane.
  const seated = useSideSeat(link.hostId, threadId, stealth);
  // A side chat deleted elsewhere closes instead of showing an error forever.
  const { thread, refresh, onEvents } = useThread(link, threadId, (e) => (showError(e), onClose()));
  useEffect(() => {
    void refresh();
  }, [refresh]);
  const label = t(aside ? "side.aside" : "side.title");
  return (
    <section aria-label={label} className="flex h-full min-h-0 flex-col">
      <header className="flex h-10 shrink-0 items-center gap-2 border-b px-3">
        <SplitIcon className="text-muted-foreground size-4 shrink-0" />
        <span className="text-sm font-medium">{label}</span>
        {thread && <span className="text-muted-foreground min-w-0 truncate text-xs">{threadName(thread)}</span>}
        <div className="ml-auto flex shrink-0 items-center gap-1">
          <Button size="icon-sm" variant="ghost" aria-label={t("side.openThread")} title={t("side.openThread")} onClick={() => onOpen(threadId)}>
            <ExternalLinkIcon />
          </Button>
          <Button size="icon-sm" variant="ghost" aria-label={t("side.close")} title={t("side.close")} onClick={onClose}>
            <XIcon />
          </Button>
        </div>
      </header>
      {thread ? (
        <Chat
          link={link}
          thread={thread}
          busy={thread.status !== ThreadStatus.IDLE}
          refresh={refresh}
          onStatus={() => {}}
          onOpen={onOpen}
          mentions={mentions}
          provider={providers.find((p) => p.id === thread.provider)}
          providers={providers}
          locked={!seated}
          pending={seated === undefined}
          onEvents={onEvents}
          onSide={onSide}
        />
      ) : (
        <Spinner className="text-muted-foreground m-auto" />
      )}
    </section>
  );
}

function Chat({
  link,
  thread,
  busy,
  refresh,
  onStatus,
  onOpen,
  mentions,
  provider,
  providers,
  locked,
  pending = false,
  onEvents,
  onTurns,
  onTurnChanges,
  onSide,
}: {
  providers: Provider[];
  link: HostLink;
  thread: Thread;
  busy: boolean;
  refresh: () => Promise<void>;
  onStatus: (known?: GitStatus) => void;
  onOpen: (threadId: string) => void;
  mentions: boolean;
  provider?: Provider;
  /** The turns that changed files, oldest first, whenever they change. */
  onTurns?: (turns: TurnScope[]) => void;
  /** Shows one turn's changes beside the conversation. */
  onTurnChanges?: (turn: TurnScope) => void;
  /** Shows a side chat (a fork started by /side or /btw) beside the conversation. */
  onSide: (threadId: string, aside: boolean) => void;
  /** Watching someone else drive: the conversation is read, and only the driver answers, sends or rewinds. */
  locked: boolean;
  /** Not yet known whether this browser drives: the composer takes typing, and what is sent waits to be known. */
  pending?: boolean;
  /** Every batch of events as it is applied, for the thread to take what they say about it. */
  onEvents?: (events: Event[]) => void;
}) {
  const { t } = useI18n();
  const threadId = thread.id;
  const names = useMemo(() => Object.fromEntries(providers.map((p) => [p.id, p.displayName || p.id])), [providers]);
  // The message the user asked to rewind to, while they confirm it.
  const [rewinding, setRewinding] = useState<Extract<Item, { type: "user" }>>();
  // What a rewind hands back to the composer.
  const [draft, setDraft] = useState<Draft>();
  // New events fold onto the log; older ones, read back on scrolling up, rebuild it from all of them.
  const [log, dispatch] = useReducer((log: Log, a: Event[] | { all: Event[] }) => (Array.isArray(a) ? applyAll(log, a) : applyAll({ items: [], lastSeq: 0n }, a.all)), { items: [], lastSeq: 0n });
  // Every event folded so far, oldest first, to fold again with older ones in front.
  const folded = useRef<Event[]>([]);
  // The first event read: a thread opens at its last turns, and 1 means nothing is left above.
  const [first, setFirst] = useState<bigint>();
  const firstSeq = useRef<bigint>(undefined);
  // The user's messages over the whole thread, for the map to show all of the conversation and not only what is read in.
  const [whole, setWhole] = useState<Item[]>([]);
  const [rewinds, setRewinds] = useState(0);
  useEffect(() => {
    let live = true;
    link.threads.listEvents({ threadId, messages: true }).then(
      ({ events }) => live && setWhole(applyAll({ items: [], lastSeq: 0n }, events).items),
      // Without it the map shows what is read in.
      () => {},
    );
    return () => {
      live = false;
    };
  }, [link, threadId, rewinds]);
  // Messages on their way to the host, shown at once rather than when the host has them: one
  // with a screenshot can take a while over the relay.
  const [sending, setSending] = useState<{ key: number; text: string; attachments: Attachment[] }[]>([]);
  const sendKey = useRef(0);
  // Messages sent before the seat is known, waiting for it.
  const [held, setHeld] = useState<{ key: number; text: string; attachments: Attachment[]; interrupt: boolean }[]>([]);
  // The turns that changed files, for the changes panel to offer; only a change in them is news.
  const turnKey = log.items.filter((it) => it.type === "changes").map((it) => (it as TurnScope).after).join();
  useEffect(() => {
    onTurns?.(log.items.filter((it): it is Extract<Item, { type: "changes" }> => it.type === "changes"));
  }, [turnKey]); // eslint-disable-line react-hooks/exhaustive-deps
  const lastSeq = useRef(0n);
  // How far the log went when the thread was opened: the replay up to there is drawn a few times a second, not per event.
  // A thread first shown as the sidebar last saw it learns how far it really goes when it is read.
  const opened = useRef(thread.lastSeq);
  if (thread.lastSeq > opened.current && lastSeq.current < thread.lastSeq) opened.current = thread.lastSeq;
  // How much of that replay has arrived, for the loading screen.
  const [received, setReceived] = useState(0n);
  useMarkSeen(link.hostId, threadId, log.lastSeq);
  const readWorkflowAgent = useCallback(
    (toolCallId: string, agentId: string, signal: AbortSignal) => link.threads.watchWorkflowAgent({ threadId, toolCallId, agentId }, { signal }),
    [link, threadId],
  );
  const bottom = useRef<HTMLDivElement>(null);
  const events$ = useRef(onEvents);
  events$.current = onEvents;

  // Follow the event log, resuming after the last event seen whenever the stream breaks. The link moving
  // to the direct connection is no reason to start again: the stream keeps going where it began.
  useEffect(() => {
    const abort = new AbortController();
    // Events are applied a frame's worth at a time: one render each made a replay freeze the tab.
    // A timer rather than a frame callback, so a hidden tab still applies them and marks the thread read.
    let buffer: Event[] = [];
    let timer: ReturnType<typeof setTimeout> | undefined;
    let progress: ReturnType<typeof setTimeout> | undefined;
    const flush = () => {
      timer = undefined;
      const events = buffer;
      buffer = [];
      // A rewind can take back messages not read in yet: the map reads them again.
      if (events.some((e) => e.payload.case === "rewound")) setRewinds((n) => n + 1);
      folded.current.push(...events);
      dispatch(events);
      events$.current?.(events);
    };
    void (async () => {
      while (!abort.signal.aborted) {
        try {
          for await (const event of link.threads.subscribe({ threadId, afterSeq: lastSeq.current, tail: OPEN }, { signal: abort.signal })) {
            if (firstSeq.current === undefined) setFirst((firstSeq.current = event.seq));
            lastSeq.current = event.seq;
            buffer.push(event);
            if (event.seq < opened.current) {
              progress ??= setTimeout(() => ((progress = undefined), setReceived(lastSeq.current)), 100);
              // Drawn as it comes over a slow link, a few times a second, so the conversation is there before all of it is.
              timer ??= setTimeout(flush, 250);
              continue;
            }
            if (event.seq === opened.current) {
              clearTimeout(timer);
              flush();
            } else timer ??= setTimeout(flush, 16);
          }
        } catch {
          // Retried below.
        }
        await new Promise((r) => setTimeout(r, 1_000));
      }
    })();
    return () => {
      abort.abort();
      clearTimeout(timer);
      clearTimeout(progress);
      // lastSeq is already past these, so the next subscription will not send them again.
      if (buffer.length) flush();
    };
  }, [link, threadId]);

  // Older turns, read in when the reader nears the top, keeping what they were looking at in place.
  // One read at a time; after one fails, none for a while, so scrolling does not repeat the error.
  const reading = useRef(false);
  const anchor = useRef<{ height: number; top: number }>(undefined);
  // to: read back as far as this event at least, as the map asks to jump to a message above what is read.
  const loadOlder = (to?: bigint): Promise<void> => {
    // Found text is counted from the top: what comes in above would move it.
    // Not while the last turns are still arriving: that read is the one the reader waits for.
    if (reading.current || find !== undefined || firstSeq.current === undefined || firstSeq.current <= 1n || lastSeq.current < opened.current) return Promise.resolve();
    reading.current = true;
    const limit = to === undefined ? PAGE : Math.max(PAGE, Number(firstSeq.current - to));
    return link.threads.listEvents({ threadId, beforeSeq: firstSeq.current, limit }).then(
      ({ events }) => {
        reading.current = false;
        const el = scroller.current;
        if (el && events.length) anchor.current = { height: el.scrollHeight, top: el.scrollTop };
        folded.current = [...events, ...folded.current];
        setFirst((firstSeq.current = events[0]?.seq ?? 1n));
        if (events.length) dispatch({ all: folded.current });
      },
      (e) => {
        showError(e);
        setTimeout(() => (reading.current = false), 5_000);
      },
    );
  };
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const was = anchor.current;
    anchor.current = undefined;
    if (was) el.scrollTop = was.top + el.scrollHeight - was.height;
    // Too little to scroll yet: read further back until there is.
    if (log.lastSeq > 0n && el.scrollHeight <= el.clientHeight) loadOlder();
  }, [log, first]); // eslint-disable-line react-hooks/exhaustive-deps

  // Follow new events only while the user is at the bottom; scrolled up, they are reading, so offer a jump instead.
  const near = useRef(true);
  const [behind, setBehind] = useState(false);
  // Older turns read in above are not news.
  const followed = useRef(0n);
  useEffect(() => {
    const grew = log.lastSeq !== followed.current;
    followed.current = log.lastSeq;
    if (near.current) bottom.current?.scrollIntoView({ block: "end" });
    else if (grew) setBehind(true);
  }, [log, sending, held]);

  // A message shows as sending until the host has it; if it never gets there, it goes back to the composer.
  // One sent before the seat is known waits for it: it goes once this browser drives, and back to the composer if it watches.
  useEffect(() => {
    if (pending || !held.length) return;
    setHeld([]);
    if (!locked) for (const m of held) send(m.text, m.attachments, m.interrupt);
    else setDraft({ text: held.map((m) => m.text).join("\n\n"), images: held.flatMap((m) => m.attachments.filter((a) => a.data.length > 0)), restore: true });
  }, [pending, locked, held]); // eslint-disable-line react-hooks/exhaustive-deps
  // Leaving before the seat is known keeps what waited for it as the thread's draft, ahead of what the composer
  // saves as it goes, which is why it waits a moment. shortcut: held images are lost, as drafts keep only text.
  const held$ = useRef(held);
  held$.current = held;
  useEffect(
    () => () => {
      const text = held$.current.map((m) => m.text).join("\n\n");
      if (text) queueMicrotask(() => saveDraft(threadId, [text, loadDraft(threadId)].filter(Boolean).join("\n\n")));
    },
    [threadId],
  );
  const submit = (text: string, attachments: Attachment[], interrupt: boolean) => {
    if (!pending) return send(text, attachments, interrupt);
    near.current = true;
    setHeld((now) => [...now, { key: ++sendKey.current, text, attachments, interrupt }]);
  };
  const send = (text: string, attachments: Attachment[], interrupt: boolean) => {
    const key = ++sendKey.current;
    near.current = true;
    setSending((now) => [...now, { key, text, attachments }]);
    void link.threads
      .startTurn({ threadId, text, attachments, interrupt })
      .then(refresh, (e) => {
        showError(e);
        setDraft({ text, images: attachments.filter((a) => a.data.length > 0), restore: true });
      })
      .finally(() => setSending((now) => now.filter((m) => m.key !== key)));
  };

  const chat = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  // What grows after it is drawn (an image fetched, code highlighted, a diagram) keeps a reader at the bottom there.
  useEffect(() => {
    const el = content.current;
    if (!el) return;
    const keep = new ResizeObserver(() => {
      if (near.current) bottom.current?.scrollIntoView({ block: "end" });
    });
    keep.observe(el);
    return () => keep.disconnect();
  }, []);
  const composer = useRef<HTMLFieldSetElement>(null);
  const findBox = useRef<HTMLInputElement>(null);
  // Cmd/Ctrl+F belongs to the conversation only while the pointer or focus is in it; elsewhere the browser's own find runs.
  const hovered = useRef(false);
  // The find bar's text, or undefined while it is closed.
  const [find, setFind] = useState<string>();
  const [hit, setHit] = useState(0);
  const [hits, setHits] = useState(0);
  // Bumped whenever the user moves to a match, so only that, not new events, scrolls the view.
  const [step, setStep] = useState(0);
  const scrolledStep = useRef(-1);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || !matches(e, "find")) return;
      // Hover counts only when nothing else holds focus, so another panel or a dialog keeps its own Cmd+F.
      const focus = document.activeElement;
      const inChat = !!focus && !!chat.current?.contains(focus) && !focus.closest("[role=dialog]");
      if (!inChat && !(hovered.current && (!focus || focus === document.body))) return;
      e.preventDefault();
      setFind((q) => q ?? "");
      findBox.current?.select();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  useEffect(() => {
    const root = content.current;
    const query = find?.toLowerCase();
    const found: Range[] = [];
    if (root && query) {
      const walk = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
      for (let node = walk.nextNode() as Text | null; node; node = walk.nextNode() as Text | null) {
        // shortcut: matches are found per text node, so a phrase split by inline formatting (code, bold, links) is missed; join text across nodes if users hit it.
      for (const at of matchOffsets(node.data, query)) {
          const range = new Range();
          range.setStart(node, at);
          // Lowercasing can change a few characters' length, so never reach past the node.
          range.setEnd(node, Math.min(at + query.length, node.length));
          found.push(range);
        }
      }
    }
    setHits(found.length);
    const current = found[Math.min(hit, found.length - 1)];
    // shortcut: without the CSS Custom Highlight API (older Firefox) matches are not painted, only scrolled to; mark them with spans if that matters.
    // Untouched while find is closed: the highlight names are page-wide, and a side chat's updates would wipe the main chat's matches.
    const paint = find === undefined || typeof Highlight === "undefined" ? undefined : CSS.highlights;
    paint?.set("inaba-find", new Highlight(...found));
    if (current) paint?.set("inaba-find-current", new Highlight(current));
    if (current && scrolledStep.current !== step) {
      scrolledStep.current = step;
      // A match folded away in a finished turn's work log is opened so it can be seen.
      for (let el = current.startContainer.parentElement?.closest("details"); el; el = el.parentElement?.closest("details")) el.open = true;
      // Centre the match itself, not its element, which can be taller than the view (a long message or <pre>).
      // shortcut: only vertical; a match far right in a wide horizontally scrolled <pre> can stay off to the side.
      const scroller = chat.current?.querySelector<HTMLElement>("[data-chat-scroll]");
      if (scroller) {
        const at = current.getBoundingClientRect();
        const view = scroller.getBoundingClientRect();
        scroller.scrollTop += at.top + at.height / 2 - (view.top + view.height / 2);
      }
    }
    return () => {
      paint?.delete("inaba-find");
      paint?.delete("inaba-find-current");
    };
  }, [find, log, hit, step]);
  const go = (by: number) => {
    if (!hits) return;
    setHit((h) => (Math.min(h, hits - 1) + by + hits) % hits);
    setStep((s) => s + 1);
  };
  const closeFind = () => {
    setFind(undefined);
    chat.current?.querySelector<HTMLElement>("[data-chat-scroll]")?.focus();
  };

  // Selected text in an answer, and where to float the button that quotes it.
  const [quoting, setQuoting] = useState<{ text: string; x: number; y: number }>();
  useEffect(() => {
    setQuoting(undefined);
    if (locked) return;
    const onSelect = () => {
      const sel = document.getSelection();
      const text = sel?.toString().trim();
      const message = (node: Node | null) => (node instanceof Element ? node : node?.parentElement)?.closest("[data-quotable]");
      const from = sel && message(sel.anchorNode);
      if (!sel || !text || !from || from !== message(sel.focusNode) || !content.current?.contains(from)) return setQuoting(undefined);
      const rect = sel.getRangeAt(0).getBoundingClientRect();
      // Kept on screen when the selection starts above the view; the button sits above y, hence the margin.
      setQuoting({ text, x: rect.left + rect.width / 2, y: Math.max(rect.top, 40) });
    };
    document.addEventListener("selectionchange", onSelect);
    return () => document.removeEventListener("selectionchange", onSelect);
  }, [locked]);
  const quote = (text: string) => {
    setQuoting(undefined);
    document.getSelection()?.removeAllRanges();
    // The composer's box is the last one: a queued message open for editing comes before it.
    const box = [...(composer.current?.querySelectorAll("textarea") ?? [])].at(-1);
    if (!box) return;
    const block = (box.value && !box.value.endsWith("\n\n") ? (box.value.endsWith("\n") ? "\n" : "\n\n") : "") + quoteMarkdown(text);
    box.focus();
    box.setSelectionRange(box.value.length, box.value.length);
    // Typed in through the textarea's own input event, so the composer keeps its attached images and undo history.
    // shortcut: the draft fallback, for a browser without insertText, drops attached images; give Composer an append prop if one turns up.
    if (!document.execCommand("insertText", false, block)) setDraft({ text: box.value + block, images: [] });
  };

  const run =(call: Promise<unknown>) => call.catch(showError).then(refresh);
  // Which rewind button was pressed (with files or not), while it runs.
  const [rewindBusy, setRewindBusy] = useState<boolean>();
  const rewind = (restoreFiles: boolean) => {
    if (!rewinding || rewindBusy !== undefined) return;
    setRewindBusy(restoreFiles);
    link.threads
      .rewind({ threadId, seq: rewinding.seq, restoreFiles })
      .then((res) => {
        setRewinding(undefined);
        setDraft({ text: res.message?.text ?? "", images: res.message?.attachments.filter((a) => a.data.length > 0) ?? [] });
        if (restoreFiles) onStatus();
        void refresh();
      }, showError)
      .finally(() => setRewindBusy(undefined));
  };

  // A new thread beside this one: same project, agent and settings, none of the conversation.
  // shortcut: a worktree thread's fresh one works in the project folder, not a worktree; pass a Worktree if that matters.
  const fresh = (title = "") =>
    link.threads.createThread({
      provider: thread.provider,
      projectId: thread.projectId,
      cwd: thread.projectId ? "" : thread.cwd,
      title,
      options: Object.fromEntries(thread.options.map((o) => [o.id, o.value])),
    });
  // Set while a side chat, fork or fresh thread is being made, so a second press does not make two.
  const starting = useRef(false);
  // Making a thread takes a while over the relay, so it is said while it runs; a failure gives back what was typed.
  const makeThread = (loading: string, make: () => Promise<unknown>, giveBack: () => unknown = () => {}) => {
    if (starting.current) return void giveBack();
    starting.current = true;
    const made = make().catch((e) => {
      giveBack();
      throw e;
    });
    toast.promise(made.finally(() => (starting.current = false)), { loading, error: errorText });
  };
  // The composer's own commands, which the agent never sees.
  const command = (name: "side" | "btw" | "clear" | "model" | "compact", arg: string) => {
    const text = arg.trim();
    // A command that fails puts what was typed back; one with nothing after it (/clear) left the
    // rest of the draft in the composer already, and attachments are never taken, so both stay.
    const giveBack = () => arg && setDraft({ text: `/${name} ${arg}` });
    switch (name) {
      case "side":
      case "btw": {
        // Forked through the last turn that ended, so asking aside works while the agent is still on the current one.
        let seq: bigint | undefined;
        let asked: bigint | undefined;
        for (const it of log.items) {
          if (it.type === "user") asked = it.seq;
          else if (it.type === "turn" && it.endedAt !== undefined) seq = asked;
        }
        if (seq === undefined) {
          toast.error(t("side.notYet"));
          return giveBack();
        }
        const title = t(name === "btw" ? "side.asideName" : "side.name", { text: text.slice(0, 40) || threadName(thread) });
        return makeThread(t("side.starting"), () =>
          link.threads.forkThread({ threadId, seq, title }).then(async (side) => {
            if (text) {
              try {
                await link.threads.startTurn({ threadId: side.id, text });
              } catch (e) {
                // Drops the side chat that never got its message.
                link.threads.deleteThread({ threadId: side.id }).catch(() => {});
                throw e;
              }
            }
            onSide(side.id, name === "btw");
          }),
          giveBack,
        );
      }
      case "clear":
        // shortcut: Th*o's stuff starts a fresh agent session in the same thread; the host has no such call, so this opens a new thread instead.
        return makeThread(t("clear.starting"), () =>
          fresh().then(async (next) => {
            if (text) {
              try {
                await link.threads.startTurn({ threadId: next.id, text });
              } catch (e) {
                // Drops the new thread that never got its message.
                link.threads.deleteThread({ threadId: next.id }).catch(() => {});
                throw e;
              }
            }
            onOpen(next.id);
          }),
          giveBack,
        );
      case "model":
        // shortcut: the model picker is the composer's first popover and has no open prop; give OptionsBar one if that stops holding.
        composer.current?.querySelector<HTMLElement>("[data-slot=popover-trigger]")?.click();
        return;
      case "compact":
        // shortcut: only agents with their own /compact compact; a handoff into a summarized fresh session needs host support.
        if (!thread.commands.some((c) => c.name === "compact")) return void toast.error(t("compact.unsupported"));
        void link.threads.startTurn({ threadId, text: text ? `/compact ${text}` : "/compact" }).then(refresh, (e) => (showError(e), giveBack()));
        return;
    }
  };

  // Transcript rows are memoized, so what they are handed stays the same and reaches this render's state through a ref.
  const row = useRef({ respond: (_requestId: string, _optionId: string): unknown => undefined, fork: (_seq: bigint) => {}, changes: onTurnChanges });
  row.current = {
    respond: (requestId, optionId) => run(link.threads.respond({ threadId, requestId, optionId })),
    fork: (seq) =>
      makeThread(t("fork.working"), () =>
        link.threads.forkThread({ threadId, seq, title: t("fork.name", { name: threadName(thread) }) }).then((fork) => {
          toast.success(t("fork.done"));
          onOpen(fork.id);
        }),
      ),
    changes: onTurnChanges,
  };
  const respond = useCallback((requestId: string, optionId: string) => row.current.respond(requestId, optionId), []);
  const fork = useCallback((seq: bigint) => row.current.fork(seq), []);
  const changes = useCallback((c: TurnScope) => row.current.changes?.(c), []);
  const loadAttachment = useCallback(
    (seq: bigint, index: number) => {
      const key = `${link.hostId}/${threadId}/${seq}/${index}`;
      let got = attachments.get(key);
      if (!got) {
        got = link.threads.getAttachment({ threadId, seq, index });
        attachments.set(key, got);
        // A failed fetch is tried again the next time the image is shown.
        got.catch(() => attachments.delete(key));
      }
      return got;
    },
    [link, threadId],
  );

  // One per message of the user's, with the answer shown under it, for the chat map: blocks() shows a turn's last assistant item and folds earlier ones.
  // Those above what is read in come from the thread's list of messages, without their answers.
  const exchanges = useMemo(() => {
    const out: Exchange[] = [];
    const above = first ?? 0n;
    for (const it of whole) if (it.type === "user" && it.seq < above) out.push({ seq: String(it.seq), text: exchangeText(it), answer: "" });
    for (const it of log.items) {
      if (it.type === "user") out.push({ seq: String(it.seq), text: exchangeText(it), answer: "" });
      else if (it.type === "assistant" && out.length) out[out.length - 1].answer = it.text;
    }
    return out;
  }, [log.items, whole, first]);
  const scroller = useRef<HTMLDivElement>(null);

  return (
    <div ref={chat} className="contents" onPointerEnter={() => (hovered.current = true)} onPointerLeave={() => (hovered.current = false)}>
      <style>{`::highlight(inaba-find){background-color:color-mix(in oklab,var(--warning) 35%,transparent)}::highlight(inaba-find-current){background-color:var(--warning);color:var(--warning-foreground)}.dark ::highlight(inaba-find-current){color:var(--background)}`}</style>
      {/* --warning is bright yellow in both themes, but dark's --warning-foreground is yellow too, so the current match takes dark's near-black background as its text. */}
      <div className="@container/chat relative flex min-h-0 flex-1 flex-col">
      <ChatMap exchanges={exchanges} scroller={scroller} reach={(seq) => loadOlder(BigInt(seq))} />
      {find !== undefined && (
        <div
          role="search"
          className="bg-popover text-popover-foreground absolute top-2 right-4 z-10 flex items-center gap-0.5 rounded-lg border p-1 shadow-sm"
          onKeyDown={(e) => {
            // Escape while composing Korean cancels the syllable, not the bar.
            if (e.key === "Escape" && !e.nativeEvent.isComposing) {
              e.preventDefault();
              closeFind();
            }
          }}
        >
          <Input
            ref={findBox}
            autoFocus
            value={find}
            aria-label={t("find.label")}
            placeholder={t("find.label")}
            className="h-7 w-44"
            onChange={(e) => {
              setFind(e.target.value);
              setHit(0);
              setStep((s) => s + 1);
            }}
            onKeyDown={(e) => {
              // Enter that commits an IME syllable is not a step to the next match.
              if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
              e.preventDefault();
              go(e.shiftKey ? -1 : 1);
            }}
          />
          <span className="text-muted-foreground min-w-10 px-1 text-center text-xs tabular-nums" aria-live="polite">
            {find && `${hits ? Math.min(hit, hits - 1) + 1 : 0}/${hits}`}
          </span>
          <Button size="icon-xs" variant="ghost" aria-label={t("find.previous")} title={t("find.previous")} disabled={!hits} onClick={() => go(-1)}>
            <ChevronUpIcon />
          </Button>
          <Button size="icon-xs" variant="ghost" aria-label={t("find.next")} title={t("find.next")} disabled={!hits} onClick={() => go(1)}>
            <ChevronDownIcon />
          </Button>
          <Button size="icon-xs" variant="ghost" aria-label={t("find.close")} title={t("find.close")} onClick={closeFind}>
            <XIcon />
          </Button>
        </div>
      )}
      <div
        ref={scroller}
        data-chat-scroll
        tabIndex={-1}
        className="min-h-0 flex-1 overflow-y-auto outline-none [overflow-anchor:none]"
        onScroll={(e) => {
          const el = e.currentTarget;
          near.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
          if (el.scrollTop < 600) loadOlder();
          if (near.current) setBehind(false);
          setQuoting(undefined);
        }}
      >
        <WorkflowReader value={readWorkflowAgent}>
          <div ref={content} className="mx-auto flex max-w-3xl flex-col gap-3 px-4 py-6">
            {log.items.length === 0 &&
              (log.lastSeq < thread.lastSeq || (first ?? 1n) > 1n ? (
                <div className="text-muted-foreground mx-auto my-16 flex flex-col items-center gap-2 text-sm" role="status">
                  <Spinner className="size-6" />
                  {t("chat.loading")}
                  {received > 0n && <span className="text-xs tabular-nums">{Number(((received - (first ?? 1n)) * 100n) / (thread.lastSeq - (first ?? 1n) + 1n))}%</span>}
                </div>
              ) : (
                <p className="text-muted-foreground py-16 text-center text-sm">{t("chat.empty")}</p>
              ))}
            {(first ?? 1n) > 1n && log.items.length > 0 && (
              <div className="text-muted-foreground flex justify-center py-2" role="status">
                <Spinner className="size-4" />
                <span className="sr-only">{t("chat.loading")}</span>
              </div>
            )}
            {keyed(blocks(log.items)).map(([key, block]) =>
              "work" in block ? (
                <WorkLog key={key} turn={block.turn} work={block.work} replaying={log.lastSeq < opened.current} />
              ) : (
                <ItemView
                  key={key}
                  item={block}
                  names={names}
                  respond={locked ? undefined : respond}
                  onChanges={changes}
                  onRewind={busy || locked ? undefined : setRewinding}
                  onFork={fork}
                  loadAttachment={loadAttachment}
                />
              ),
            )}
            {[...sending, ...held].map((m) => (
              <SendingMessage key={m.key} text={m.text} attachments={m.attachments} />
            ))}
            <div ref={bottom} />
          </div>
        </WorkflowReader>
      </div>
      {behind && (
        <Button
          size="sm"
          variant="secondary"
          className="absolute bottom-3 left-1/2 -translate-x-1/2 rounded-full border shadow-sm"
          onClick={() => bottom.current?.scrollIntoView({ block: "end", behavior: "smooth" })}
        >
          <ArrowDownIcon />
          {t("chat.jumpLatest")}
        </Button>
      )}
      </div>
      {quoting && !locked && (
        <Button
          size="xs"
          variant="secondary"
          className="fixed z-20 -translate-x-1/2 -translate-y-full border shadow-md"
          style={{ left: quoting.x, top: quoting.y - 6 }}
          // Keeps the selection while the button is pressed.
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => quote(quoting.text)}
        >
          <QuoteIcon />
          {t("chat.quote")}
        </Button>
      )}
      <Dialog open={rewinding !== undefined} onOpenChange={(open) => !open && setRewinding(undefined)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("rewind.title")}</DialogTitle>
            <DialogDescription>{t(thread.worktree ? "rewind.helpWorktree" : "rewind.help")}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            {thread.worktree && (
              <Button variant="outline" disabled={rewindBusy !== undefined} onClick={() => rewind(true)}>
                {rewindBusy === true && <Spinner data-icon="inline-start" />}
                {t("rewind.withFiles")}
              </Button>
            )}
            <Button disabled={rewindBusy !== undefined} onClick={() => rewind(false)}>
              {rewindBusy === false && <Spinner data-icon="inline-start" />}
              {t("rewind.confirm")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <div className="mx-auto grid w-full max-w-3xl grid-cols-1 gap-2 px-4 pb-4">
        {/* What is queued, and the composer, are the driver's to change; the composer takes typing before that is known. */}
        <fieldset disabled={locked} className="contents">
        <QueueList
          queue={thread.queue}
          paused={thread.queuePaused}
          onEdit={(queuedId, text, attachments) => link.threads.editQueued({ threadId, queuedId, text, attachments }).then(refresh)}
          onMove={(queuedId, toIndex) => run(link.threads.moveQueued({ threadId, queuedId, toIndex }))}
          onSendNow={(queuedId) => run(link.threads.sendQueuedNow({ threadId, queuedId }))}
          onRemove={(queuedId) => run(link.threads.cancelQueued({ threadId, queuedId }))}
          onResume={() => run(link.threads.resumeQueue({ threadId }))}
        />
        </fieldset>
        <fieldset ref={composer} disabled={locked && !pending} className="contents">
        <Composer
          link={link}
          thread={thread}
          busy={busy}
          draft={draft}
          onSend={submit}
          onStop={(force) => run(link.threads.interrupt({ threadId, force }))}
          onSetOption={(optionId, value) => run(link.threads.setOption({ threadId, optionId, value }))}
          mentions={mentions}
          provider={provider && { id: provider.id, name: provider.displayName || provider.id, icon: provider.icon }}
          providers={providers.filter((p) => p.structured).map((p) => ({ id: p.id, name: p.displayName || p.id, icon: p.icon }))}
          onProvider={(id) => run(link.threads.updateThread({ threadId, provider: id }))}
          pastPrompts={log.items.flatMap((it) => (it.type === "user" ? [it.text] : [])).reverse()}
          onCommand={command}
        />
        </fieldset>
      </div>
    </div>
  );
}

// Images the event stream leaves out, by host, thread, message and place, shared by every view of them.
// shortcut: kept for the page's life; drop entries with their thread if memory ever matters.
const attachments = new Map<string, Promise<Attachment>>();



/** A span of time as people say it: 42s, 3m 5s, 1h 2m. */
function formatDuration(ms: number) {
  const seconds = Math.max(1, Math.round(ms / 1000));
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
  return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
}

/** The current time, ticking each second while on; for durations that are still growing. */
function useNow(on: boolean) {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    if (!on) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1_000);
    return () => clearInterval(timer);
  }, [on]);
  return now;
}

/** How long a workflow agent ran, or has run so far; "" when the host did not say when it started. */
function agentDuration(a: WorkflowAgentItem, now: number) {
  if (!a.startedAt) return "";
  const end = a.endedAt ? timestampMs(a.endedAt) : a.done || a.stopped ? undefined : now;
  return end === undefined ? "" : formatDuration(end - timestampMs(a.startedAt));
}

const agentRunning = (a: WorkflowAgentItem) => !a.done && !a.stopped;

/** A workflow agent's state as an icon: done, stopped before it finished, or running. */
function AgentIcon({ agent, className }: { agent: WorkflowAgentItem; className: string }) {
  if (agent.done) return <CheckIcon className={cn("text-success", className)} />;
  if (agent.stopped) return <CircleStopIcon className={cn("text-muted-foreground", className)} />;
  return <Spinner className={className} />;
}

// blocks() makes a new work list each render; the same steps in it need no new render.
const WorkLog = memo(
  function WorkLog({ turn, work, replaying }: { turn: Turn; work: Item[]; replaying: boolean }) {
    const { t } = useI18n();
    // A turn without its end while the replay is still coming is history not read yet, not work going on.
    const running = turn.endedAt === undefined && !replaying;
    if (!running && turn.endedAt === undefined && !work.length) return null;
    const duration = formatDuration((turn.endedAt ?? Date.now()) - turn.startedAt);
    return (
      // Open while the agent works, so its steps show as they happen; folded once it has answered.
      <details className="group/work border-border/60 border-b pb-2" open={running}>
        <summary className="text-muted-foreground flex cursor-pointer list-none items-center gap-1.5 text-sm tabular-nums [&::-webkit-details-marker]:hidden">
          {running && <Spinner />}
          {running ? t("chat.working") : turn.endedAt !== undefined && t("chat.workedFor", { duration })}
          {work.length > 0 && <span className="text-xs">{turn.endedAt !== undefined || running ? "· " : ""}{t("chat.steps", { count: String(work.length) })}</span>}
          <ChevronRightIcon className="size-3.5 transition-transform group-open/work:rotate-90" />
          <Clock at={turn.startedAt} className="ml-auto" />
        </summary>
        <div className="flex flex-col gap-1 pt-2">
          {work.map((item, i) => (
            <ItemView key={i} item={item} inWorkLog />
          ))}
        </div>
      </details>
    );
  },
  (a, b) => a.turn === b.turn && a.replaying === b.replaying && a.work.length === b.work.length && a.work.every((it, i) => it === b.work[i]),
);

/** A time of day, with the full date on hover. */
function Clock({ at, className }: { at: number; className?: string }) {
  const { locale } = useI18n();
  const date = new Date(at);
  return (
    <time dateTime={date.toISOString()} title={date.toLocaleString(locale)} className={cn("text-muted-foreground text-xs", className)}>
      {date.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" })}
    </time>
  );
}

/** What can be done with a message. Out of the way until the message is hovered or focused. */
function MessageActions({ text, at, onRewind, onFork }: { text: string; at: number; onRewind?: () => void; onFork?: () => void }) {
  const { t } = useI18n();
  return (
    <div className="flex items-center gap-1.5 opacity-0 transition-opacity group-focus-within/message:opacity-100 group-hover/message:opacity-100 pointer-coarse:opacity-100">
      {onFork && (
        <Button size="icon-xs" variant="ghost" aria-label={t("fork.action")} title={t("fork.action")} onClick={onFork}>
          <SplitIcon />
        </Button>
      )}
      {onRewind && (
        <Button size="icon-xs" variant="ghost" aria-label={t("rewind.action")} title={t("rewind.action")} onClick={onRewind}>
          <Undo2Icon />
        </Button>
      )}
      <Button
        size="icon-xs"
        variant="ghost"
        aria-label={t("chat.copy")}
        title={t("chat.copy")}
        onClick={() => navigator.clipboard.writeText(text).then(() => toast.success(t("common.copied")), showError)}
      >
        <CopyIcon />
      </Button>
      <Clock at={at} />
    </div>
  );
}

/** One flat line of the work log: a small icon and what happened. No box around it. */
/** A tool call, opened to show what it was given and what it gave back. */
function ToolRow({ item }: { item: Extract<Item, { type: "tool" }> }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const more = Boolean(item.output || (item.input && item.input !== item.title));
  const title = (
    <>
      <span className={cn("wrap-anywhere", item.kind === "execute" && "font-mono text-xs", open && item.kind === "execute" && "whitespace-pre-wrap")}>{item.title || item.kind || "tool"}</span>
      {/* Only a failure is worth a word; the rest is the normal course of things. */}
      {item.status === "failed" && <span className="text-destructive ml-2 text-xs">{t("chat.toolFailed")}</span>}
    </>
  );
  return (
    <Row icon={toolIcons[item.kind] ?? <WrenchIcon />}>
      {more ? (
        <button type="button" className="hover:text-foreground inline-flex max-w-full items-start gap-1 text-left" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
          {/* One line until opened, however long the command. */}
          <span className={cn("min-w-0", !open && "truncate")}>{title}</span>
          <ChevronRightIcon className={cn("mt-1 size-3.5 shrink-0 transition-transform", open && "rotate-90")} />
        </button>
      ) : (
        title
      )}
      {open && (
        <div className="bg-muted/50 mt-1 mb-2 max-h-80 overflow-auto rounded-md border font-mono text-xs leading-5">
          {item.input && item.input !== item.title && <pre className="text-foreground border-b px-3 py-2 break-all whitespace-pre-wrap">{item.kind === "execute" ? `$ ${item.input}` : item.input}</pre>}
          <pre className="px-3 py-2 break-all whitespace-pre-wrap">{item.output || t("chat.noOutput")}</pre>
        </div>
      )}
      {item.agents.length > 0 && <WorkflowAgents toolCallId={item.id} agents={item.agents} />}
    </Row>
  );
}

type WorkflowAgentItem = Extract<Item, { type: "tool" }>["agents"][number];

/** Reads what one agent of a workflow did, for the thread on screen. */
const WorkflowReader = createContext<((toolCallId: string, agentId: string, signal: AbortSignal) => AsyncIterable<{ steps: WorkflowStep[] }>) | undefined>(undefined);

/**
 * The agents a background workflow spawned: one folded line saying how many and how far
 * along, opened to a line per agent; each agent's own conversation opens on its own.
 */
function WorkflowAgents({ toolCallId, agents }: { toolCallId: string; agents: WorkflowAgentItem[] }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [shown, setShown] = useState<string>();
  const done = agents.filter((a) => a.done).length;
  const running = agents.filter(agentRunning).length;
  const stopped = agents.length - done - running;
  const now = useNow(running > 0);
  const agent = agents.find((a) => a.id === shown);
  return (
    <Collapsible open={open} onOpenChange={setOpen} className="mt-1 mb-2 rounded-md border">
      <CollapsibleTrigger className="hover:bg-muted/50 flex w-full items-center gap-2 px-3 py-2 text-left text-xs">
        {running > 0 ? <Spinner className="size-3.5" /> : stopped > 0 ? <CircleStopIcon className="text-muted-foreground size-3.5" /> : <CheckIcon className="text-success size-3.5" />}
        <span className="text-foreground font-medium">{t("chat.workflowAgentCount", { count: String(agents.length) })}</span>
        <span className="min-w-0 flex-1 truncate">
          {running > 0
            ? t("chat.workflowRunning", { running: String(running), done: String(done) })
            : stopped > 0
              ? t("chat.workflowStopped", { stopped: String(stopped), done: String(done) })
              : t("chat.workflowDone")}
        </span>
        <ChevronDownIcon className={cn("size-3.5 shrink-0 transition-transform", open && "rotate-180")} />
      </CollapsibleTrigger>
      <CollapsibleContent className="grid gap-0.5 border-t p-1">
        {agents.map((a) => (
          <button key={a.id} type="button" className="hover:bg-muted flex min-w-0 items-center gap-2 rounded-sm px-2 py-1 text-left text-xs" onClick={() => setShown(a.id)}>
            <AgentIcon agent={a} className="size-3.5 shrink-0" />
            <span className="text-foreground shrink-0">{a.label || a.id}</span>
            {a.phase && <span className="shrink-0">{a.phase}</span>}
            {agentRunning(a) && a.activity && <span className="min-w-0 flex-1 truncate font-mono">{a.activity}</span>}
            {a.stopped && <span className="shrink-0">{t("chat.workflowAgentStopped")}</span>}
            <span className="ml-auto shrink-0 tabular-nums">{agentDuration(a, now)}</span>
            <ChevronRightIcon className="size-3.5 shrink-0" />
          </button>
        ))}
      </CollapsibleContent>
      <Sheet open={agent !== undefined} onOpenChange={(o) => !o && setShown(undefined)}>
        <SheetContent side="right" className="w-full gap-0 p-0 sm:max-w-xl">{agent && <AgentTranscript toolCallId={toolCallId} agent={agent} />}</SheetContent>
      </Sheet>
    </Collapsible>
  );
}

/** One workflow agent's own conversation, read only, kept current while it runs. */
function AgentTranscript({ toolCallId, agent }: { toolCallId: string; agent: WorkflowAgentItem }) {
  const { t } = useI18n();
  const now = useNow(agentRunning(agent));
  const read = useContext(WorkflowReader);
  const [steps, setSteps] = useState<WorkflowStep[]>();
  const [error, setError] = useState<string>();
  useEffect(() => {
    if (!read) return;
    // The host sends the conversation again whenever it grows, and ends the stream once the agent is done.
    const abort = new AbortController();
    void (async () => {
      try {
        for await (const res of read(toolCallId, agent.id, abort.signal)) {
          setSteps(res.steps);
          setError(undefined);
        }
      } catch (e) {
        if (!abort.signal.aborted) setError(errorText(e));
      }
    })();
    return () => abort.abort();
  }, [read, toolCallId, agent.id]);
  return (
    <>
      <div className="grid gap-0.5 border-b px-4 py-3 pr-12">
        <SheetTitle className="flex items-center gap-2">
          <AgentIcon agent={agent} className="size-4" />
          {agent.label || agent.id}
        </SheetTitle>
        <p className="text-muted-foreground text-xs tabular-nums">
          {[agent.phase, t(agent.done ? "chat.workflowAgentDone" : agent.stopped ? "chat.workflowAgentStopped" : "chat.workflowAgentRunning"), agentDuration(agent, now)].filter(Boolean).join(" · ")}
        </p>
      </div>
      {/* A column whose steps keep their height, so an opened tool output pushes what follows down. */}
      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-4 py-4 *:shrink-0">
        {error && <p className="text-destructive text-sm">{error}</p>}
        {!steps && !error && <Spinner />}
        {steps?.map((s, i) =>
          s.kind === "prompt" ? (
            <div key={i} className="bg-muted/50 rounded-md border px-3 py-2 text-xs whitespace-pre-wrap">
              {s.text}
            </div>
          ) : s.kind === "tool" ? (
            <ToolRow key={i} item={{ type: "tool", id: String(i), title: s.input ? `${s.tool}: ${s.input}` : s.tool, kind: "", status: s.failed ? "failed" : "", input: "", output: s.output, agents: [] }} />
          ) : (
            <Markdown key={i}>{s.text}</Markdown>
          ),
        )}
      </div>
    </>
  );
}

function Row({ icon, children, className }: { icon: ReactNode; children: ReactNode; className?: string }) {
  return (
    <div className={cn("text-muted-foreground flex min-h-6 items-start gap-2 text-sm leading-6", className)}>
      <span className="flex h-6 w-4 shrink-0 items-center justify-center [&>svg]:size-4">{icon}</span>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}

// What kind of thing a tool call does, as the agent reports it.
const toolIcons: Record<string, ReactNode> = {
  execute: <SquareTerminalIcon />,
  read: <FileTextIcon />,
  edit: <FilePenLineIcon />,
  delete: <FilePenLineIcon />,
  move: <FilePenLineIcon />,
  search: <SearchIcon />,
  fetch: <GlobeIcon />,
  think: <BrainIcon />,
};

/** A message of the user's still on its way to the host, dimmed, with how much it is carrying. */
function SendingMessage({ text, attachments }: { text: string; attachments: Attachment[] }) {
  const { t } = useI18n();
  const size = attachments.reduce((n, a) => n + a.data.length, 0);
  return (
    <div className="flex max-w-[85%] flex-col items-end gap-1 self-end" aria-busy="true">
      <div className="flex flex-col items-end gap-1 opacity-60">
        {attachments.some((a) => a.data.length > 0) && (
          <div className="flex flex-wrap justify-end gap-1.5">
            {attachments.map((a, i) => a.data.length > 0 && <AttachmentImage key={i} attachment={a} className="max-h-40 max-w-60" />)}
          </div>
        )}
        {text && <div className="bg-muted rounded-2xl px-3.5 py-2 wrap-anywhere whitespace-pre-wrap">{text}</div>}
      </div>
      <span className="text-muted-foreground flex items-center gap-1.5 text-xs tabular-nums">
        <Spinner className="size-3" />
        {size > 0 ? t("chat.sendingSize", { size: bytes(size) }) : t("chat.sending")}
      </span>
    </div>
  );
}

const ItemView = memo(function ItemView({
  item,
  respond,
  inWorkLog,
  onChanges,
  onRewind,
  onFork,
  names,
  loadAttachment,
}: {
  /** Providers' names by id, for notes that name them. */
  names?: Record<string, string>;
  item: Item;
  /** Answers the agent's questions; without it, as for a watcher, they are shown but not answered. */
  respond?: (requestId: string, optionId: string) => unknown;
  /** A step inside a turn's work log, shown without message actions. */
  inWorkLog?: boolean;
  /** Opens the diff of a turn's changes. */
  onChanges?: (changes: Extract<Item, { type: "changes" }>) => void;
  /** Takes the conversation back to before a message of the user's; absent while a turn runs. */
  onRewind?: (message: Extract<Item, { type: "user" }>) => void;
  /** Starts a new thread from the conversation through this answer's turn. */
  onFork?: (seq: bigint) => void;
  /** Fetches an image the event stream left out, by its message and place. */
  loadAttachment?: (seq: bigint, index: number) => Promise<Attachment>;
}) {
  const { t } = useI18n();
  // The answer being sent, so a second press does not answer twice.
  const [chosen, setChosen] = useState<string>();
  switch (item.type) {
    case "user":
      return (
        <div data-seq={inWorkLog ? undefined : String(item.seq)} className="group/message flex max-w-[85%] flex-col items-end gap-1 self-end">
          {item.attachments.length > 0 && (
            <div className="flex flex-wrap justify-end gap-1.5">
              {item.attachments.map((a, i) =>
                a.mimeType.startsWith("image/") && !a.path ? (
                  <SentImage key={i} attachment={a} load={loadAttachment && (() => loadAttachment(item.seq, i))} at={`${item.seq}/${i}`} />
                ) : (
                  // A file the host saved for the agent (or one pointed at by path): its name, and size when known.
                  <span key={i} className="bg-muted text-muted-foreground flex max-w-60 items-center gap-1.5 rounded-lg border px-2 py-1 text-xs" title={a.path}>
                    <FileIcon className="size-3.5 shrink-0" />
                    <span className="truncate">{a.name || a.path}</span>
                    {a.size !== undefined && a.size > 0n && <span className="shrink-0 tabular-nums">{bytes(a.size)}</span>}
                  </span>
                ),
              )}
            </div>
          )}
          {item.text && <div className="bg-muted rounded-2xl px-3.5 py-2 wrap-anywhere whitespace-pre-wrap">{item.text}</div>}
          <MessageActions text={item.text} at={item.at} onRewind={onRewind && (() => onRewind(item))} />
        </div>
      );
    case "assistant":
      // data-quotable marks the text a selection can be quoted from.
      if (inWorkLog)
        return (
          <div data-quotable>
            <Markdown>{item.text}</Markdown>
          </div>
        );
      return (
        <div className="group/message flex flex-col gap-1">
          <div data-quotable>
            <Markdown>{item.text}</Markdown>
          </div>
          <MessageActions text={item.text} at={item.at} onFork={onFork && (() => onFork(item.seq))} />
        </div>
      );
    case "thought":
      return (
        <Row icon={<BrainIcon />}>
          <span className="line-clamp-4 wrap-anywhere whitespace-pre-wrap">{item.text}</span>
        </Row>
      );
    case "note":
      return (
        <div className="text-muted-foreground py-1 text-center text-xs">
          {t(item.key, item.values && Object.fromEntries(Object.entries(item.values).map(([k, v]) => [k, (item.key === "chat.providerChanged" && names?.[v]) || v])))}
        </div>
      );
    case "restarted":
      return (
        <div role="status" className="text-muted-foreground flex items-center justify-center gap-1.5 py-1 text-xs">
          <Logo className="size-4" />
          {t("chat.restarted")}
        </div>
      );
    case "error":
      return (
        <Row icon={<CircleAlertIcon />} className="text-destructive">
          <span className="font-medium">{t("chat.turnFailed")}</span> <span className="wrap-anywhere">{item.text}</span>
        </Row>
      );
    case "tool":
      return <ToolRow item={item} />;
    case "permission":
      return (
        <Row icon={<ShieldQuestionIcon />} className={item.answer ? undefined : "text-foreground"}>
          <span className="wrap-anywhere">{t("chat.asks", { title: item.title })}</span>
          {item.answer ? (
            <span className="ml-2 text-xs">· {item.options.find((o) => o.id === item.answer)?.name ?? t("chat.cancelled")}</span>
          ) : (
            <div className="flex flex-wrap gap-1.5 py-1.5">
              {item.options.map((o) => (
                <Button
                  key={o.id}
                  size="sm"
                  variant={o.kind.startsWith("allow") ? "default" : "outline"}
                  disabled={!respond || chosen !== undefined}
                  onClick={() => {
                    setChosen(o.id);
                    void Promise.resolve(respond?.(item.requestId, o.id)).finally(() => setChosen(undefined));
                  }}
                >
                  {chosen === o.id && <Spinner data-icon="inline-start" />}
                  {o.name}
                </Button>
              ))}
            </div>
          )}
        </Row>
      );
    case "changes":
      return (
        <button type="button" className="hover:bg-muted -mx-2 rounded-md px-2 text-left" onClick={() => onChanges?.(item)}>
          <Row icon={<FileDiffIcon />}>
            {t("changes.files", { count: String(item.files) })} <LineCounts additions={item.additions} deletions={item.deletions} className="ml-1" />
          </Row>
        </button>
      );
    case "plan":
      return <PlanCard entries={item.entries} />;
    case "turn":
      return null;
    case "raw":
      return (
        <details className="text-muted-foreground font-mono text-xs">
          <summary className="cursor-pointer leading-6">{item.method}</summary>
          <pre className="bg-muted mt-1 overflow-x-auto rounded-md p-2">{item.json}</pre>
        </details>
      );
  }
});

/** The agent's plan: its steps, each marked done, under way or waiting. */
function PlanCard({ entries }: { entries: Plan_Entry[] }) {
  const { t } = useI18n();
  const done = entries.filter((e) => e.status === "completed").length;
  return (
    <section className="bg-card grid gap-1.5 rounded-xl border px-3.5 py-3" aria-label={t("plan.title")}>
      <header className="text-muted-foreground flex items-center gap-1.5 text-xs font-medium">
        <ListChecksIcon className="size-3.5" />
        {t("plan.title")}
        <span className="ml-auto tabular-nums">
          {done}/{entries.length}
        </span>
        <Button
          size="icon-xs"
          variant="ghost"
          aria-label={t("plan.copy")}
          title={t("plan.copy")}
          // Inside a promise so a missing navigator.clipboard (plain-http hosts) is reported, not thrown.
          onClick={() => Promise.resolve().then(() => navigator.clipboard.writeText(planMarkdown(entries))).then(() => toast.success(t("common.copied")), showError)}
        >
          <CopyIcon />
        </Button>
      </header>
      <ol className="grid gap-1">
        {entries.map((e, i) => (
          <li key={i} className={cn("flex items-start gap-2 text-sm", e.status === "completed" && "text-muted-foreground line-through")}>
            <span className="flex h-5 w-4 shrink-0 items-center justify-center">
              {e.status === "completed" ? (
                <CheckIcon className="text-success size-4" />
              ) : e.status === "in_progress" ? (
                <CircleDotIcon className="text-info size-3.5" />
              ) : (
                <CircleDashedIcon className="text-muted-foreground size-3.5" />
              )}
            </span>
            <span className="min-w-0 wrap-anywhere">{e.content}</span>
          </li>
        ))}
      </ol>
    </section>
  );
}

/**
 * An image the user sent, opened large on click, with a way to save it. The event stream
 * leaves its bytes out, so load fetches them; at names which image, for when to fetch again.
 */
function SentImage({ attachment: sent, load, at }: { attachment: Attachment; load?: () => Promise<Attachment>; at: string }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [loaded, setLoaded] = useState<{ at: string; attachment?: Attachment; error?: string }>();
  const missing = sent.data.length === 0;
  useEffect(() => {
    if (!missing || !load) return;
    let live = true;
    load().then(
      (attachment) => live && setLoaded({ at, attachment }),
      (e) => live && setLoaded({ at, error: errorText(e) }),
    );
    return () => void (live = false);
  }, [missing, at]); // eslint-disable-line react-hooks/exhaustive-deps
  const attachment = missing ? (loaded?.at === at ? loaded.attachment : undefined) : sent;
  const name = sent.name || t("image.title");
  if (!attachment) {
    if (loaded?.at === at && loaded.error)
      return (
        <span className="text-destructive flex max-w-60 items-center gap-1.5 rounded-lg border px-2 py-1 text-xs" title={loaded.error}>
          <CircleAlertIcon className="size-3.5 shrink-0" />
          <span className="truncate">{name}</span>
        </span>
      );
    // The size of a shown image at most, so the conversation does not jump as it arrives.
    return (
      <span className="bg-muted flex size-40 items-center justify-center rounded-lg border" aria-busy="true" aria-label={name}>
        <Spinner className="text-muted-foreground" />
      </span>
    );
  }
  const download = () => {
    const url = URL.createObjectURL(new Blob([attachment.data as BlobPart], { type: attachment.mimeType }));
    const a = document.createElement("a");
    a.href = url;
    // An unnamed image still gets an extension so it opens once saved.
    a.download = attachment.name || `${name}.${attachment.mimeType.split("/")[1]?.split("+")[0] || "png"}`;
    a.click();
    // Safari starts the download after click returns, so the URL has to outlive it a little.
    setTimeout(() => URL.revokeObjectURL(url), 1_000);
  };
  return (
    <>
      <button type="button" aria-label={t("image.open", { name })} className="focus-visible:ring-ring rounded-lg outline-none focus-visible:ring-2" onClick={() => setOpen(true)}>
        <AttachmentImage attachment={attachment} className="max-h-40 max-w-60" />
      </button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-[min(90vw,64rem)]">
          <DialogHeader>
            <DialogTitle className="truncate pr-8">{name}</DialogTitle>
          </DialogHeader>
          <AttachmentImage attachment={attachment} className="mx-auto max-h-[75vh] max-w-full object-contain" />
          <DialogFooter>
            <Button variant="outline" onClick={download}>
              <DownloadIcon />
              {t("image.download")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
