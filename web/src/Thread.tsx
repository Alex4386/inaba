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
  EyeIcon,
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
import { createContext, type FormEvent, memo, type ReactNode, useCallback, useContext, useEffect, useMemo, useReducer, useRef, useState } from "react";
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
  ThreadMode,
  ThreadStatus,
} from "./gen/inaba/v1/host_pb";
import { TerminalView } from "./Terminal";
import { ChangesPanel, LineCounts, type TurnScope } from "@/components/changes-panel";

import { WhereCard } from "@/components/where-card";
import { allTabs, dockOf, type Docks, type DockState, moveTab, newTabId, oneChanges, placeTab, type Surface, type TabId, withoutTab } from "@/lib/tabs";
import { CLIENT, PeopleStack, usePresent } from "@/components/presence";
import { type Host, PresenceService } from "./gen/inaba/v1/server_pb";
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { FilesPanel } from "@/components/files-panel";
import { BrowserPanel } from "@/modules/browser/BrowserPanel";
import { AttachmentImage, Composer, type Draft } from "@/components/composer";
import { QueueList } from "@/components/queue-list";
import { ConfirmDialog, RenameDialog } from "@/components/dialogs";
import { Markdown } from "@/components/markdown";
import { Badge } from "@/components/reui/badge";
import { ProjectIcon, threadFailed, threadName, threadState } from "@/components/project-tree";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuSub, DropdownMenuSubContent, DropdownMenuSubTrigger, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
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
  const toggleDock = (dock: Dock) => updateDock(dock, (d) => ({ ...d, open: !d.open }));
  // Opens a surface in a dock: another terminal, file tree or page each time, and the one diff moved there.
  const openSurface = (dock: Dock, surface: Surface) => setDocks((all) => placeTab(all, dock, newTabId(surface, allTabs(all))));
  // Takes a tab from whichever dock holds it into this one, keeping what it shows.
  const moveTo = (dock: Dock, tab: TabId) => setDocks((all) => placeTab(all, dock, tab));
  // A turn's changes show in the changes panel beside the conversation, opening it if the driver may.
  const showTurn = (turn: TurnScope) => {
    setDiffScope(turn.after);
    const tab = allTabs(now.current.docks).find((x) => surfaceOf(x) === "changes");
    if (tab) {
      const dock = dockOf(now.current.docks, tab);
      if (dock && now$.current.driving) updateDock(dock, (d) => ({ ...d, open: true, active: tab }));
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
  const closeTab = (tab: TabId) => {
    setDocks((all) => withoutTab(all, tab));
    // A browser tab's page closes with it.
    const page = pageOf[tab];
    if (page) {
      link.browser.navigate({ threadId, pageId: page, action: "close" }).catch(() => {});
      setPageOf(({ [tab]: _, ...rest }) => rest);
    }
    // A terminal's process ends with its tab.
    if (surfaceOf(tab) === "shell") link.shell.closeShell({ threadId, shellId: tab }).catch(() => {});
  };
  const isMobile = useIsMobile();
  // The sizes the user dragged the docks to, kept across threads and reloads.
  const rightLayout = useDefaultLayout({ id: "inaba-thread-side", panelIds: docks.right.open ? ["main", "side"] : ["main"] });
  const bottomLayout = useDefaultLayout({ id: "inaba-thread-bottom", panelIds: docks.bottom.open ? ["top", "bottom"] : ["top"] });
  const [thread, setThread] = useState<Thread>();
  const [provider, setProvider] = useState<Provider>();
  // Every provider the host offers, for moving the thread to another between turns.
  const [providers, setProviders] = useState<Provider[]>([]);
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

  // Why the thread could not be read, while none is shown yet.
  const [loadError, setLoadError] = useState<string>();
  const refresh = useCallback(() => link.threads.getThread({ threadId }).then(setThread, (e) => (showError(e), setLoadError(errorText(e)))), [link, threadId]);
  useEffect(() => {
    setLoadError(undefined);
    void refresh();
  }, [refresh]);
  // Opening the thread marks it read; the conversation marks newer events as they arrive.
  useMarkSeen(link.hostId, threadId, thread?.lastSeq);
  // The thread's project, for the header's path and the file tree's first step.
  const projectId = thread?.projectId;
  const [project, setProject] = useState<Project>();
  useEffect(() => {
    if (projectId) link.projects.listProjects({}).then((r) => setProject(r.projects.find((p) => p.id === projectId)), () => setProject(undefined));
  }, [link, projectId]);
  const projectName = project?.name;
  const providerId = thread?.provider;
  useEffect(() => {
    if (providerId)
      link.threads.listProviders({}).then((r) => {
        setProviders(r.providers);
        setProvider(r.providers.find((p) => p.id === providerId));
      }, showError);
  }, [link, providerId]);

  // What changed in the folder is worth another look whenever the thread comes to rest.
  const status = thread?.status;
  // A call made while the direct connection is still forming can fail once; one more try covers it.
  const loadGit = useCallback(
    (known?: GitStatus) => {
      if (known) return setGit(known);
      const get = () => link.git.getStatus({ threadId });
      return get().then(setGit, () => new Promise((r) => setTimeout(r, 1500)).then(get).then(setGit, () => setGit(undefined)));
    },
    [link, threadId],
  );
  useEffect(() => {
    if (status !== undefined) void loadGit();
  }, [loadGit, status]);

  // The docks are the thread's, not this browser's: everyone in it sees the same screen.
  // The layout last sent or taken, so neither side echoes the other.
  const synced = useRef("");
  const layout = thread?.layout;
  useEffect(() => {
    if (!layout || layout === synced.current) return;
    synced.current = layout;
    try {
      const { pages = {}, driver: d, ...saved } = JSON.parse(layout);
      // Only the known docks are taken, so a key this build does not know is ignored instead of dropping the layout.
      const shown = Object.fromEntries(DOCKS.map((dock) => [dock, Array.isArray(saved[dock]?.tabs) ? saved[dock] : { open: false, tabs: [] }])) as unknown as Docks<Dock>;
      // A layout saved when the changes panel could open twice keeps only its first; the driver sends the fix back.
      setDocks(oneChanges(shown));
      setPageOf(pages);
      if (d !== now$.current.driver) {
        learned.current = performance.now();
        // The seat came free: ask at once, so a lone watcher takes it without waiting for the next beat.
        if (!d) askSeat();
      }
      setDriver(d);
    } catch {
      // A layout this version cannot read leaves the screen as it is.
    }
  }, [layout]);
  // Only the driver changes the screen everyone sees; a watcher follows it.
  const sendLayout = (d: string | undefined) => {
    const next = JSON.stringify({ ...docks, pages: pageOf, driver: d });
    synced.current = next;
    link.threads.setLayout({ threadId, layout: next }).catch(() => {});
  };
  useEffect(() => {
    const next = JSON.stringify({ ...docks, pages: pageOf, driver });
    if (next === synced.current || !thread || !driving) return;
    // Before the thread's own layout arrives, the empty one in hand is not a change to send.
    if (!synced.current && !driver && DOCKS.every((d) => !docks[d].open) && allTabs(docks).length === 0) return;
    const timer = setTimeout(() => {
      synced.current = next;
      link.threads.setLayout({ threadId, layout: next }).catch(() => {});
    }, 200);
    return () => clearTimeout(timer);
  }, [docks, pageOf, driver, driving, link, threadId, thread]);
  // What the server says goes, unless this browser has heard newer since asking. A driver who left the
  // thread, closed the page or went quiet long enough for presence to forget them (half a minute) holds nothing.
  useEffect(() => {
    if (seat && seat.sentAt >= learned.current) setDriver(seat.driver || undefined);
  }, [seat]);
  // A free seat goes by itself only to a browser alone on the thread; with others watching, the first to ask gets it.
  const loaded = Boolean(thread);
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
        <Button size="sm" variant="outline" onClick={() => (setLoadError(undefined), void refresh())}>
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
        return <FilesPanel link={link} threadId={threadId} root={fileRoot(thread, projectName)} onClose={() => closeTab(tab)} readOnly={!driving} />;
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

  const sideChat = side && <SideChat key={side.id} link={link} threadId={side.id} aside={side.aside} providers={providers} mentions={modules.has("files")} stealth={stealth} onOpen={onOpen} onSide={(id, aside) => setSide({ id, aside })} onClose={() => setSide(undefined)} />;
  const main = (
      <div className="@container relative flex h-full min-h-0 min-w-0 flex-1 flex-col">
        {/* While a tab is dragged, each closed dock offers a place on the conversation to drop it. */}
        {dragging &&
          DOCKS.filter((d) => !docks[d].open || (isMobile && d === "right")).map((d) => (
            <div
              key={d}
              className={cn("border-primary/60 bg-primary/10 text-primary absolute z-20 flex items-center justify-center rounded-lg border-2 border-dashed text-xs font-medium", dropZone[d])}
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
        {seat && !driving && (
          <div className="pointer-events-none absolute inset-x-0 top-14 z-10 flex justify-center px-3">
            <div role="status" className="pointer-events-auto flex max-w-full items-center gap-2 rounded-2xl border border-amber-500/40 bg-amber-100/95 py-1 pr-1 pl-3 text-sm text-amber-950 shadow-sm dark:bg-amber-950/90 dark:text-amber-100">
              <EyeIcon className="size-4 shrink-0" />
              <span className="py-0.5">{!driver ? t("drive.bannerFree") : holder ? t("drive.bannerOther", { name: holder.name }) : t("drive.bannerOwnTab")}</span>
              {mayTake && (
                <Button size="xs" variant="outline" className="border-amber-500/50 bg-transparent" onClick={() => void askToTake(driver ?? "")}>
                  {t(driver ? "drive.take" : "drive.claim")}
                </Button>
              )}
            </div>
          </div>
        )}
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
          {thread.status === ThreadStatus.RUNNING || switching ? (
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
          {(busy || threadFailed(thread)) && <span className={cn("hidden min-w-0 flex-1 basis-0 truncate text-xs @2xl:inline", busy ? "text-muted-foreground" : "text-destructive")}>{threadState(thread, t)}</span>}
          <div className="ml-auto flex shrink-0 items-center gap-2">
            {/* Who else has this thread open. */}
            <PeopleStack people={here} label={driving ? t("drive.watchers") : undefined} />
            {git?.repository && (
              <Button size="sm" variant={changesTab ? "secondary" : "outline"} title={t("git.changes")} disabled={!driving} onClick={() => (changesTab ? closeTab(changesTab) : openSurface("right", "changes"))}>
                <GitBranchIcon data-icon="inline-start" />
                <span className="hidden max-w-32 truncate @3xl:inline">{git.branch}</span>
                {git.files.length > 0 && <LineCounts additions={git.additions} deletions={git.deletions} />}
              </Button>
            )}
            <Button size="icon-sm" variant={info ? "secondary" : "ghost"} aria-pressed={info} aria-label={t("thread.info")} title={t("thread.info")} onClick={() => setInfo((v) => !v)}>
              <PanelTopIcon />
            </Button>
            {(["bottom", "right"] as const).map((dock) => {
              const Icon = dock === "bottom" ? PanelBottomIcon : PanelRightIcon;
              return (
                <Button key={dock} size="icon-sm" variant={docks[dock].open ? "secondary" : "ghost"} aria-pressed={docks[dock].open} aria-label={t(`dock.${dock}`)} title={t(`dock.${dock}`)} disabled={!driving} onClick={() => toggleDock(dock)}>
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
        {terminal ? (
          <TerminalView key={terminalRun} link={link} threadId={threadId} driving={driving} onExit={() => setExited(true)} />
        ) : (
          // A wrapper that is always there, so opening the side chat never remounts the conversation.
          <div className="flex min-h-0 flex-1">
            <div className="flex min-w-0 flex-1 flex-col">
              <Chat link={link} thread={thread} busy={busy} refresh={refresh} onStatus={loadGit} onOpen={onOpen} mentions={modules.has("files")} provider={provider} providers={providers} locked={!driving} onTurns={setTurns} onTurnChanges={showTurn} onSide={(id, aside) => setSide({ id, aside })} />
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
  const right = docks.right.open && !isMobile;
  return (
    <ResizablePanelGroup id="inaba-thread-side" orientation="horizontal" className="min-h-0 flex-1" defaultLayout={rightLayout.defaultLayout} onLayoutChanged={rightLayout.onLayoutChanged}>
      <ResizablePanel id="main" minSize={360}>
        <ResizablePanelGroup id="inaba-thread-bottom" orientation="vertical" defaultLayout={bottomLayout.defaultLayout} onLayoutChanged={bottomLayout.onLayoutChanged}>
          <ResizablePanel id="top" minSize={200}>
            {main}
          </ResizablePanel>
          {docks.bottom.open && (
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
      <Sheet open={docks.right.open && isMobile} onOpenChange={(open) => !open && toggleDock("right")}>
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
  children: (tab: TabId) => ReactNode;
}) {
  const { t } = useI18n();
  useKeybindings();
  // A dragged tab of this dock's own is reordered; one from another dock is taken in.
  const own = draggingTab !== undefined && tabs.includes(draggingTab);
  // The tab whose right-click menu is open.
  const [menuFor, setMenuFor] = useState<TabId>();
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
          variant="line"
          className="min-w-0 flex-1 justify-start gap-1 overflow-x-auto overflow-y-hidden group-data-horizontal/tabs:h-12"
        >
          {tabs.map((tab) => {
            const Icon = sideTabIcon[surfaceOf(tab)];
            return (
              <TabsTrigger
                key={tab}
                value={tab}
                className="flex-none"
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
                  if (from !== tab) onMove(from, tab);
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
                {t(sideTabTitle[surfaceOf(tab)])}
                {tabNumber(tab) && tabNumber(tab) !== "1" && <span className="text-muted-foreground tabular-nums">{tabNumber(tab)}</span>}
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
  const [driving, setDriving] = useState(false);
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
  const locked = !useSideSeat(link.hostId, threadId, stealth);
  const [thread, setThread] = useState<Thread>();
  // A side chat deleted elsewhere closes instead of showing an error forever.
  const refresh = useCallback(() => link.threads.getThread({ threadId }).then(setThread, (e) => (showError(e), onClose())), [link, threadId]); // eslint-disable-line react-hooks/exhaustive-deps
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
          locked={locked}
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
}) {
  const { t } = useI18n();
  const threadId = thread.id;
  const names = useMemo(() => Object.fromEntries(providers.map((p) => [p.id, p.displayName || p.id])), [providers]);
  // The message the user asked to rewind to, while they confirm it.
  const [rewinding, setRewinding] = useState<Extract<Item, { type: "user" }>>();
  // What a rewind hands back to the composer.
  const [draft, setDraft] = useState<Draft>();
  const [log, dispatch] = useReducer(applyAll, { items: [], lastSeq: 0n });
  // Messages on their way to the host, shown at once rather than when the host has them: one
  // with a screenshot can take a while over the relay.
  const [sending, setSending] = useState<{ key: number; text: string; attachments: Attachment[] }[]>([]);
  const sendKey = useRef(0);
  // The turns that changed files, for the changes panel to offer; only a change in them is news.
  const turnKey = log.items.filter((it) => it.type === "changes").map((it) => (it as TurnScope).after).join();
  useEffect(() => {
    onTurns?.(log.items.filter((it): it is Extract<Item, { type: "changes" }> => it.type === "changes"));
  }, [turnKey]); // eslint-disable-line react-hooks/exhaustive-deps
  const lastSeq = useRef(0n);
  // How far the log went when the thread was opened: the replay up to there is drawn at once, not as it streams in.
  const opened = useRef(thread.lastSeq);
  // How much of that replay has arrived, for the loading screen.
  const [received, setReceived] = useState(0n);
  useMarkSeen(link.hostId, threadId, log.lastSeq);
  const readWorkflowAgent = useCallback(
    (toolCallId: string, agentId: string, signal: AbortSignal) => link.threads.watchWorkflowAgent({ threadId, toolCallId, agentId }, { signal }),
    [link, threadId],
  );
  const bottom = useRef<HTMLDivElement>(null);
  const direct = link.direct;

  // Follow the event log, resuming after the last event seen whenever the stream
  // breaks or the link moves between the relay and the direct connection.
  useEffect(() => {
    const abort = new AbortController();
    // A replay brings hundreds of these at once: one read of the thread at a time, and one more after it for what came meanwhile.
    let reading = false;
    let again = false;
    const reread = () => {
      if (reading) return void (again = true);
      reading = true;
      void refresh().finally(() => {
        reading = false;
        if (again) (again = false), reread();
      });
    };
    // Events are applied a frame's worth at a time: one render each made a replay freeze the tab.
    // A timer rather than a frame callback, so a hidden tab still applies them and marks the thread read.
    let buffer: Event[] = [];
    let timer: ReturnType<typeof setTimeout> | undefined;
    let progress: ReturnType<typeof setTimeout> | undefined;
    const flush = () => {
      timer = undefined;
      const events = buffer;
      buffer = [];
      dispatch(events);
      if (events.some((e) => threadEvents.has(e.payload.case ?? ""))) reread();
    };
    void (async () => {
      while (!abort.signal.aborted) {
        try {
          for await (const event of link.threads.subscribe({ threadId, afterSeq: lastSeq.current }, { signal: abort.signal })) {
            lastSeq.current = event.seq;
            buffer.push(event);
            if (event.seq < opened.current) {
              progress ??= setTimeout(() => ((progress = undefined), setReceived(lastSeq.current)), 100);
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
  }, [link, threadId, direct, refresh]);

  // Follow new events only while the user is at the bottom; scrolled up, they are reading, so offer a jump instead.
  const near = useRef(true);
  const [behind, setBehind] = useState(false);
  useEffect(() => {
    if (near.current) bottom.current?.scrollIntoView({ block: "end" });
    else setBehind(true);
  }, [log, sending]);

  // A message shows as sending until the host has it; if it never gets there, it goes back to the composer.
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

  return (
    <div ref={chat} className="contents" onPointerEnter={() => (hovered.current = true)} onPointerLeave={() => (hovered.current = false)}>
      <style>{`::highlight(inaba-find){background-color:color-mix(in oklab,var(--warning) 35%,transparent)}::highlight(inaba-find-current){background-color:var(--warning);color:var(--warning-foreground)}.dark ::highlight(inaba-find-current){color:var(--background)}`}</style>
      {/* --warning is bright yellow in both themes, but dark's --warning-foreground is yellow too, so the current match takes dark's near-black background as its text. */}
      <div className="relative flex min-h-0 flex-1 flex-col">
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
        data-chat-scroll
        tabIndex={-1}
        className="min-h-0 flex-1 overflow-y-auto outline-none"
        onScroll={(e) => {
          const el = e.currentTarget;
          near.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
          if (near.current) setBehind(false);
          setQuoting(undefined);
        }}
      >
        <WorkflowReader value={readWorkflowAgent}>
          <div ref={content} className="mx-auto flex max-w-3xl flex-col gap-3 px-4 py-6">
            {log.items.length === 0 &&
              (log.lastSeq < thread.lastSeq ? (
                <div className="text-muted-foreground mx-auto my-16 flex flex-col items-center gap-2 text-sm" role="status">
                  <Spinner className="size-6" />
                  {t("chat.loading")}
                  {received > 0n && <span className="text-xs tabular-nums">{Number((received * 100n) / thread.lastSeq)}%</span>}
                </div>
              ) : (
                <p className="text-muted-foreground py-16 text-center text-sm">{t("chat.empty")}</p>
              ))}
            {blocks(log.items).map((block, i) =>
              "work" in block ? (
                <WorkLog key={i} turn={block.turn} work={block.work} />
              ) : (
                <ItemView
                  key={i}
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
            {sending.map((m) => (
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
        {/* What is queued, and the composer, are the driver's to change. */}
        <fieldset ref={composer} disabled={locked} className="contents">
        <QueueList
          queue={thread.queue}
          paused={thread.queuePaused}
          onEdit={(queuedId, text) => link.threads.editQueued({ threadId, queuedId, text }).then(refresh)}
          onMove={(queuedId, toIndex) => run(link.threads.moveQueued({ threadId, queuedId, toIndex }))}
          onSendNow={(queuedId) => run(link.threads.sendQueuedNow({ threadId, queuedId }))}
          onRemove={(queuedId) => run(link.threads.cancelQueued({ threadId, queuedId }))}
          onResume={() => run(link.threads.resumeQueue({ threadId }))}
        />
        <Composer
          link={link}
          thread={thread}
          busy={busy}
          draft={draft}
          onSend={send}
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

// Events that change the thread itself, not only its transcript.
const threadEvents = new Set([
  "layoutChanged",
  "threadTitle",
  "userMessage",
  "toolCall",
  "optionsChanged",
  "turnStarted",
  "turnEnded",
  "modeChanged",
  "permissionRequest",
  "permissionResolved",
  "commandsChanged",
  "usageChanged",
  "rewound",
  "providerChanged",
]);


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
  function WorkLog({ turn, work }: { turn: Turn; work: Item[] }) {
    const { t } = useI18n();
    const running = turn.endedAt === undefined;
    const duration = formatDuration((turn.endedAt ?? Date.now()) - turn.startedAt);
    return (
      // Open while the agent works, so its steps show as they happen; folded once it has answered.
      <details className="group/work border-border/60 border-b pb-2" open={running}>
        <summary className="text-muted-foreground flex cursor-pointer list-none items-center gap-1.5 text-sm tabular-nums [&::-webkit-details-marker]:hidden">
          {running && <Spinner />}
          {running ? t("chat.working") : t("chat.workedFor", { duration })}
          {work.length > 0 && <span className="text-xs">· {t("chat.steps", { count: String(work.length) })}</span>}
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
  (a, b) => a.turn === b.turn && a.work.length === b.work.length && a.work.every((it, i) => it === b.work[i]),
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
        <div className="group/message flex max-w-[85%] flex-col items-end gap-1 self-end">
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
