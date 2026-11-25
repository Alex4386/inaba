import {
  ArchiveIcon,
  ArchiveRestoreIcon,
  BellIcon,
  BellOffIcon,
  BotIcon,
  ChevronRightIcon,
  CircleAlertIcon,
  CircleHelpIcon,
  CircleDotIcon,
  CopyIcon,
  FolderOpenIcon,
  GitForkIcon,
  ListIcon,
  MoreHorizontalIcon,
  PencilIcon,
  PinIcon,
  PinOffIcon,
  PlusIcon,
  SettingsIcon,
  SquareTerminalIcon,
  Trash2Icon,
} from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import type { HostLink } from "@/api";
import { ConfirmDialog, RenameDialog } from "@/components/dialogs";
import { PeopleStack, usePeople } from "@/components/presence";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuSub, DropdownMenuSubContent, DropdownMenuSubTrigger, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import {
  SidebarGroup,
  SidebarGroupAction,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuAction,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  useSidebar,
} from "@/components/ui/sidebar";
import { Spinner } from "@/components/ui/spinner";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { SnoozeKind } from "@/gen/inaba/v1/server_pb";
import { type Project, type Thread, ThreadMode, ThreadStatus } from "@/gen/inaba/v1/host_pb";
import { showError } from "@/lib/errors";
import { notifyEnabled } from "@/lib/settings";
import { type Translate, useI18n } from "@/lib/i18n";
import { useSeen } from "@/lib/seen";
import { type Muted, type SnoozePreset, type Snoozes, snoozePresets, useSnoozes } from "@/lib/snooze";
import { cn } from "@/lib/utils";

/** What to call a thread in lists and headers. */
export function threadName(thread: Thread): string {
  return thread.title || thread.provider;
}

/** A project's icon: its initial on a color picked from its id, so it stays the same everywhere. */
export function ProjectIcon({ project, className }: { project: Project; className?: string }) {
  let hash = 0;
  for (const ch of project.id) hash = (hash * 31 + ch.charCodeAt(0)) % 360;
  return (
    <span
      aria-hidden="true"
      className={cn("flex size-5 shrink-0 items-center justify-center overflow-hidden rounded-md bg-cover bg-center text-[0.65rem] font-semibold text-white", className)}
      // A picture of its own, or a color made from its id behind its initial.
      style={project.icon ? { backgroundImage: `url("${project.icon}")` } : { backgroundColor: `oklch(0.55 0.13 ${hash})` }}
    >
      {/* Inside a project row the initial gives way to the fold arrow while the row is hovered or focused. */}
      <span className={cn("group-focus-within/project:hidden group-hover/project:hidden", project.icon && "invisible")}>{[...project.name][0]?.toUpperCase() ?? "?"}</span>
      <ChevronRightIcon className="hidden size-3.5 transition-transform group-focus-within/project:block group-hover/project:block group-data-open/project:rotate-90" />
    </span>
  );
}

/** How long ago, in the reader's language: "3 min. ago", "3분 전". */
export function timeAgo(date: Date, locale: string, justNow: string): string {
  const seconds = Math.round((date.getTime() - Date.now()) / 1000);
  const format = new Intl.RelativeTimeFormat(locale, { numeric: "auto", style: "narrow" });
  const steps: [Intl.RelativeTimeFormatUnit, number][] = [
    ["day", 86_400],
    ["hour", 3_600],
    ["minute", 60],
  ];
  for (const [unit, size] of steps) if (Math.abs(seconds) >= size) return format.format(Math.trunc(seconds / size), unit);
  return justNow;
}

/** Whether the thread's last turn ended in an error; a new turn clears it. */
export const threadFailed = (thread: Thread) => thread.status === ThreadStatus.IDLE && thread.lastTurnFailed;

/** One line on what a thread is doing now, or how it was left. */
export function threadState(thread: Thread, t: Translate): string {
  if (thread.status === ThreadStatus.WAITING) return t("thread.asks");
  if (thread.status === ThreadStatus.RUNNING) return thread.activity || t("thread.working");
  if (threadFailed(thread)) return t("thread.failed");
  if (thread.mode === ThreadMode.TERMINAL) return t("thread.inTerminal");
  return thread.activitySeq > 0n ? t("thread.done") : t("thread.notStarted");
}

const presetKey = { hour: "snooze.hour", tomorrow: "snooze.tomorrow", week: "snooze.week", forever: "snooze.forever" } as const;
// A weekday alone reads as this week, so a wake time past a day also says the date.
const wakeTime = (until: Date, locale: string) =>
  until.toLocaleString(locale, { ...(until.getTime() - Date.now() > 86_400_000 && { month: "short", day: "numeric" }), weekday: "short", hour: "2-digit", minute: "2-digit" });

/** A small muted bell after a snoozed name, saying until when. */
function SnoozedMark({ muted }: { muted?: Muted }) {
  const { t, locale } = useI18n();
  if (!muted) return null;
  const label = muted.until ? t("snooze.on", { time: wakeTime(muted.until, locale) }) : t("snooze.onForever");
  return (
    <Tooltip>
      <TooltipTrigger render={<span className="text-muted-foreground flex shrink-0" aria-label={label} />}>
        <BellOffIcon className="size-3" />
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

/** The snooze part of a thread's or project's menu: the presets, or unsnoozing what is snoozed. */
function SnoozeItems({ own, byProject, onSet, onClear }: { own?: Muted; byProject?: Muted; onSet: (preset: SnoozePreset) => void; onClear: () => void }) {
  const { t, locale } = useI18n();
  if (own)
    return (
      <DropdownMenuItem onClick={onClear}>
        <BellIcon />
        {own.until ? t("snooze.off", { time: wakeTime(own.until, locale) }) : t("snooze.offForever")}
      </DropdownMenuItem>
    );
  if (byProject)
    return (
      <DropdownMenuItem disabled>
        <BellOffIcon />
        {t("snooze.byProject")}
      </DropdownMenuItem>
    );
  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger openOnHover={false}>
        <BellOffIcon />
        {t("snooze.menu")}
      </DropdownMenuSubTrigger>
      <DropdownMenuSubContent>
        {snoozePresets.map((p) => (
          <DropdownMenuItem key={p} onClick={() => onSet(p)}>
            {t(presetKey[p])}
          </DropdownMenuItem>
        ))}
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  );
}

/** What can be done to a thread, from its … or a right-click. */
type ThreadActions = {
  onNewThread?: () => void;
  onPin: (pinned: boolean) => void;
  onArchive: (archived: boolean) => void;
  onRename: () => void;
  onFork: () => void;
  onEnd: () => void;
  onSnooze: (preset: SnoozePreset) => void;
  onUnsnooze: () => void;
  /** Absent when the thread already reads as unread or has nothing to read. */
  onMarkUnread?: () => void;
};

function ThreadRow({
  thread,
  active,
  unread,
  onSelect,
  actions,
  snoozed,
  byProject,
}: {
  thread: Thread;
  active: boolean;
  unread: boolean;
  onSelect: () => void;
  actions: ThreadActions;
  snoozed?: Muted;
  byProject?: Muted;
}) {
  const { t, locale } = useI18n();
  const people = usePeople();
  const [menu, setMenu] = useState(false);
  const running = thread.status === ThreadStatus.RUNNING;
  const waiting = thread.status === ThreadStatus.WAITING;
  const failed = threadFailed(thread);
  const pinned = Boolean(thread.labels.pinned);
  const archived = Boolean(thread.labels.archived);
  const copy =(text: string) => navigator.clipboard.writeText(text).then(() => toast.success(t("common.copied")), showError);
  return (
    <SidebarMenuSubItem
      className="group/thread relative"
      onContextMenu={(e) => {
        e.preventDefault();
        setMenu(true);
      }}
    >
      <SidebarMenuSubButton render={<button type="button" />} className="h-auto w-full items-start py-1.5 pr-7 text-left" isActive={active} onClick={onSelect}>
        <span className="mt-0.5 shrink-0">
          {running ? (
            <Spinner />
          ) : waiting ? (
            <CircleHelpIcon className="text-info size-4" />
          ) : failed ? (
            <CircleAlertIcon className="text-destructive size-4" />
          ) : thread.mode === ThreadMode.TERMINAL ? (
            <SquareTerminalIcon className="size-4" />
          ) : (
            <BotIcon className="size-4" />
          )}
        </span>
        <span className="grid min-w-0 flex-1">
          <span className="flex items-center gap-1">
            <span className={cn("truncate", unread && "text-sidebar-foreground font-semibold")}>{threadName(thread)}</span>
            {unread && <span className="bg-primary size-1.5 shrink-0 rounded-full" role="img" aria-label={t("thread.unread")} />}
            {pinned && <PinIcon className="text-muted-foreground size-3 shrink-0" aria-label={t("thread.pinned")} />}
            {archived && <ArchiveIcon className="text-muted-foreground size-3 shrink-0" aria-label={t("archive.archived")} />}
            <SnoozedMark muted={snoozed} />
            <PeopleStack className="ml-auto" size="xs" people={people.filter((p) => p.threadId === thread.id)} />
          </span>
          <span className={cn("truncate text-xs", waiting ? "text-info font-medium" : failed ? "text-destructive" : "text-muted-foreground")}>
            {threadState(thread, t)}
            {!running && !waiting && thread.updatedAt && ` · ${timeAgo(new Date(Number(thread.updatedAt.seconds) * 1000), locale, t("common.justNow"))}`}
          </span>
        </span>
      </SidebarMenuSubButton>
      <DropdownMenu open={menu} onOpenChange={setMenu}>
        {/* Shown on hover, while open, or for the thread in view; a right-click opens the same menu. */}
        <DropdownMenuTrigger
          render={
            <button
              type="button"
              className={cn(
                "text-sidebar-foreground hover:bg-sidebar-accent absolute top-1.5 right-1 flex size-5 items-center justify-center rounded-md opacity-0 group-hover/thread:opacity-100 focus-visible:opacity-100 aria-expanded:opacity-100",
                active && "opacity-100",
              )}
            />
          }
        >
          <MoreHorizontalIcon className="size-4" />
          <span className="sr-only">{t("project.more", { name: threadName(thread) })}</span>
        </DropdownMenuTrigger>
        <DropdownMenuContent className="w-auto min-w-48 whitespace-nowrap" side="right" align="start">
          {actions.onNewThread && (
            <DropdownMenuItem onClick={actions.onNewThread}>
              <PlusIcon />
              {t("thread.new")}
            </DropdownMenuItem>
          )}
          <DropdownMenuItem onClick={() => actions.onPin(!pinned)}>
            {pinned ? <PinOffIcon /> : <PinIcon />}
            {t(pinned ? "thread.unpin" : "thread.pin")}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => actions.onArchive(!archived)}>
            {archived ? <ArchiveRestoreIcon /> : <ArchiveIcon />}
            {t(archived ? "archive.unarchiveThread" : "archive.archiveThread")}
          </DropdownMenuItem>
          <SnoozeItems own={snoozed} byProject={byProject} onSet={actions.onSnooze} onClear={actions.onUnsnooze} />
          {actions.onMarkUnread && (
            <DropdownMenuItem onClick={actions.onMarkUnread}>
              <CircleDotIcon />
              {t("thread.markUnread")}
            </DropdownMenuItem>
          )}
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={actions.onRename}>
            <PencilIcon />
            {t("thread.rename")}
          </DropdownMenuItem>
          <DropdownMenuItem disabled={running} onClick={actions.onFork}>
            <GitForkIcon />
            {t("thread.fork")}
          </DropdownMenuItem>
          <DropdownMenuSub>
            <DropdownMenuSubTrigger openOnHover={false}>
              <CopyIcon />
              {t("common.copy")}
            </DropdownMenuSubTrigger>
            <DropdownMenuSubContent>
              <DropdownMenuItem onClick={() => copy(thread.id)}>{t("thread.copyId")}</DropdownMenuItem>
              <DropdownMenuItem onClick={() => copy(thread.cwd)}>{t("thread.copyPath")}</DropdownMenuItem>
              {thread.nativeSessionId && <DropdownMenuItem onClick={() => copy(thread.nativeSessionId)}>{t("thread.copyNative")}</DropdownMenuItem>}
            </DropdownMenuSubContent>
          </DropdownMenuSub>
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" onClick={actions.onEnd}>
            <Trash2Icon />
            {t("thread.end")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </SidebarMenuSubItem>
  );
}

/** The host's projects, each with its threads under it. */
export function ProjectTree({
  link,
  selected,
  onSelect,
  onNewThread,
  onAddProject,
  canManage,
  openProject,
  onProjectSettings,
  onAllProjects,
  onProjectThreads,
}: {
  /** Opens the page listing every project, archived ones too. */
  onAllProjects: () => void;
  /** Opens the page listing every thread of a project, archived ones too. */
  onProjectThreads: (project: Project) => void;
  /** Opens a project's settings; absent when the account may not change them. */
  onProjectSettings?: (project: Project) => void;
  link: HostLink;
  selected?: string;
  /** The project whose own page is showing. */
  openProject?: string;
  onSelect: (id: string | undefined) => void;
  /** Opens the screen that starts a thread in the project. */
  onNewThread: (project: Project) => void;
  /** Opens the folder picker that adds a project. */
  onAddProject: () => void;
  /** Whether the account may add and remove projects. */
  canManage: boolean;
}) {
  const { t } = useI18n();
  const { setOpenMobile } = useSidebar();
  const people = usePeople();
  const [renaming, setRenaming] = useState<Thread>();
  const [ending, setEnding] = useState<Thread>();
  // What a thread's menu does; a thread outside any project has no project to start another in.
  const actionsFor = (th: Thread, project?: Project): ThreadActions => ({
    onNewThread: project
      ? () => {
          setOpenMobile(false);
          onNewThread(project);
        }
      : undefined,
    onPin: (pinned) => link.threads.updateThread({ threadId: th.id, pinned }).then(refresh, showError),
    onArchive: (archived) => link.threads.updateThread({ threadId: th.id, archived }).then(refresh, showError),
    onRename: () => setRenaming(th),
    onFork: () => link.threads.forkThread({ threadId: th.id, seq: th.lastSeq }).then((f) => (void refresh(), onSelect(f.id)), showError),
    onEnd: () => setEnding(th),
    onSnooze: (preset) => snoozes.set(SnoozeKind.THREAD, th.id, preset),
    onUnsnooze: () => snoozes.clear(SnoozeKind.THREAD, th.id),
    onMarkUnread: th.activitySeq > 0n && !seen.unread(th) ? () => seen.markUnread(th) : undefined,
  });
  // Projects folded shut; every other one shows its threads.
  const [folded, setFolded] = useState<Set<string>>(new Set());
  const selectedRef = useRef(selected);
  selectedRef.current = selected;
  const [projects, setProjects] = useState<Project[]>();
  const [threads, setThreads] = useState<Thread[]>([]);
  const statuses = useRef(new Map<string, ThreadStatus>());
  const [removing, setRemoving] = useState<Project>();
  const snoozes = useSnoozes(link.hostId);
  const seen = useSeen(link.hostId);
  // Read by the poll below without restarting it.
  const snoozesRef = useRef<Snoozes>(snoozes);
  snoozesRef.current = snoozes;

  const refresh = useCallback(
    () =>
      Promise.all([link.threads.listThreads({}), link.projects.listProjects({})]).then(
        ([th, pr]) => {
          // Say so when a thread the user is not looking at finishes or starts waiting on them.
          for (const thread of th.threads) {
            const before = statuses.current.get(thread.id);
            statuses.current.set(thread.id, thread.status);
            if (before === undefined || before === thread.status || thread.id === selectedRef.current || !notifyEnabled()) continue;
            // A snooze quiets a finished turn; a question still gets through, as with push.
            if (thread.status === ThreadStatus.WAITING) toast.info(t("notify.asks", { name: threadName(thread) }));
            else if (thread.status === ThreadStatus.IDLE && !snoozesRef.current.muted(thread.id, thread.projectId)) toast.success(t("notify.done", { name: threadName(thread) }));
          }
          setThreads(th.threads);
          setProjects(pr.projects);
        },
        () => {},
      ),
    [link, t],
  );
  useEffect(() => {
    void refresh();
    const timer = setInterval(refresh, 3_000);
    return () => clearInterval(timer);
  }, [link, refresh]);

  // Archived projects and threads are left out of the sidebar; the all-projects and all-threads pages list them.
  const shown = (th: Thread) => !th.labels.archived;
  const known = new Set(projects?.map((p) => p.id));
  // Threads started outside any project, or whose project was removed.
  const floating = threads.filter((th) => !known.has(th.projectId) && !th.labels.archived);
  // Pinned projects first, for everyone; otherwise as the host lists them.
  const listed = projects?.filter((p) => !p.archived).sort((a, b) => Number(b.pinned) - Number(a.pinned));

  return (
    <>
      <SidebarGroup>
        <SidebarGroupLabel>{t("project.projects")}</SidebarGroupLabel>
        {canManage && (
          <SidebarGroupAction title={t("project.add")} onClick={() => {
              setOpenMobile(false);
              onAddProject();
            }}>
            <PlusIcon />
            <span className="sr-only">{t("project.add")}</span>
          </SidebarGroupAction>
        )}
        <SidebarMenu>
          {projects?.length === 0 && <li className="text-muted-foreground px-2 py-1.5 text-sm group-data-[collapsible=icon]:hidden">{t(canManage ? "project.none" : "project.noneNoPermission")}</li>}
          {listed?.map((project) => {
            const listedThreads = threads.filter((th) => th.projectId === project.id && shown(th));
            return (
              <Collapsible key={project.id} open={!folded.has(project.id)} className="group/project" render={<SidebarMenuItem />}>
                {/* The project's row stays at the top while its threads scroll under it. */}
                <div className="bg-sidebar sticky top-0 z-10">
                {/* A project opens its own page first; clicked again there, it folds or unfolds its threads. */}
                <CollapsibleTrigger
                  render={<SidebarMenuButton tooltip={project.name} isActive={openProject === project.id} className="pr-14" />}
                  onClick={() => {
                    if (openProject !== project.id) {
                      setOpenMobile(false);
                      onNewThread(project);
                      setFolded((f) => (f.has(project.id) ? new Set([...f].filter((x) => x !== project.id)) : f));
                    } else
                      setFolded((f) => {
                        const next = new Set(f);
                        if (!next.delete(project.id)) next.add(project.id);
                        return next;
                      });
                  }}
                >
                  <ProjectIcon project={project} />
                  <span className={cn("truncate font-medium", project.archived && "text-muted-foreground")}>{project.name}</span>
                  {project.pinned && <PinIcon className="text-muted-foreground size-3 shrink-0" aria-label={t("thread.pinned")} />}
                  {project.archived && <ArchiveIcon className="text-muted-foreground size-3 shrink-0" aria-label={t("archive.archived")} />}
                  <SnoozedMark muted={snoozes.project(project.id)} />
                  {/* Who has the project's own page open. */}
                  <PeopleStack size="xs" people={people.filter((p) => p.projectId === project.id && !p.threadId)} />
                  {listedThreads.some((th) => th.status === ThreadStatus.RUNNING) && <Spinner className="group-data-open/project:hidden" />}
                </CollapsibleTrigger>
                {/* One menu, shown on hover or while open, holds what a project offers. */}
                <DropdownMenu>
                  <DropdownMenuTrigger render={<SidebarMenuAction showOnHover className="aria-expanded:bg-muted" />}>
                    <MoreHorizontalIcon />
                    <span className="sr-only">{t("project.more", { name: project.name })}</span>
                  </DropdownMenuTrigger>
                  {/* Sized to its items: the trigger it hangs from is only an icon wide. */}
                  <DropdownMenuContent className="w-auto whitespace-nowrap" side="right" align="start">
                    <DropdownMenuItem
                      onClick={() => {
                        setOpenMobile(false);
                        onNewThread(project);
                      }}
                    >
                      <PlusIcon />
                      {t("thread.new")}
                    </DropdownMenuItem>
                    <DropdownMenuItem
                      onClick={() => {
                        setOpenMobile(false);
                        onProjectThreads(project);
                      }}
                    >
                      <ListIcon />
                      {t("archive.allThreads")}
                    </DropdownMenuItem>
                    <SnoozeItems
                      own={snoozes.project(project.id)}
                      onSet={(preset) => snoozes.set(SnoozeKind.PROJECT, project.id, preset)}
                      onClear={() => snoozes.clear(SnoozeKind.PROJECT, project.id)}
                    />
                    {onProjectSettings && (
                      <DropdownMenuItem onClick={() => onProjectSettings(project)}>
                        <SettingsIcon />
                        {t("projectSettings.open")}
                      </DropdownMenuItem>
                    )}
                    {canManage && (
                      <>
                        <DropdownMenuItem onClick={() => link.projects.updateProject({ projectId: project.id, pinned: !project.pinned }).then(refresh, showError)}>
                          {project.pinned ? <PinOffIcon /> : <PinIcon />}
                          {t(project.pinned ? "project.unpin" : "project.pin")}
                        </DropdownMenuItem>
                        <DropdownMenuItem onClick={() => link.projects.updateProject({ projectId: project.id, archived: !project.archived }).then(refresh, showError)}>
                          {project.archived ? <ArchiveRestoreIcon /> : <ArchiveIcon />}
                          {t(project.archived ? "archive.unarchiveProject" : "archive.archiveProject")}
                        </DropdownMenuItem>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem variant="destructive" onClick={() => setRemoving(project)}>
                          <Trash2Icon />
                          {t("project.remove")}
                        </DropdownMenuItem>
                      </>
                    )}
                  </DropdownMenuContent>
                </DropdownMenu>
                </div>
                <CollapsibleContent>
                  {/* Indented on the left only, so a thread keeps the full width to the right. */}
                  <SidebarMenuSub className="mr-0 pr-0">
                    {listedThreads.length === 0 && <li className="text-muted-foreground px-2 py-1 text-xs">{t("project.noThreads")}</li>}
                    {/* Pinned threads first, for everyone; otherwise as the host lists them. */}
                    {[...listedThreads.filter((th) => th.labels.pinned), ...listedThreads.filter((th) => !th.labels.pinned)].map((th) => (
                      <ThreadRow
                        key={th.id}
                        thread={th}
                        active={th.id === selected}
                        // The thread in view marks itself seen; its own row stays plain meanwhile.
                        unread={th.id !== selected && seen.unread(th)}
                        onSelect={() => {
                          setOpenMobile(false);
                          onSelect(th.id);
                        }}
                        actions={actionsFor(th, project)}
                        snoozed={snoozes.thread(th.id)}
                        byProject={snoozes.project(project.id)}
                      />
                    ))}
                  </SidebarMenuSub>
                </CollapsibleContent>
              </Collapsible>
            );
          })}
        </SidebarMenu>
      </SidebarGroup>
      {floating.length > 0 && (
        <SidebarGroup className="group-data-[collapsible=icon]:hidden">
          <SidebarGroupLabel>{t("thread.other")}</SidebarGroupLabel>
          <SidebarMenuSub className="mx-0 border-l-0 px-0">
            {floating.map((th) => (
              <ThreadRow
                key={th.id}
                thread={th}
                active={th.id === selected}
                unread={th.id !== selected && seen.unread(th)}
                onSelect={() => {
                  setOpenMobile(false);
                  onSelect(th.id);
                }}
                actions={actionsFor(th)}
                snoozed={snoozes.thread(th.id)}
              />
            ))}
          </SidebarMenuSub>
        </SidebarGroup>
      )}
      {/* At the foot of the list: every project, archived ones too, on its own page. */}
      {/* Always in view at the foot, however long the list. */}
      <SidebarGroup className="bg-sidebar sticky bottom-0 z-10 mt-auto group-data-[collapsible=icon]:hidden">
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              size="sm"
              className="text-muted-foreground"
              onClick={() => {
                setOpenMobile(false);
                onAllProjects();
              }}
            >
              <FolderOpenIcon />
              <span>{t("archive.allProjects")}</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarGroup>
      <RenameDialog
        open={renaming !== undefined}
        onClose={() => setRenaming(undefined)}
        title={t("thread.renameTitle")}
        name={renaming ? threadName(renaming) : ""}
        onRename={(title) => link.threads.updateThread({ threadId: renaming!.id, title }).then(refresh)}
      />
      <ConfirmDialog
        open={ending !== undefined}
        onClose={() => setEnding(undefined)}
        title={t("thread.endTitle", { name: ending ? threadName(ending) : "" })}
        description={t("thread.endDescription")}
        cancel={t("project.keep")}
        confirm={t("thread.end")}
        destructive
        onConfirm={() =>
          ending &&
          link.threads.deleteThread({ threadId: ending.id }).then(() => {
            if (ending.id === selected) onSelect(undefined);
            void refresh();
          }, showError)
        }
      />
      <ConfirmDialog
        open={removing !== undefined}
        onClose={() => setRemoving(undefined)}
        title={t("project.removeTitle", { name: removing?.name ?? "" })}
        description={t("project.removeDescription")}
        cancel={t("project.keep")}
        confirm={t("project.remove")}
        destructive
        onConfirm={() => removing && link.projects.deleteProject({ projectId: removing.id }).then(refresh, showError)}
      />
    </>
  );
}
