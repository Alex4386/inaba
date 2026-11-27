import { Code, ConnectError } from "@connectrpc/connect";
import { CaseSensitiveIcon, ChevronRightIcon, CopyIcon, DownloadIcon, FileIcon, FilesIcon, FolderIcon, MoreHorizontalIcon, RefreshCwIcon, RegexIcon, SearchIcon, WholeWordIcon, XIcon } from "lucide-react";
import { Fragment, lazy, type ReactNode, Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useDefaultLayout } from "react-resizable-panels";
import { ConfirmDialog } from "@/components/dialogs";
import type { HostLink } from "@/api";
import { FilePreview, previewKind } from "@/components/file-preview";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { toast } from "sonner";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { Reveal } from "@/components/code-editor";
import type { GrepFilesResponse_Match, ListFolderResponse_Entry, ReadFileResponse } from "@/gen/inaba/v1/files_pb";
import { useIsMobile } from "@/hooks/use-mobile";
import { errorText, showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

const TREE_KEY = "inaba.files.tree";

/** A file of the thread's folder whole, through the server: saved as a download, or a picture shown with inline. */
// The server relays a host's REST routes under /v1/hosts/{id}/, as the host's /v1/.
const rawUrl = (link: HostLink, threadId: string, path: string, inline = false) =>
  `/v1/hosts/${link.hostId}/threads/${threadId}/raw?${new URLSearchParams({ path, ...(inline && { inline: "1" }) })}`;
// Pictures the host's raw route shows inline, by name; it still checks the bytes before it does.
const RASTER = /\.(png|jpe?g|gif|webp|bmp|ico|avif)$/i;
const loadCodeEditor = () => import("@/components/code-editor");
// Narrower than this, the tree floats over the editor instead of sitting beside it.
const NARROW = 480;

/** The files of a thread's folder: a tree beside them, and the files open in tabs, each kept as it was left. */
export function FilesPanel({ link, threadId, root, onClose, onDirty: onAnyDirty, onActive, readOnly = false }: { link: HostLink; threadId: string; /** What the path's first step is called: the project, or the folder. */ root: string; onClose: () => void; /** Says whether any open file has unsaved edits, so whoever closes or moves the panel can ask first. */ onDirty: (dirty: boolean) => void; /** Says which file is shown, if any. */ onActive?: (path?: string) => void; /** Watching a thread someone else drives: files are read, not changed. */ readOnly?: boolean }) {
  const { t } = useI18n();
  // Each folder's entries once read, or why it could not be; "" is the root.
  const [folders, setFolders] = useState<Record<string, ListFolderResponse_Entry[] | string>>({});
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set());
  const [tabs, setTabs] = useState<string[]>([]);
  const [active, setActive] = useState<string>();
  // Which open files have unsaved edits, so closing them asks first.
  const [dirty, setDirtyOf] = useState<Record<string, boolean>>({});
  const onDirty = useCallback((path: string, d: boolean) => setDirtyOf((x) => (x[path] === d ? x : { ...x, [path]: d })), []);
  const anyDirty = Object.values(dirty).some(Boolean);
  useEffect(() => {
    onAnyDirty(anyDirty);
  }, [anyDirty, onAnyDirty]);
  // Closing with unsaved edits waits for a yes; then holds what to do.
  const [leaving, setLeaving] = useState<() => void>();
  const [refreshing, setRefreshing] = useState(false);
  const [treeHidden, setTreeHidden] = useState(() => localStorage.getItem(TREE_KEY) === "hidden");
  const [narrowBox, setNarrow] = useState(false);
  // On a phone the tree and the search take the whole panel, and step aside once something is picked.
  const mobile = useIsMobile();
  const narrow = narrowBox || mobile;
  // The search floating over the editor: 0 when closed, counting each time it is asked for so it takes the focus again.
  const [search, setSearch] = useState(0);
  // Where each file was asked to be shown from a search result.
  const [revealOf, setRevealOf] = useState<Record<string, Reveal>>({});
  // On a narrow panel the tree is an overlay, shown at first and gone once a file is picked.
  const [overlay, setOverlay] = useState(() => localStorage.getItem(TREE_KEY) !== "hidden");
  const box = useRef<HTMLDivElement>(null);
  const strip = useRef<HTMLDivElement>(null);
  // The editor loads while the tree does, not after the first file opened.
  useEffect(() => void loadCodeEditor(), []);
  useEffect(() => {
    onActive?.(active);
  }, [active, onActive]);
  useEffect(() => {
    strip.current?.querySelector("[aria-current]")?.scrollIntoView({ inline: "nearest", block: "nearest" });
  }, [active]);
  useEffect(() => {
    const ro = new ResizeObserver(([e]) => e.contentRect.width > 0 && setNarrow(e.contentRect.width < NARROW));
    ro.observe(box.current!);
    return () => ro.disconnect();
  }, []);
  const besideTree = !narrow && !treeHidden;
  const layout = useDefaultLayout({ id: "inaba-files", panelIds: besideTree ? ["tree", "editor"] : ["editor"] });

  const loadFolder = useCallback(
    (path: string) =>
      link.files
        .listFolder({ threadId, path })
        .then((r) => r.entries, (e) => errorText(e))
        .then((v) => setFolders((f) => ({ ...f, [path]: v }))),
    [link, threadId],
  );
  useEffect(() => {
    setFolders({});
    void loadFolder("");
  }, [loadFolder]);
  const refresh = () => {
    setRefreshing(true);
    void Promise.all(["", ...expanded].map(loadFolder)).finally(() => setRefreshing(false));
  };
  const toggleFolder = (path: string) => {
    const next = new Set(expanded);
    if (!next.delete(path)) {
      next.add(path);
      // Read again each time it opens, so a refresh while it was closed does not leave it stale.
      void loadFolder(path);
    }
    setExpanded(next);
  };
  const toggleTree = () => {
    if (narrow) {
      setSearch(0);
      return setOverlay((o) => !o);
    }
    localStorage.setItem(TREE_KEY, treeHidden ? "shown" : "hidden");
    setTreeHidden(!treeHidden);
  };

  const open = (path: string) => {
    setTabs((ts) => (ts.includes(path) ? ts : [...ts, path]));
    setActive(path);
    setOverlay(false);
  };
  // A link from a preview opens its file, with its folders opened in the tree to show where it is.
  const reveal = (path: string) => {
    const steps = path.split("/").slice(0, -1).map((_, i, a) => a.slice(0, i + 1).join("/"));
    setExpanded((s) => new Set([...s, ...steps]));
    for (const f of steps) if (folders[f] === undefined) void loadFolder(f);
    open(path);
  };
  const pick = (path: string, at?: Reveal) => {
    if (at) setRevealOf((r) => ({ ...r, [path]: at }));
    reveal(path);
    if (narrow) setSearch(0);
  };
  // Back into the panel, so ⌘⇧F still reaches it once the search is gone.
  const closeSearch = () => {
    setSearch(0);
    box.current?.focus();
  };
  const openSearch = () => {
    setSearch((n) => n + 1);
    if (narrow) setOverlay(false);
  };
  const close = (path: string) => {
    const go = () => {
      const next = tabs.filter((p) => p !== path);
      if (active === path) setActive(next[Math.min(tabs.indexOf(path), next.length - 1)]);
      setTabs(next);
      setDirtyOf(({ [path]: _, ...rest }) => rest);
      setRevealOf(({ [path]: _, ...rest }) => rest);
    };
    if (dirty[path]) setLeaving(() => go);
    else go();
  };

  const rows = (folder: string, depth: number): ReactNode => {
    const list = folders[folder];
    const pad = { paddingLeft: 8 + depth * 12 };
    if (list === undefined) return <Spinner className="my-1" style={{ marginLeft: pad.paddingLeft + 20 }} />;
    if (typeof list === "string") return <p className="text-destructive px-3 py-1 text-xs" style={pad}>{list}</p>;
    if (list.length === 0) return <p className="text-muted-foreground py-1 text-xs" style={{ paddingLeft: pad.paddingLeft + 20 }}>{t("files.empty")}</p>;
    return list.map((e) => {
      const path = folder ? `${folder}/${e.name}` : e.name;
      const isOpen = e.folder && expanded.has(path);
      return (
        <Fragment key={path}>
          <button
            type="button"
            className={cn("hover:bg-sidebar-accent/60 flex w-full items-center gap-1 py-0.5 pr-2 text-left", path === active && "bg-sidebar-accent text-sidebar-accent-foreground font-medium")}
            style={pad}
            title={path}
            aria-expanded={e.folder ? isOpen : undefined}
            aria-current={path === active || undefined}
            onClick={() => (e.folder ? toggleFolder(path) : open(path))}
          >
            {e.folder ? <ChevronRightIcon className={cn("text-muted-foreground size-4 shrink-0 transition-transform", isOpen && "rotate-90")} /> : <span className="size-4 shrink-0" />}
            {e.folder ? <FolderIcon className="text-muted-foreground size-4 shrink-0" /> : <FileIcon className="text-muted-foreground size-4 shrink-0" />}
            <span className="min-w-0 flex-1 truncate">{e.name}</span>
          </button>
          {isOpen && rows(path, depth + 1)}
        </Fragment>
      );
    });
  };
  const tree = (
    <nav className="bg-sidebar text-sidebar-foreground h-full overflow-y-auto py-1 text-sm" aria-label={t("files.title")}>
      {rows("", 0)}
    </nav>
  );

  return (
    <div
      ref={box}
      tabIndex={-1}
      className="flex h-full min-h-0 flex-col outline-none"
      onKeyDown={(e) => {
        if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key.toLowerCase() === "f") {
          e.preventDefault();
          openSearch();
        }
      }}
    >
      <header className="flex h-12 shrink-0 items-center gap-1 border-b px-3">
        <Button size="icon-sm" variant="ghost" aria-label={t("files.tree")} title={t("files.tree")} aria-pressed={narrow ? overlay : !treeHidden} onClick={toggleTree}>
          <FilesIcon />
        </Button>
        <Button size="icon-sm" variant="ghost" aria-label={t("files.search")} title={`${t("files.search")} (⇧⌘F)`} aria-pressed={search > 0} onClick={() => (search ? setSearch(0) : openSearch())}>
          <SearchIcon />
        </Button>
        <span className="min-w-0 flex-1 truncate px-1 text-sm font-medium" title={active ?? root}>
          {root || t("files.root")}
          {active !== undefined && <span className="text-muted-foreground font-normal"> / {active}</span>}
        </span>
        <Button size="icon-sm" variant="ghost" aria-label={t("git.refresh")} title={t("git.refresh")} disabled={refreshing} onClick={refresh}>
          {refreshing ? <Spinner /> : <RefreshCwIcon />}
        </Button>
        <DropdownMenu>
          <DropdownMenuTrigger render={<Button size="icon-sm" variant="ghost" aria-label={t("files.more")} title={t("files.more")} />}>
            <MoreHorizontalIcon />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-auto whitespace-nowrap">
            <DropdownMenuItem disabled={active === undefined} render={<a href={active && rawUrl(link, threadId, active)} download />}>
              <DownloadIcon />
              {t("files.download")}
            </DropdownMenuItem>
            <DropdownMenuItem disabled={active === undefined} onClick={() => active && navigator.clipboard.writeText(active).then(() => toast.success(t("common.copied")), showError)}>
              <CopyIcon />
              {t("files.copyPath")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        <Button size="icon-sm" variant="ghost" aria-label={t("palette.close")} title={t("palette.close")} onClick={onClose}>
          <XIcon />
        </Button>
      </header>
      <ResizablePanelGroup id="inaba-files" orientation="horizontal" className="min-h-0 flex-1" defaultLayout={layout.defaultLayout} onLayoutChanged={layout.onLayoutChanged}>
        {besideTree && (
          <>
            <ResizablePanel id="tree" defaultSize={220} minSize={140} maxSize="70" groupResizeBehavior="preserve-pixel-size">
              {tree}
            </ResizablePanel>
            <ResizableHandle />
          </>
        )}
        <ResizablePanel id="editor" minSize={200}>
          <div className="relative flex h-full min-h-0 flex-col">
            {tabs.length > 0 && (
              <div ref={strip} className="bg-muted/40 flex h-8 shrink-0 overflow-x-auto border-b">
                {tabs.map((p) => (
                  <div
                    key={p}
                    className={cn("group flex shrink-0 items-center border-r text-xs", p === active ? "bg-background text-foreground" : "text-muted-foreground hover:text-foreground")}
                    onAuxClick={(e) => e.button === 1 && (e.preventDefault(), close(p))}
                  >
                    <button type="button" aria-current={p === active || undefined} className="h-full max-w-48 truncate pr-1 pl-3" title={p} onClick={() => setActive(p)}>
                      {p.split("/").at(-1)}
                    </button>
                    <button type="button" className="hover:bg-muted mr-1 grid size-5 place-items-center rounded" aria-label={dirty[p] ? `${t("palette.close")} (${t("files.unsaved")})` : t("palette.close")} title={t("palette.close")} onClick={() => close(p)}>
                      {dirty[p] && <span className="size-2 rounded-full bg-current group-hover:hidden" />}
                      <XIcon className={cn("size-3.5", dirty[p] && "hidden group-hover:block")} />
                    </button>
                  </div>
                ))}
              </div>
            )}
            <div className="relative min-h-0 flex-1">
              {/* Every open file stays mounted, hidden when not shown, so its edits, undo, view and scroll wait for it. */}
              {tabs.map((p) => (
                <div key={p} className={cn("absolute inset-0 flex flex-col", p !== active && "invisible")}>
                  <FileView link={link} threadId={threadId} path={p} onDirty={onDirty} readOnly={readOnly} onOpen={reveal} at={revealOf[p]} />
                </div>
              ))}
              {tabs.length === 0 && (
                <div className="text-muted-foreground absolute inset-0 flex flex-col items-center justify-center gap-2 p-4 text-center text-sm">
                  <p>{t("files.pick")}</p>
                  {!besideTree && !(narrow && overlay) && (
                    <Button size="sm" variant="outline" onClick={toggleTree}>
                      <FilesIcon data-icon="inline-start" />
                      {t("files.tree")}
                    </Button>
                  )}
                </div>
              )}
            </div>
            {narrow && overlay && <div className="absolute inset-0 z-20">{tree}</div>}
            {/* Mounted while closed, so the query and its results wait for the next look. */}
            <SearchPanel link={link} threadId={threadId} focus={search} full={narrow} onPick={pick} onClose={closeSearch} />
          </div>
        </ResizablePanel>
      </ResizablePanelGroup>
      <ConfirmDialog
        open={leaving !== undefined}
        onClose={() => setLeaving(undefined)}
        title={t("files.discard")}
        description={t("files.discardHelp")}
        cancel={t("files.keepEditing")}
        confirm={t("files.discardConfirm")}
        destructive
        onConfirm={() => {
          leaving?.();
          setLeaving(undefined);
        }}
      />
    </div>
  );
}

function size(n: bigint) {
  const bytes = Number(n);
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
}

/** A file's content: text with line numbers, an image as itself, anything else described. */
const CodeEditor = lazy(loadCodeEditor);

function FileView({ link, threadId, path, onDirty, readOnly, onOpen, at }: { link: HostLink; threadId: string; path: string; onDirty: (path: string, dirty: boolean) => void; readOnly: boolean; /** Opens another file of the folder, from a link in a preview. */ onOpen: (path: string) => void; /** A line to show, from a search result. */ at?: Reveal }) {
  const { t } = useI18n();
  const [file, setFile] = useState<ReadFileResponse>();
  const [error, setError] = useState("");
  // What the editor holds, and the version of the file it started from.
  const [draft, setDraft] = useState<string>();
  const [version, setVersion] = useState("");
  const [saving, setSaving] = useState(false);
  const [stale, setStale] = useState(false);
  // Reloading over unsaved edits waits for a yes.
  const [reloading, setReloading] = useState(false);
  // Markdown and tables open rendered; the source view is the editor.
  const kind = previewKind(path);
  const [source, setSource] = useState(false);
  // The editor stays mounted once shown, so toggling back keeps its undo history, cursor and scroll.
  const [editorShown, setEditorShown] = useState(!kind);
  const previewing = kind !== undefined && !source;
  // A picture by its name goes straight to the raw route, which streams it, instead of through readFile first;
  // one that turns out not to be a picture is read as any other file. The mount time keeps a reopened one fresh.
  const [notPicture, setNotPicture] = useState(false);
  const pictured = !notPicture && RASTER.test(path);
  const [opened] = useState(Date.now);
  // A search result is a line of the source, so it shows the source.
  useEffect(() => {
    if (at && kind) {
      setSource(true);
      setEditorShown(true);
    }
  }, [at, kind]);
  const load = useCallback(() => {
    if (pictured) return;
    setFile(undefined);
    setError("");
    setStale(false);
    link.files.readFile({ threadId, path }).then(
      (f) => {
        setFile(f);
        setVersion(f.version);
        setDraft(undefined);
      },
      (e) => setError(errorText(e)),
    );
  }, [link, threadId, path, pictured]);
  useEffect(load, [load]);
  // From the host whole, as a picture can be larger than what readFile sends; the version keeps a changed one fresh.
  const image = file?.mimeType.startsWith("image/") ? `${rawUrl(link, threadId, path, true)}&v=${file.version}` : undefined;
  const text = useMemo(() => (file && (file.mimeType.startsWith("text/") || file.mimeType.includes("json")) ? new TextDecoder().decode(file.data) : undefined), [file]);
  const dirty = draft !== undefined && draft !== text;
  useEffect(() => {
    onDirty(path, dirty);
  }, [path, dirty, onDirty]);
  const save = () => {
    if (!dirty || saving || file?.truncated) return;
    setSaving(true);
    // What was written; anything typed while it was being written stays in the editor.
    const saved = draft;
    const data = new TextEncoder().encode(saved);
    link.files
      .writeFile({ threadId, path, data, baseVersion: version })
      .then(
        (r) => {
          setVersion(r.version);
          setFile((f) => f && { ...f, data });
          setDraft((d) => (d === saved ? undefined : d));
        },
        (e) => (ConnectError.from(e).code === Code.Aborted ? setStale(true) : showError(e)),
      )
      .finally(() => setSaving(false));
  };

  if (error) return <p className="text-destructive p-4 text-sm">{error}</p>;
  if (pictured)
    return (
      <div className="min-h-0 flex-1 overflow-auto">
        <img src={`${rawUrl(link, threadId, path, true)}&v=${opened}`} alt={path} className="m-3 max-w-[calc(100%-1.5rem)] rounded-md border" onError={() => setNotPicture(true)} />
      </div>
    );
  if (!file) return <Spinner className="m-4" />;
  return (
    <div
      className="flex min-h-0 flex-1 flex-col outline-none"
      // Focusable so ⌘S reaches here while the preview hides the editor that otherwise binds it.
      tabIndex={-1}
      onKeyDown={(e) => {
        if (previewing && (e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "s") {
          e.preventDefault();
          if (!readOnly) save();
        }
      }}
    >
      {file.truncated && <p className="text-muted-foreground border-b px-3 py-1.5 text-xs">{t("files.truncated", { size: size(file.size) })}</p>}
      {stale && (
        <div className="bg-warning/10 flex items-center gap-2 border-b px-3 py-1.5 text-xs">
          <span className="flex-1">{t("files.changedOnDisk")}</span>
          <Button size="xs" variant="outline" onClick={() => (dirty ? setReloading(true) : load())}>
            {t("files.reload")}
          </Button>
        </div>
      )}
      {text !== undefined && (
        <div className="flex h-8 shrink-0 items-center gap-2 border-b px-3 text-xs">
          {kind && (
            <Tabs value={source ? "source" : "preview"} onValueChange={(v) => (setSource(v === "source"), v === "source" && setEditorShown(true))}>
              <TabsList className="h-6!" aria-label={t("files.view")}>
                <TabsTrigger value="preview" className="px-2 text-xs">
                  {t("files.preview")}
                </TabsTrigger>
                <TabsTrigger value="source" className="px-2 text-xs">
                  {t("files.source")}
                </TabsTrigger>
              </TabsList>
            </Tabs>
          )}
          <span className="text-muted-foreground flex-1">{dirty ? t("files.unsaved") : t("files.saved")}</span>
          <Button size="xs" variant={dirty ? "default" : "ghost"} disabled={!dirty || saving || file.truncated} onClick={save} title="⌘S">
            {saving && <Spinner data-icon="inline-start" />}
            {t("providers.save")}
          </Button>
        </div>
      )}
      {image ? (
        <div className="min-h-0 flex-1 overflow-auto">
          <img src={image} alt={path} className="m-3 max-w-[calc(100%-1.5rem)] rounded-md border" />
        </div>
      ) : text !== undefined ? (
        <>
          {previewing && <FilePreview kind={kind} text={draft ?? text} path={path} onOpen={onOpen} />}
          {editorShown && (
            <div className={previewing ? "hidden" : "min-h-0 flex-1"}>
              {/* The text is already here: show it plain while the editor's chunk downloads. */}
              <Suspense fallback={<pre className="h-full overflow-auto p-3 font-mono text-[13px] whitespace-pre">{draft ?? text}</pre>}>
                <CodeEditor path={path} value={draft ?? text} readOnly={file.truncated || readOnly} onChange={setDraft} onSave={() => !readOnly && save()} reveal={at} />
              </Suspense>
            </div>
          )}
        </>
      ) : (
        <p className="text-muted-foreground p-4 text-sm">{t("files.binary", { type: file.mimeType, size: size(file.size) })}</p>
      )}
      <ConfirmDialog
        open={reloading}
        onClose={() => setReloading(false)}
        title={t("files.discard")}
        description={t("files.discardHelp")}
        cancel={t("files.keepEditing")}
        confirm={t("files.discardConfirm")}
        destructive
        onConfirm={load}
      />
    </div>
  );
}

// Each half is there once its call has answered.
type Found = { files?: string[]; matches?: GrepFilesResponse_Match[]; truncated: boolean };

/** Finding files by name and lines by content, floating over the editor; it stays open to go through the results. */
function SearchPanel({ link, threadId, focus, full, onPick, onClose }: { link: HostLink; threadId: string; /** 0 while closed; changes when the search is asked for again, to take the focus. */ focus: number; /** Covers the whole panel, as on a phone. */ full: boolean; onPick: (path: string, at?: Reveal) => void; onClose: () => void }) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const [caseSensitive, setCaseSensitive] = useState(false);
  const [wholeWord, setWholeWord] = useState(false);
  const [regex, setRegex] = useState(false);
  // The results, or why there are none.
  const [found, setFound] = useState<Found | string>();
  const [busy, setBusy] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (focus) input.current?.select();
  }, [focus]);
  useEffect(() => {
    if (!query.trim()) {
      setFound(undefined);
      return setBusy(false);
    }
    setBusy(true);
    const abort = new AbortController();
    const signal = abort.signal;
    const timer = setTimeout(() => {
      // Names come back at once and lines can take seconds, so each shows as it lands.
      setFound({ truncated: false });
      let left = 2;
      const settle = <T,>(call: Promise<T>, take: (r: T, f: Found) => Found) =>
        void call
          .then(
            (r) => !signal.aborted && setFound((f) => (typeof f === "object" ? take(r, f) : f)),
            (e) => !signal.aborted && setFound(errorText(e)),
          )
          .finally(() => !signal.aborted && --left === 0 && setBusy(false));
      settle(link.files.searchFiles({ threadId, query }, { signal }), (r, f) => ({ ...f, files: r.paths.slice(0, 5) }));
      settle(link.files.grepFiles({ threadId, query, caseSensitive, wholeWord, regex }, { signal }), (r, f) => ({ ...f, matches: r.matches, truncated: r.truncated }));
    }, 250);
    return () => {
      clearTimeout(timer);
      abort.abort();
    };
  }, [link, threadId, query, caseSensitive, wholeWord, regex]);
  // The host sends them in path order, so each file's lines are together.
  const byFile = useMemo(() => {
    const files = new Map<string, GrepFilesResponse_Match[]>();
    for (const m of typeof found === "object" ? (found.matches ?? []) : []) files.set(m.path, [...(files.get(m.path) ?? []), m]);
    return [...files];
  }, [found]);
  const toggles = [
    { on: caseSensitive, set: setCaseSensitive, label: t("files.matchCase"), Icon: CaseSensitiveIcon },
    { on: wholeWord, set: setWholeWord, label: t("files.wholeWord"), Icon: WholeWordIcon },
    { on: regex, set: setRegex, label: t("files.regex"), Icon: RegexIcon },
  ];
  const row = "hover:bg-accent flex w-full items-baseline gap-2 px-3 py-0.5 text-left";
  return (
    <section
      className={cn("bg-popover text-popover-foreground absolute z-30 flex flex-col text-sm", !focus && "hidden", full ? "inset-0" : "top-2 right-2 bottom-2 w-80 max-w-[calc(100%-1rem)] rounded-lg border shadow-lg")}
      aria-label={t("files.search")}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.stopPropagation();
          onClose();
        }
      }}
    >
      <div className="flex shrink-0 items-center gap-0.5 border-b p-1.5">
        <input
          ref={input}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t("files.searchPlaceholder")}
          aria-label={t("files.search")}
          className="placeholder:text-muted-foreground min-w-0 flex-1 bg-transparent px-1.5 py-1 outline-none"
        />
        {toggles.map(({ on, set, label, Icon }) => (
          <Button key={label} size="icon-xs" variant="ghost" className={cn(on && "bg-accent text-accent-foreground")} aria-pressed={on} aria-label={label} title={label} onClick={() => set(!on)}>
            <Icon />
          </Button>
        ))}
        <Button size="icon-xs" variant="ghost" aria-label={t("palette.close")} title={t("palette.close")} onClick={onClose}>
          <XIcon />
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto py-1">
        {busy && <Spinner className="mx-3 my-2" />}
        {typeof found === "string" && <p className="text-destructive px-3 py-2 text-xs">{found}</p>}
        {typeof found === "object" && found.files?.length === 0 && found.matches?.length === 0 && <p className="text-muted-foreground px-3 py-2 text-xs">{t("files.noResults")}</p>}
        {typeof found === "object" && found.files && found.files.length > 0 && (
          <>
            <h3 className="text-muted-foreground px-3 pt-1 text-xs font-medium">{t("files.fileNames")}</h3>
            {found.files.map((p) => (
              <button key={p} type="button" className={row} title={p} onClick={() => onPick(p)}>
                <FileIcon className="text-muted-foreground size-3.5 shrink-0 self-center" />
                <span className="min-w-0 truncate">{p}</span>
              </button>
            ))}
          </>
        )}
        {byFile.map(([path, ms]) => (
          <Fragment key={path}>
            <h3 className="text-muted-foreground mt-1 truncate px-3 pt-1 text-xs font-medium" title={path}>
              {path}
            </h3>
            {ms.map((m) => (
              <button key={m.line} type="button" className={cn(row, "font-mono text-xs")} onClick={() => onPick(path, { line: m.line, column: m.column, length: m.matchEnd - m.matchStart })}>
                <span className="text-muted-foreground w-8 shrink-0 text-right tabular-nums">{m.line}</span>
                <span className="min-w-0 truncate">
                  {m.text.slice(0, m.matchStart)}
                  <mark className="bg-yellow-300/60 text-inherit dark:bg-yellow-500/40">{m.text.slice(m.matchStart, m.matchEnd)}</mark>
                  {m.text.slice(m.matchEnd)}
                </span>
              </button>
            ))}
          </Fragment>
        ))}
        {typeof found === "object" && found.truncated && <p className="text-muted-foreground px-3 py-2 text-xs">{t("files.resultsTruncated")}</p>}
      </div>
    </section>
  );
}
