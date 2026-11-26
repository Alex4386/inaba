import { Code, ConnectError } from "@connectrpc/connect";
import { ChevronLeftIcon, FileIcon, FolderIcon, RefreshCwIcon, XIcon } from "lucide-react";
import { Fragment, lazy, Suspense, useCallback, useEffect, useMemo, useState } from "react";
import { ConfirmDialog } from "@/components/dialogs";
import type { HostLink } from "@/api";
import { FilePreview, previewKind } from "@/components/file-preview";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { ListFolderResponse_Entry, ReadFileResponse } from "@/gen/inaba/v1/files_pb";
import { errorText, showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

/** The files of a thread's folder: one folder at a time, and a file read in place. */
export function FilesPanel({ link, threadId, root, onClose, readOnly = false }: { link: HostLink; threadId: string; /** What the path's first step is called: the project, or the folder. */ root: string; onClose: () => void; /** Watching a thread someone else drives: files are read, not changed. */ readOnly?: boolean }) {
  const { t } = useI18n();
  const [folder, setFolder] = useState("");
  const [entries, setEntries] = useState<ListFolderResponse_Entry[]>();
  const [file, setOpenFile] = useState<string>();
  // Unsaved edits in the open file, so leaving it asks first.
  const [dirty, setDirty] = useState(false);
  // Leaving a file with unsaved edits waits for a yes; then holds what to do after.
  const [leaving, setLeaving] = useState<() => void>();
  const setFile = (next: string | undefined, then?: () => void) => {
    const go = () => {
      setDirty(false);
      setOpenFile(next);
      then?.();
    };
    if (dirty && next !== file) setLeaving(() => go);
    else go();
  };
  const [error, setError] = useState("");
  const [refreshing, setRefreshing] = useState(false);

  const load = useCallback(() => {
    setError("");
    setRefreshing(true);
    link.files
      .listFolder({ threadId, path: folder })
      .then(
        (r) => setEntries(r.entries),
        (e) => (setEntries([]), setError(errorText(e))),
      )
      .finally(() => setRefreshing(false));
  }, [link, threadId, folder]);
  useEffect(() => {
    setEntries(undefined);
    load();
  }, [load]);

  const parts = folder ? folder.split("/") : [];
  const join = (name: string) => (folder ? `${folder}/${name}` : name);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex h-12 shrink-0 items-center gap-1 border-b px-3">
        {file !== undefined && (
          <Button size="icon-sm" variant="ghost" aria-label={t("files.back")} title={t("files.back")} onClick={() => void setFile(undefined)}>
            <ChevronLeftIcon />
          </Button>
        )}
        {/* Where we are, each step a way back up. */}
        <nav className="flex min-w-0 flex-1 items-center gap-0.5 overflow-hidden text-sm" aria-label={t("files.path")}>
          <Button size="xs" variant="ghost" className="shrink-0 font-medium" title={root} onClick={() => setFile(undefined, () => setFolder(""))}>
            {root || t("files.root")}
          </Button>
          {parts.map((part, i) => (
            <Fragment key={i}>
              <span className="text-muted-foreground">/</span>
              <Button size="xs" variant="ghost" className="min-w-0 truncate" onClick={() => setFile(undefined, () => setFolder(parts.slice(0, i + 1).join("/")))}>
                {part}
              </Button>
            </Fragment>
          ))}
          {file !== undefined && (
            <>
              <span className="text-muted-foreground">/</span>
              <span className="min-w-0 truncate px-1.5 font-medium">{file.split("/").at(-1)}</span>
            </>
          )}
        </nav>
        {/* The folder list is hidden while a file is open; the file has its own reload when it changes on disk. */}
        {file === undefined && (
          <Button size="icon-sm" variant="ghost" aria-label={t("git.refresh")} title={t("git.refresh")} disabled={refreshing} onClick={load}>
            {refreshing ? <Spinner /> : <RefreshCwIcon />}
          </Button>
        )}
        <Button size="icon-sm" variant="ghost" aria-label={t("palette.close")} title={t("palette.close")} onClick={onClose}>
          <XIcon />
        </Button>
      </header>
      {file !== undefined ? (
        // Keyed so each file opens fresh, in its preview when it has one.
        <FileView key={file} link={link} threadId={threadId} path={file} onDirty={setDirty} readOnly={readOnly} onOpen={(p) => setFile(p, () => setFolder(p.split("/").slice(0, -1).join("/")))} />
      ) : error ? (
        <p className="text-destructive p-4 text-sm">{error}</p>
      ) : !entries ? (
        <Spinner className="m-4" />
      ) : entries.length === 0 ? (
        <p className="text-muted-foreground m-auto text-sm">{t("files.empty")}</p>
      ) : (
        <ul className="min-h-0 flex-1 overflow-y-auto py-1">
          {entries.map((e) => (
            <li key={e.name}>
              <button
                type="button"
                className="hover:bg-muted flex w-full items-center gap-2 px-3 py-1 text-left text-sm"
                onClick={() => (e.folder ? setFolder(join(e.name)) : void setFile(join(e.name)))}
              >
                {e.folder ? <FolderIcon className="text-muted-foreground size-4 shrink-0" /> : <FileIcon className="text-muted-foreground size-4 shrink-0" />}
                <span className="min-w-0 flex-1 truncate">{e.name}</span>
                {!e.folder && <span className="text-muted-foreground text-xs tabular-nums">{size(e.size)}</span>}
              </button>
            </li>
          ))}
        </ul>
      )}
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
const CodeEditor = lazy(() => import("@/components/code-editor"));

function FileView({ link, threadId, path, onDirty, readOnly, onOpen }: { link: HostLink; threadId: string; path: string; onDirty: (dirty: boolean) => void; readOnly: boolean; /** Opens another file of the folder, from a link in a preview. */ onOpen: (path: string) => void }) {
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
  const load = useCallback(() => {
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
  }, [link, threadId, path]);
  useEffect(load, [load]);
  const image = useMemo(() => (file?.mimeType.startsWith("image/") ? URL.createObjectURL(new Blob([file.data as BlobPart], { type: file.mimeType })) : undefined), [file]);
  useEffect(
    () => () => {
      if (image) URL.revokeObjectURL(image);
    },
    [image],
  );
  const text = useMemo(() => (file && (file.mimeType.startsWith("text/") || file.mimeType.includes("json")) ? new TextDecoder().decode(file.data) : undefined), [file]);
  const dirty = draft !== undefined && draft !== text;
  useEffect(() => onDirty(dirty), [dirty, onDirty]);
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
              <Suspense fallback={<Spinner className="m-4" />}>
                <CodeEditor path={path} value={draft ?? text} readOnly={file.truncated || readOnly} onChange={setDraft} onSave={() => !readOnly && save()} />
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
