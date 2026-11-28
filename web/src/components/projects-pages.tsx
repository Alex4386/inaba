import { ArchiveIcon, BotIcon, CircleAlertIcon, MoreHorizontalIcon, PinIcon, PlusIcon, SquareTerminalIcon } from "lucide-react";
import { type ReactNode, useCallback, useEffect, useState } from "react";
import type { HostLink } from "@/api";
import { ProjectMenuItems, ThreadMenuItems, useProjectDialogs, useThreadDialogs } from "@/components/entity-menus";
import { ProjectIcon, SnoozedMark, threadFailed, threadName, threadState, timeAgo } from "@/components/project-tree";
import { Badge } from "@/components/reui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { type Project, type Thread, ThreadMode } from "@/gen/inaba/v1/host_pb";
import { errorText } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { useSnoozes } from "@/lib/snooze";

/** The host's projects and threads, read once for a page that lists them. */
function useListing(link: HostLink) {
  const [projects, setProjects] = useState<Project[]>();
  const [threads, setThreads] = useState<Thread[]>([]);
  const [error, setError] = useState("");
  const load = useCallback(() => {
    setError("");
    Promise.all([link.projects.listProjects({}), link.threads.listThreads({})]).then(
      ([p, th]) => {
        setProjects(p.projects);
        setThreads(th.threads);
      },
      (e) => setError(errorText(e)),
    );
  }, [link]);
  useEffect(load, [load]);
  return { projects, threads, reload: load, failed: error && <LoadError error={error} onRetry={load} /> };
}

/** A row's or the page's ⋯ menu. Clicks in it stay in it, not opening the row it sits on. */
function RowMenu({ label, children }: { label: string; children: ReactNode }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button size="icon-sm" variant="ghost" onClick={(e) => e.stopPropagation()} />}>
        <MoreHorizontalIcon />
        <span className="sr-only">{label}</span>
      </DropdownMenuTrigger>
      <DropdownMenuContent className="w-auto min-w-48 whitespace-nowrap" align="end" onClick={(e) => e.stopPropagation()}>
        {children}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

// The menu's cell stays at the row's right edge, in view however far a narrow screen scrolls the table.
const menuCell = "bg-background sticky right-0 py-0";

/** What a project's menu does on these pages. */
type ProjectActions = {
  onNewThread: (project: Project) => void;
  /** Opens a project's settings; absent when the account may not change them. */
  onProjectSettings?: (project: Project) => void;
  /** Whether the account may pin, archive and remove projects. */
  canManage: boolean;
};

function LoadError({ error, onRetry }: { error: string; onRetry: () => void }) {
  const { t } = useI18n();
  return (
    <div className="grid justify-items-start gap-2">
      <p className="text-destructive text-sm">{error}</p>
      <Button size="sm" variant="outline" onClick={onRetry}>
        {t("common.retry")}
      </Button>
    </div>
  );
}

function PageHeader({ children }: { children: React.ReactNode }) {
  return (
    <header className="flex h-12 shrink-0 items-center gap-2 border-b px-3">
      <SidebarTrigger />
      {children}
    </header>
  );
}

function Pinned() {
  const { t } = useI18n();
  return (
    <Badge size="sm" variant="outline">
      <PinIcon />
      {t("thread.pinned")}
    </Badge>
  );
}

function Archived() {
  const { t } = useI18n();
  return (
    <Badge size="sm" variant="outline">
      <ArchiveIcon />
      {t("archive.archived")}
    </Badge>
  );
}

/** Every project on the host, archived ones too: pick one to see all its threads. */
export function ProjectsPage({
  link,
  onOpen,
  onAddProject,
  onNewThread,
  onProjectSettings,
  canManage,
}: ProjectActions & { link: HostLink; onOpen: (project: Project) => void; /** Opens the folder picker that adds a project; absent when the account may not. */ onAddProject?: () => void }) {
  const { t } = useI18n();
  const { projects, threads, reload, failed } = useListing(link);
  const snoozes = useSnoozes(link.hostId);
  const dialogs = useProjectDialogs({ link, onChanged: reload });
  const [filter, setFilter] = useState("");
  const q = filter.trim().toLowerCase();
  const shown = projects?.filter((p) => !q || p.name.toLowerCase().includes(q) || p.cwd.toLowerCase().includes(q)).sort((a, b) => Number(b.pinned) - Number(a.pinned));
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader>
        <h1 className="text-sm font-medium">{t("project.projects")}</h1>
        {onAddProject && (
          <Button size="sm" variant="outline" className="ml-auto" onClick={onAddProject}>
            <PlusIcon data-icon="inline-start" />
            {t("project.add")}
          </Button>
        )}
      </PageHeader>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto grid w-full max-w-3xl grid-cols-[minmax(0,1fr)] gap-4 p-6">
          <Input className="max-w-64" value={filter} onChange={(e) => setFilter(e.target.value)} placeholder={t("archive.filterProjects")} aria-label={t("archive.filterProjects")} />
          {failed ? (
            failed
          ) : !shown ? (
            <Spinner />
          ) : shown.length === 0 ? (
            <p className="text-muted-foreground text-sm">{t("project.noneNoPermission")}</p>
          ) : (
            <div className="overflow-hidden rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("listing.project")}</TableHead>
                    <TableHead>{t("listing.folder")}</TableHead>
                    <TableHead className="text-right">{t("listing.threads")}</TableHead>
                    <TableHead className="w-0" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {shown.map((p) => (
                    <TableRow key={p.id} className="cursor-pointer" onClick={() => onOpen(p)}>
                      <TableCell>
                        <span className="flex items-center gap-2 font-medium">
                          <ProjectIcon project={p} />
                          <span className="truncate">{p.name}</span>
                          {p.pinned && <Pinned />}
                          {p.archived && <Archived />}
                          <SnoozedMark muted={snoozes.project(p.id)} />
                        </span>
                      </TableCell>
                      <TableCell className="text-muted-foreground max-w-64 truncate">{p.cwd}</TableCell>
                      <TableCell className="text-muted-foreground text-right tabular-nums">{threads.filter((th) => th.projectId === p.id).length}</TableCell>
                      <TableCell className={menuCell}>
                        <RowMenu label={t("project.more", { name: p.name })}>
                          <ProjectMenuItems
                            link={link}
                            project={p}
                            canManage={canManage}
                            onChanged={reload}
                            onNewThread={() => onNewThread(p)}
                            onThreads={() => onOpen(p)}
                            onSettings={onProjectSettings && (() => onProjectSettings(p))}
                            onRemove={() => dialogs.remove(p)}
                          />
                        </RowMenu>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </div>
      </div>
      {dialogs.dialogs}
    </div>
  );
}

/** Every thread of one project, archived ones too, newest first. */
export function ProjectThreadsPage({
  link,
  projectId,
  onProjects,
  onOpen,
  onNewThread,
  onProjectSettings,
  onThreadSettings,
  canManage,
}: ProjectActions & {
  link: HostLink;
  projectId: string;
  onProjects: () => void;
  onOpen: (threadId: string) => void;
  /** Opens a thread with its settings showing. */
  onThreadSettings: (threadId: string) => void;
}) {
  const { t, locale } = useI18n();
  const { projects, threads, reload, failed } = useListing(link);
  const snoozes = useSnoozes(link.hostId);
  const threadDialogs = useThreadDialogs({ link, onChanged: reload });
  const projectDialogs = useProjectDialogs({ link, onChanged: reload, onRemoved: onProjects });
  const [filter, setFilter] = useState("");
  const project = projects?.find((p) => p.id === projectId);
  const q = filter.trim().toLowerCase();
  const listed = threads
    .filter((th) => th.projectId === projectId && (!q || threadName(th).toLowerCase().includes(q)))
    // Pinned first, as in the sidebar; then newest first.
    .sort((a, b) => Number(Boolean(b.labels.pinned)) - Number(Boolean(a.labels.pinned)) || Number((b.updatedAt?.seconds ?? 0n) - (a.updatedAt?.seconds ?? 0n)));
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader>
        <button type="button" className="text-muted-foreground hover:text-foreground text-sm" onClick={onProjects}>
          {t("project.projects")}
        </button>
        <span className="text-muted-foreground">/</span>
        {project && <ProjectIcon project={project} />}
        <h1 className="truncate text-sm font-medium">{project?.name}</h1>
        {project?.archived && <Archived />}
        {project && <SnoozedMark muted={snoozes.project(project.id)} />}
        {project && (
          <>
            <Button size="sm" variant="outline" className="ml-auto" onClick={() => onNewThread(project)}>
              <PlusIcon data-icon="inline-start" />
              {t("thread.new")}
            </Button>
            <RowMenu label={t("project.more", { name: project.name })}>
              <ProjectMenuItems
                link={link}
                project={project}
                canManage={canManage}
                onChanged={reload}
                onNewThread={() => onNewThread(project)}
                onSettings={onProjectSettings && (() => onProjectSettings(project))}
                onRemove={() => projectDialogs.remove(project)}
              />
            </RowMenu>
          </>
        )}
      </PageHeader>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto grid w-full max-w-3xl grid-cols-[minmax(0,1fr)] gap-4 p-6">
          <Input className="max-w-64" value={filter} onChange={(e) => setFilter(e.target.value)} placeholder={t("archive.filterThreads")} aria-label={t("archive.filterThreads")} />
          {failed ? (
            failed
          ) : !projects ? (
            <Spinner />
          ) : listed.length === 0 ? (
            <p className="text-muted-foreground text-sm">{t("project.noThreads")}</p>
          ) : (
            <div className="overflow-hidden rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("listing.thread")}</TableHead>
                    <TableHead>{t("listing.status")}</TableHead>
                    <TableHead>{t("listing.updated")}</TableHead>
                    <TableHead className="w-0" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {listed.map((th) => (
                    <TableRow key={th.id} className="cursor-pointer" onClick={() => onOpen(th.id)}>
                      <TableCell>
                        <span className="flex items-center gap-2 font-medium">
                          {th.mode === ThreadMode.TERMINAL ? <SquareTerminalIcon className="text-muted-foreground size-4 shrink-0" /> : <BotIcon className="text-muted-foreground size-4 shrink-0" />}
                          <span className="truncate">{threadName(th)}</span>
                          {th.labels.pinned && <Pinned />}
                          {th.labels.archived && <Archived />}
                          <SnoozedMark muted={snoozes.thread(th.id)} />
                        </span>
                      </TableCell>
                      <TableCell className={threadFailed(th) ? "text-destructive" : "text-muted-foreground"}>
                        <span className="flex items-center gap-1.5">
                          {threadFailed(th) && <CircleAlertIcon className="size-4 shrink-0" aria-hidden />}
                          {threadState(th, t)}
                        </span>
                      </TableCell>
                      <TableCell className="text-muted-foreground">{th.updatedAt && timeAgo(new Date(Number(th.updatedAt.seconds) * 1000), locale, t("common.justNow"))}</TableCell>
                      <TableCell className={menuCell}>
                        <RowMenu label={t("project.more", { name: threadName(th) })}>
                          <ThreadMenuItems
                            link={link}
                            thread={th}
                            onChanged={reload}
                            onOpen={onOpen}
                            onRename={() => threadDialogs.rename(th)}
                            onEnd={() => threadDialogs.end(th)}
                            onSettings={() => onThreadSettings(th.id)}
                          />
                        </RowMenu>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </div>
      </div>
      {threadDialogs.dialogs}
      {projectDialogs.dialogs}
    </div>
  );
}
