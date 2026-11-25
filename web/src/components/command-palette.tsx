import { ArrowLeftIcon, BotIcon, CornerLeftUpIcon, FolderGit2Icon, FolderIcon, FolderPlusIcon, FolderSearchIcon, PlusIcon, SearchIcon, SettingsIcon, SquareTerminalIcon } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import type { HostLink } from "@/api";
import { ProjectIcon, threadName, threadState } from "@/components/project-tree";
import { Button } from "@/components/ui/button";
import { HostAvatar, hostSubtitle } from "@/components/host-icon";
import { defaultFilter } from "cmdk";
import { Command, CommandDialog, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { type ListDirectoryResponse, type Project, type Thread, ThreadMode } from "@/gen/inaba/v1/host_pb";
import type { Host } from "@/gen/inaba/v1/server_pb";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { shortPath } from "@/lib/utils";

export type PalettePage = "root" | "folder";

// The value of the row a typed or browsed path offers, kept fixed whatever the path.
const PATH_ROW = "\u0000path";
// What reads as a folder path rather than a search: /…, ~ or ~/…, C:\….
const PATH = /^(\/|~([\/\\]|$)|[A-Za-z]:[\/\\])/;

function Key({ children }: { children: string }) {
  return <kbd className="bg-muted rounded border px-1.5 py-0.5 font-sans text-[0.7rem]">{children}</kbd>;
}

/**
 * The one place to jump anywhere: open a thread, start one in a project, switch
 * hosts, or add a project by walking the host's folders. Opened with the search
 * button or Cmd/Ctrl+K.
 */
export function CommandPalette({
  page,
  onPage,
  onClose,
  hosts,
  activeHost,
  link,
  canManageProjects,
  onSelectHost,
  onOpenThread,
  onNewThread,
  onSettings,
}: {
  /** The page shown; the palette is open while it is set. */
  page?: PalettePage;
  onPage: (page: PalettePage) => void;
  onClose: () => void;
  hosts: Host[];
  activeHost?: Host;
  /** The active host's API; absent while no host is connected. */
  link?: HostLink;
  canManageProjects: boolean;
  onSelectHost: (host: Host) => void;
  onOpenThread: (id: string) => void;
  onNewThread: (project: Project) => void;
  onSettings: () => void;
}) {
  const { t } = useI18n();
  const [search, setSearch] = useState("");
  const [threads, setThreads] = useState<Thread[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [listing, setListing] = useState<ListDirectoryResponse>();
  const open = page !== undefined;

  useEffect(() => {
    setSearch("");
    setTyped(undefined);
    if (!open || !link) return;
    Promise.all([link.threads.listThreads({}), link.projects.listProjects({})]).then(([th, pr]) => {
      setThreads(th.threads);
      setProjects(pr.projects);
    }, showError);
  }, [open, link]);

  const browse = useCallback(
    (path: string) => {
      setSearch("");
      link?.projects.listDirectory({ path }).then(setListing, showError);
    },
    [link],
  );
  // The folder page starts where a typed path pointed (filtered by what was typed past its last separator),
  // or else at the home folder; going back to search brings back what was typed.
  const startAt = useRef({ path: "", filter: "" });
  // What was typed in search before finding it in the filesystem; unset when the folder page was opened directly.
  const [typed, setTyped] = useState<string>();
  useEffect(() => {
    if (page === "folder") {
      const { path, filter } = startAt.current;
      link?.projects.listDirectory({ path }).then((r) => (setListing(r), setSearch(filter)), showError);
    } else setListing(undefined);
    startAt.current = { path: "", filter: "" };
  }, [page, link]);
  // On the folder page, typing goes like a shell: "git/" steps into git, "../" up, and a whole path (~/…, /…, C:\…) there.
  const type = (value: string) => {
    setSearch(value);
    if (page !== "folder" || !listing || !/.[\/\\]$/.test(value)) return;
    const name = value.slice(0, -1);
    if (PATH.test(value)) return browse(value);
    if (name === "..") return listing.parent && browse(listing.parent);
    const entry = listing.entries.find((e) => e.name === name) ?? listing.entries.find((e) => e.name.toLowerCase() === name.toLowerCase());
    if (entry) browse(`${listing.path.replace(/[\/\\]$/, "")}${listing.path.includes("\\") ? "\\" : "/"}${entry.name}`);
  };
  // The text brought back from the filesystem: it stays in search until changed, instead of jumping straight back.
  const returned = useRef<string | undefined>(undefined);
  const input = useRef<HTMLInputElement>(null);
  const back = () => {
    const text = typed ?? "";
    returned.current = text;
    setSearch(text);
    // Typing goes on from the end of what was brought back.
    requestAnimationFrame(() => (input.current?.focus(), input.current?.setSelectionRange(text.length, text.length)));
    setTyped(undefined);
    onPage("root");
  };

  // A search that reads as a folder (/…, ~/…, C:\…) is looked up on the host, which also expands ~.
  // A folder that is there exactly, or else the folder before the last separator and the rest as a filter (~/Doc).
  const [found, setFound] = useState<{ path: string; filter: string }>();
  useEffect(() => {
    setFound(undefined);
    if (page !== "root" || !link || !PATH.test(search)) return;
    const cut = Math.max(search.lastIndexOf("/"), search.lastIndexOf("\\")) + 1;
    const timer = setTimeout(
      () =>
        link.projects.listDirectory({ path: search }).then(
          (r) =>
            returned.current === search ? setFound({ path: r.path, filter: "" }) : cut === search.length ? go({ path: r.path, filter: "" }) : resolveIn(search.slice(0, cut), search.slice(cut)),
          () => resolveIn(search.slice(0, cut), search.slice(cut)),
        ),
      200,
    );
    return () => clearTimeout(timer);
    // A typed path goes straight to the filesystem, in the folder before its last separator and filtered by the rest,
    // as a shell would complete it; just back from there, it offers the way in instead.
    function go(target: { path: string; filter: string }) {
      if (returned.current === search) return setFound(target);
      setTyped(search);
      startAt.current = target;
      onPage("folder");
    }
    function resolveIn(dir: string, rest: string) {
      link!.projects.listDirectory({ path: dir }).then((r) => go({ path: r.path, filter: rest }), () => {});
    }
  }, [page, link, search, onPage]);
  const exact = found && !found.filter ? found.path : undefined;
  // shortcut: exact path match only, case-sensitive even on Windows/macOS hosts; normalize if that bites.
  const foundProject = exact ? projects.find((p) => p.cwd === exact) : undefined;
  // The project already at the folder being browsed, if any: it is opened rather than added twice.
  const here = listing ? projects.find((p) => p.cwd === listing.path) : undefined;

  const run = (action: () => void) => {
    onClose();
    action();
  };
  const addProject = async (cwd: string) => {
    if (!link) return;
    try {
      const project = await link.projects.createProject({ cwd });
      toast.success(t("project.added", { name: project.name }));
      run(() => onNewThread(project));
    } catch (e) {
      showError(e);
    }
  };

  return (
    <CommandDialog open={open} onOpenChange={(next) => !next && onClose()} title={t("search.open")} description={t("search.open")} className="top-1/2 -translate-y-1/2 sm:max-w-xl">
      <Command
        // The typed-path row always shows: its value stays fixed, as cmdk breaks when a mounted item's value changes.
        filter={(value, query, keywords) => (value.startsWith(PATH_ROW) ? 1 : defaultFilter!(value, query, keywords))}
        onKeyDown={(e) => {
          // On an empty search, Backspace steps back: up a folder, then out of the folder page.
          if (e.key !== "Backspace" || search !== "" || page !== "folder") return;
          e.preventDefault();
          if (listing?.parent) browse(listing.parent);
          else back();
        }}
      >
        <div className="flex items-center">
          {page === "folder" && (
            <Button className="ml-2" size="icon-sm" variant="ghost" aria-label={t("palette.back")} title={t("palette.back")} onClick={back}>
              <ArrowLeftIcon />
            </Button>
          )}
          <div className="flex-1">
            <CommandInput ref={input} value={search} onValueChange={type} placeholder={page === "folder" ? t("palette.folderPlaceholder") : t("search.placeholder")} />
          </div>
        </div>
        {/* On the folder page only its folders scroll, so the rows under them never cover any. */}
        <CommandList className={page === "folder" ? "max-h-none overflow-visible" : "max-h-[min(60vh,28rem)]"}>
          <CommandEmpty>{t("search.none")}</CommandEmpty>
          {page === "folder" && listing && (
            <CommandGroup heading={listing.path} className="max-h-[min(50vh,24rem)] overflow-y-auto **:[[cmdk-group-heading]]:break-all">
              {listing.parent && (
                <CommandItem value=".." onSelect={() => browse(listing.parent)}>
                  <CornerLeftUpIcon />
                  ..
                </CommandItem>
              )}
              {listing.entries.map((entry) => (
                <CommandItem key={entry.name} value={entry.name} onSelect={() => browse(`${listing.path === "/" ? "" : listing.path}/${entry.name}`)}>
                  {entry.git ? <FolderGit2Icon /> : <FolderIcon />}
                  {entry.name}
                </CommandItem>
              ))}
            </CommandGroup>
          )}
          {/* Under the folders, outside their scroll, and shown whatever the search. */}
          {page === "folder" && listing && (
            <CommandGroup forceMount className="border-t">
              {typed !== undefined && (
                <CommandItem forceMount value={`${PATH_ROW}back`} onSelect={back}>
                  <SearchIcon />
                  {t("palette.returnToSearch")}
                </CommandItem>
              )}
              {here ? (
                <CommandItem forceMount value={PATH_ROW} onSelect={() => run(() => onNewThread(here))}>
                  <ProjectIcon project={here} />
                  <span className="font-medium">{t("thread.newIn", { name: here.name })}</span>
                </CommandItem>
              ) : (
                <CommandItem forceMount value={PATH_ROW} onSelect={() => void addProject(listing.path)}>
                  <FolderPlusIcon />
                  <span className="font-medium">{t("palette.useFolder")}</span>
                </CommandItem>
              )}
            </CommandGroup>
          )}
          {page === "root" && found && (foundProject || canManageProjects) && (
            <CommandGroup key={`${found.path}\u0000${found.filter}`} heading={found.path}>
              {foundProject ? (
                <CommandItem value={`${PATH_ROW}open`} onSelect={() => run(() => onNewThread(foundProject))}>
                  <ProjectIcon project={foundProject} />
                  {t("thread.newIn", { name: foundProject.name })}
                </CommandItem>
              ) : (
                exact && (
                  <CommandItem value={`${PATH_ROW}add`} onSelect={() => void addProject(exact)}>
                    <FolderPlusIcon />
                    {t("palette.addPath")}
                  </CommandItem>
                )
              )}
              {canManageProjects && (
                <CommandItem
                  value={`${PATH_ROW}find`}
                  onSelect={() => {
                    setTyped(search);
                    startAt.current = found;
                    onPage("folder");
                  }}
                >
                  <FolderSearchIcon />
                  {t("palette.findInFiles")}
                </CommandItem>
              )}
            </CommandGroup>
          )}
          {page === "root" && (
            <>
              {threads.length > 0 && (
                <CommandGroup heading={t("palette.threads")}>
                  {threads.map((thread) => (
                    <CommandItem key={thread.id} value={`${threadName(thread)} ${thread.id}`} onSelect={() => run(() => onOpenThread(thread.id))}>
                      {thread.mode === ThreadMode.TERMINAL ? <SquareTerminalIcon /> : <BotIcon />}
                      <span className="truncate">{threadName(thread)}</span>
                      <span className="text-muted-foreground ml-auto hidden shrink-0 text-xs sm:inline">
                        {projects.find((p) => p.id === thread.projectId)?.name} · {threadState(thread, t)}
                      </span>
                    </CommandItem>
                  ))}
                </CommandGroup>
              )}
              {link && (
                <CommandGroup heading={t("project.projects")}>
                  {projects.map((project) => (
                    <CommandItem key={project.id} value={`${t("thread.newIn", { name: project.name })} ${project.id}`} onSelect={() => run(() => onNewThread(project))}>
                      <ProjectIcon project={project} />
                      <span className="shrink-0">{t("thread.newIn", { name: project.name })}</span>
                      <span className="text-muted-foreground ml-auto shrink-0 text-xs">{shortPath(project.cwd)}</span>
                    </CommandItem>
                  ))}
                  {canManageProjects && (
                    <CommandItem value={t("project.add")} onSelect={() => onPage("folder")}>
                      <PlusIcon />
                      {t("project.add")}…
                    </CommandItem>
                  )}
                </CommandGroup>
              )}
              <CommandGroup heading={t("host.hosts")}>
                {hosts.map((host) => (
                  <CommandItem key={host.id} value={`${host.name} ${host.id}`} onSelect={() => run(() => onSelectHost(host))}>
                    <HostAvatar host={host} className="size-4" label={t(host.online ? "host.online" : "host.offline")} />
                    <span className="grid min-w-0 leading-tight">
                      <span className="truncate">{host.name}</span>
                      {hostSubtitle(host, t("host.personal")) && <span className="text-muted-foreground truncate text-xs">{hostSubtitle(host, t("host.personal"))}</span>}
                    </span>
                    {host.id === activeHost?.id && <span className="text-muted-foreground ml-auto text-xs">{t("palette.current")}</span>}
                  </CommandItem>
                ))}
                <CommandItem value={t("settings.title")} onSelect={() => run(onSettings)}>
                  <SettingsIcon />
                  {t("settings.title")}
                </CommandItem>
              </CommandGroup>
            </>
          )}
        </CommandList>
        <footer className="text-muted-foreground hidden flex-wrap items-center sm:flex gap-x-4 gap-y-1 border-t px-3 py-2 text-xs">
          <span className="flex items-center gap-1.5">
            <Key>↑</Key>
            <Key>↓</Key>
            {t("palette.navigate")}
          </span>
          <span className="flex items-center gap-1.5">
            <Key>Enter</Key>
            {t("palette.select")}
          </span>
          {page === "folder" && (
            <span className="flex items-center gap-1.5">
              <Key>Backspace</Key>
              {t("palette.back")}
            </span>
          )}
          <span className="flex items-center gap-1.5">
            <Key>Esc</Key>
            {t("palette.close")}
          </span>
        </footer>
      </Command>
    </CommandDialog>
  );
}
