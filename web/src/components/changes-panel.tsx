import { ArrowDownIcon, ArrowUpIcon, ChevronsDownUpIcon, ChevronsUpDownIcon, ColumnsIcon, FlaskConicalOffIcon, GitBranchIcon, GitPullRequestIcon, PilcrowIcon, PlusIcon, RefreshCwIcon, RowsIcon, Undo2Icon, WrapTextIcon, XIcon } from "lucide-react";
import { type FormEvent, lazy, Suspense, useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import type { HostLink } from "@/api";
import { ConfirmDialog, RenameDialog } from "@/components/dialogs";
import { Picker } from "@/components/picker";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import type { GitStatus } from "@/gen/inaba/v1/host_pb";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

const DiffView = lazy(() => import("@/components/diff-view"));

// How diffs are laid out is one's own taste, kept in this browser.
const DIFF_PREFS = "inaba.diffView";
function readPrefs(): { split: boolean; wrap: boolean; hideTests: boolean; ignoreWhitespace: boolean } {
  const fallback = { split: false, wrap: false, hideTests: false, ignoreWhitespace: false };
  try {
    return { ...fallback, ...JSON.parse(localStorage.getItem(DIFF_PREFS) ?? "{}") };
  } catch {
    return fallback;
  }
}

// The base picker's value for the repository's default branch; a colon is never in a branch name.
const DEFAULT_BASE = ":default";

/** Added and removed line counts, in the colors diffs use for them. */
export function LineCounts({ additions, deletions, className }: { additions: number; deletions: number; className?: string }) {
  return (
    <span className={cn("font-mono text-xs tabular-nums", className)}>
      <span className="text-success">+{additions}</span> <span className="text-destructive">−{deletions}</span>
    </span>
  );
}

// The scope picker's value for what is not committed; a select takes no empty value.
const WORKING = "working";

/** What one turn changed: the folder's snapshots from before and after it. */
export type TurnScope = { before: string; after: string; files: number; additions: number; deletions: number };

/**
 * What the folder a thread works in has changed: what is not committed yet, with a way to commit it,
 * or what one turn did.
 */
export function ChangesPanel({
  link,
  threadId,
  status,
  onStatus,
  onClose,
  readOnly = false,
  turns = [],
  scope,
  onScope,
  restorable = false,
  running = false,
}: {
  /** A turn is running in the thread's folder, so a pull would be refused. */
  running?: boolean;
  /** The turns that changed files, oldest first. */
  turns?: TurnScope[];
  /** The turn shown, by its after snapshot; none shows what is not committed. */
  scope?: string;
  onScope?: (after: string | undefined) => void;
  /** The thread's folder is its own and it is at rest: a turn's files may be put back as they were before it. */
  restorable?: boolean;
  /** Watching a thread someone else drives: its changes are read, not committed. */
  readOnly?: boolean;
  link: HostLink;
  threadId: string;
  status: GitStatus;
  /** Reloads the status, or takes the one a commit returned. */
  onStatus: (status?: GitStatus) => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const turn = turns.find((x) => x.after === scope);
  const [diff, setDiff] = useState<{ text: string; truncated: boolean }>();
  const [confirming, setConfirming] = useState(false);
  const [prefs, setPrefs] = useState(readPrefs);
  const setPref = (change: Partial<typeof prefs>) =>
    setPrefs((now) => {
      const next = { ...now, ...change };
      try {
        localStorage.setItem(DIFF_PREFS, JSON.stringify(next));
      } catch {
        // Kept for this panel only.
      }
      return next;
    });
  // Files folded shut, by name.
  const [folded, setFolded] = useState<Set<string>>(new Set());
  const fold = (name: string) =>
    setFolded((now) => {
      const next = new Set(now);
      if (!next.delete(name)) next.add(name);
      return next;
    });
  // The header or commit action running, so its own button spins and the rest wait.
  const [busy, setBusy] = useState<"fetch" | "pull" | "push" | "commit" | "commitPush">();
  const [switching, setSwitching] = useState<string>();
  const [branches, setBranches] = useState<string[]>([]);
  const [naming, setNaming] = useState(false);
  const [opening, setOpening] = useState(false);
  // The branch list is read again whenever the current branch changes, which is when it can have grown.
  useEffect(() => {
    link.git.listBranches({ threadId }).then((r) => setBranches(r.branches), () => setBranches([]));
  }, [link, threadId, status.branch]);
  const checkout = (branch: string) => {
    setSwitching(branch);
    link.git
      .checkout({ threadId, branch, create: false })
      .then(onStatus, showError)
      .finally(() => setSwitching(undefined));
  };

  const before = turn?.before;
  const after = turn?.after;
  const ignoreWhitespace = prefs.ignoreWhitespace;
  // A new scope starts blank; a plain status change keeps the old diff and folds until the new one lands.
  useEffect(() => {
    setDiff(undefined);
    setFolded(new Set());
  }, [link, threadId, before, after, ignoreWhitespace]);
  useEffect(() => {
    let current = true;
    (after ? link.git.getDiff({ threadId, from: before, to: after, ignoreWhitespace }) : link.git.getDiff({ threadId, path: "", ignoreWhitespace })).then(
      (r) => current && setDiff({ text: r.diff, truncated: r.truncated }),
      (e) => current && (showError(e), setDiff({ text: "", truncated: false })),
    );
    return () => {
      current = false;
    };
    // The status changes whenever the files do, so it stands in for them here.
  }, [link, threadId, status, before, after, ignoreWhitespace]);
  // One file whole, from the same two points as the diff shown, for expanding context around its hunks.
  const loadSides = useCallback(
    async (path: string) => {
      // The same whitespace setting as the whole diff, so the one-path diff matches its part of it.
      if (after) return link.git.getDiff({ threadId, path, from: before, to: after, ignoreWhitespace });
      const r = await link.git.getDiff({ threadId, path, ignoreWhitespace });
      // An agent may have edited the file since the diff was read, and its newer contents would not line up
      // with the hunks shown; the host's one-path diff is the same text as that file's part of the whole.
      if (!r.diff || !diff?.text.includes(r.diff)) {
        toast.info(t("git.changedSince"));
        onStatus();
        throw new Error(path + " changed since the diff was read");
      }
      return r;
    },
    [link, threadId, before, after, ignoreWhitespace, diff, onStatus, t],
  );
  // The files in the diff shown, to fold them all at once.
  const names = [...(diff?.text ?? "").matchAll(/^diff --git a\/.+ b\/(.+)$/gm)].map((m) => m[1]);
  const restore = () =>
    turn &&
    link.git.restore({ threadId, commit: turn.before }).then((res) => {
      onStatus(res.status);
      onScope?.(undefined);
      // The files as they were are kept; offer the way back while it is fresh.
      toast.success(t("changes.restored"), {
        duration: 15000,
        action: {
          label: t("changes.undo"),
          onClick: () => void link.git.restore({ threadId, commit: res.undo }).then((r) => (onStatus(r.status), toast.success(t("changes.undone"))), showError),
        },
      });
    }, showError);

  const commit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = event.currentTarget;
    const push = (event.nativeEvent as SubmitEvent).submitter?.getAttribute("value") === "push";
    setBusy(push ? "commitPush" : "commit");
    try {
      const res = await link.git.commit({ threadId, message: String(new FormData(form).get("message")), push });
      form.reset();
      toast.success(t(push ? "git.pushed" : "git.committed"));
      onStatus(res.status);
    } catch (e) {
      showError(e);
      // A push can fail after the commit was made; show where things stand.
      onStatus();
    } finally {
      setBusy(undefined);
    }
  };

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PullRequestDialog open={opening} onClose={() => setOpening(false)} link={link} threadId={threadId} status={status} onStatus={onStatus} branches={branches} />
      <RenameDialog open={naming} onClose={() => setNaming(false)} title={t("git.newBranch")} name="" onRename={(branch) => link.git.checkout({ threadId, branch, create: true }).then(onStatus)} />
      <header className="flex h-12 shrink-0 items-center gap-2 border-b px-3">
        {/* Branching, pushing and pull requests are the driver's; close and refresh stay. */}
        <fieldset disabled={readOnly} className="contents">
        <GitBranchIcon className="text-muted-foreground size-4 shrink-0" />
        {branches.includes(status.branch) ? (
          <Picker size="sm" className="min-w-0 border-0 font-medium shadow-none" label={t("git.branch")} value={switching ?? status.branch} onChange={(branch) => !switching && checkout(branch)} options={branches.map((b) => ({ value: b, label: b }))} />
        ) : (
          // No branch checked out, or the list has not loaded: show where HEAD is.
          <span className="truncate text-sm font-medium">{status.branch}</span>
        )}
        {switching && <Spinner className="shrink-0" />}
        <Button size="icon-sm" variant="ghost" aria-label={t("git.newBranch")} title={t("git.newBranch")} onClick={() => setNaming(true)}>
          <PlusIcon />
        </Button>
        {status.upstream && (status.ahead > 0 || status.behind > 0) && (
          <span className="text-muted-foreground flex items-center text-xs tabular-nums" title={status.upstream}>
            {status.ahead > 0 && (
              <>
                <ArrowUpIcon className="size-3" />
                {status.ahead}
              </>
            )}
            {status.behind > 0 && (
              <>
                <ArrowDownIcon className="size-3" />
                {status.behind}
              </>
            )}
          </span>
        )}
        <span className="flex-1" />
        <Button
          size="sm"
          variant="ghost"
          disabled={busy !== undefined}
          onClick={() => {
            setBusy("fetch");
            link.git
              .fetch({ threadId })
              .then((res) => (toast.success(t("git.fetched", { ahead: String(res.status?.ahead ?? 0), behind: String(res.status?.behind ?? 0) })), onStatus(res.status)), showError)
              .finally(() => setBusy(undefined));
          }}
        >
          {busy === "fetch" && <Spinner data-icon="inline-start" />}
          {t("git.fetch")}
        </Button>
        {/* Fast-forward only on the host, so nothing is merged and no confirm is needed. */}
        {status.upstream && (
          <Button
            size="sm"
            variant="outline"
            title={t("git.pullHelp")}
            disabled={busy !== undefined || running || status.behind === 0}
            onClick={() => {
              setBusy("pull");
              link.git
                .pull({ threadId })
                .then((res) => (toast.success(t("git.pulled")), onStatus(res.status)), showError)
                .finally(() => setBusy(undefined));
            }}
          >
            {busy === "pull" && <Spinner data-icon="inline-start" />}
            {t("git.pull")}
          </Button>
        )}
        {/* Commits that are not on the remote yet, or a branch the remote has never seen. */}
        {(status.ahead > 0 || !status.upstream) && (
          <Button
            size="sm"
            variant="outline"
            disabled={busy !== undefined}
            onClick={() => {
              setBusy("push");
              link.git
                .push({ threadId })
                .then((res) => (toast.success(t("git.pushDone")), onStatus(res.status)), showError)
                .finally(() => setBusy(undefined));
            }}
          >
            {busy === "push" && <Spinner data-icon="inline-start" />}
            {t("git.push")}
          </Button>
        )}
        {branches.includes(status.branch) && (
          <Button size="icon-sm" variant="ghost" aria-label={t("pr.title")} title={t("pr.title")} disabled={busy !== undefined} onClick={() => setOpening(true)}>
            <GitPullRequestIcon />
          </Button>
        )}
        </fieldset>
        <Button size="icon-sm" variant="ghost" aria-label={t("git.refresh")} title={t("git.refresh")} onClick={() => onStatus()}>
          <RefreshCwIcon />
        </Button>
        <Button size="icon-sm" variant="ghost" aria-label={t("palette.close")} title={t("palette.close")} onClick={onClose}>
          <XIcon />
        </Button>
      </header>
      {status.files.length === 0 && !turn && turns.length === 0 ? (
        <p className="text-muted-foreground m-auto px-6 text-center text-sm">{t("git.clean")}</p>
      ) : (
        <>
          {!readOnly && !turn && status.files.length > 0 && <form onSubmit={commit} className="grid gap-2 border-b p-3">
            <Input name="message" placeholder={t("git.message")} aria-label={t("git.message")} required disabled={busy !== undefined} />
            <div className="flex gap-2">
              <Button className="flex-1" type="submit" variant="outline" value="commit" disabled={busy !== undefined}>
                {busy === "commit" && <Spinner data-icon="inline-start" />}
                {t("git.commit")}
              </Button>
              <Button className="flex-1" type="submit" value="push" disabled={busy !== undefined}>
                {busy === "commitPush" && <Spinner data-icon="inline-start" />}
                {t("git.commitAndPush")}
              </Button>
            </div>
          </form>}
          {/* Every changed file at once, each folding on its own. */}
          <div className="flex h-10 shrink-0 items-center gap-1 border-b px-3">
            {/* What is shown: what is not committed yet, or one turn's changes. */}
            {turns.length > 0 ? (
              <Picker
                size="sm"
                className="min-w-0 border-0 shadow-none"
                label={t("git.scope")}
                value={turn?.after ?? WORKING}
                onChange={(v) => onScope?.(v === WORKING ? undefined : v)}
                options={[
                  { value: WORKING, label: t("git.uncommitted", { count: String(status.files.length) }) },
                  ...turns
                    .map((x, i) => ({ value: x.after, label: t(i === turns.length - 1 ? "git.latestTurn" : "git.turnScope", { n: String(i + 1), count: String(x.files) }) }))
                    .reverse(),
                ]}
              />
            ) : (
              <span className="text-muted-foreground text-xs">{t("git.allChanges", { count: String(status.files.length) })}</span>
            )}
            <LineCounts additions={turn?.additions ?? status.additions} deletions={turn?.deletions ?? status.deletions} />
            <span className="flex-1" />
            {turn && restorable && !readOnly && (
              <Button size="icon-sm" variant="ghost" aria-label={t("changes.restore")} title={t("changes.restore")} onClick={() => setConfirming(true)}>
                <Undo2Icon />
              </Button>
            )}
            <Button size="icon-sm" variant={prefs.split ? "ghost" : "secondary"} aria-pressed={!prefs.split} aria-label={t("git.unified")} title={t("git.unified")} onClick={() => setPref({ split: false })}>
              <RowsIcon />
            </Button>
            <Button size="icon-sm" variant={prefs.split ? "secondary" : "ghost"} aria-pressed={prefs.split} aria-label={t("git.split")} title={t("git.split")} onClick={() => setPref({ split: true })}>
              <ColumnsIcon />
            </Button>
            <Button size="icon-sm" variant={prefs.wrap ? "secondary" : "ghost"} aria-pressed={prefs.wrap} aria-label={t("git.wrap")} title={t("git.wrap")} onClick={() => setPref({ wrap: !prefs.wrap })}>
              <WrapTextIcon />
            </Button>
            <Button size="icon-sm" variant={prefs.hideTests ? "secondary" : "ghost"} aria-pressed={prefs.hideTests} aria-label={t("git.hideTests")} title={t("git.hideTests")} onClick={() => setPref({ hideTests: !prefs.hideTests })}>
              <FlaskConicalOffIcon />
            </Button>
            <Button size="icon-sm" variant={prefs.ignoreWhitespace ? "secondary" : "ghost"} aria-pressed={prefs.ignoreWhitespace} aria-label={t("git.ignoreWhitespace")} title={t("git.ignoreWhitespace")} onClick={() => setPref({ ignoreWhitespace: !prefs.ignoreWhitespace })}>
              <PilcrowIcon />
            </Button>
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label={t(folded.size > 0 ? "git.expandAll" : "git.collapseAll")}
              title={t(folded.size > 0 ? "git.expandAll" : "git.collapseAll")}
              onClick={() => setFolded(folded.size > 0 ? new Set() : new Set(names))}
            >
              {folded.size > 0 ? <ChevronsUpDownIcon /> : <ChevronsDownUpIcon />}
            </Button>
          </div>
          <div className="min-h-0 flex-1 overflow-auto">
            {!diff ? (
              <Spinner className="m-3" />
            ) : diff.text ? (
              <Suspense fallback={<Spinner className="m-3" />}>
                <DiffView patch={diff.text} split={prefs.split} wrap={prefs.wrap} folded={folded} onFold={fold} hideTests={prefs.hideTests} loadSides={loadSides} />
              </Suspense>
            ) : (
              <p className="text-muted-foreground px-3 py-2 text-sm">{t(!turn && status.files.length === 0 ? "git.clean" : "git.noDiff")}</p>
            )}
            {diff?.truncated && <p className="text-muted-foreground px-3 pb-2 text-xs">{t("git.truncated")}</p>}
          </div>
        </>
      )}
      <ConfirmDialog
        open={confirming}
        onClose={() => setConfirming(false)}
        title={t("changes.restoreTitle")}
        description={t("changes.restoreHelp")}
        cancel={t("project.keep")}
        confirm={t("changes.restore")}
        onConfirm={restore}
      />
    </div>
  );
}

/** Opens a pull request for the current branch; the host pushes it first when needed. */
function PullRequestDialog({
  open,
  onClose,
  link,
  threadId,
  status,
  onStatus,
  branches,
}: {
  open: boolean;
  onClose: () => void;
  link: HostLink;
  threadId: string;
  status: GitStatus;
  onStatus: (known?: GitStatus) => void;
  /** The repository's branches, to merge into. */
  branches: string[];
}) {
  const { t } = useI18n();
  const [draft, setDraft] = useState(false);
  const [base, setBase] = useState(DEFAULT_BASE);
  const [sending, setSending] = useState(false);
  const bases = [{ value: DEFAULT_BASE, label: t("pr.defaultBase") }, ...branches.filter((b) => b !== status.branch).map((b) => ({ value: b, label: b }))];
  // The dialog stays mounted, so a base chosen earlier may since be checked out or deleted; fall back then.
  const chosen = bases.some((b) => b.value === base) ? base : DEFAULT_BASE;
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    setSending(true);
    try {
      const res = await link.git.createPullRequest({ threadId, title: String(form.get("title")), body: String(form.get("body")), base: chosen === DEFAULT_BASE ? "" : chosen, draft });
      onClose();
      onStatus(res.status);
      toast.success(t(res.existing ? "pr.existing" : "pr.created"), {
        description: res.url,
        action: { label: t("pr.view"), onClick: () => window.open(res.url, "_blank", "noopener") },
      });
    } catch (e) {
      showError(e);
      // The push may have gone through before the pull request failed.
      onStatus();
    } finally {
      setSending(false);
    }
  };
  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("pr.title")}</DialogTitle>
          <DialogDescription>{t("pr.help", { branch: status.branch })}</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <Field>
            <FieldLabel htmlFor="pr-title">{t("pr.name")}</FieldLabel>
            <Input id="pr-title" name="title" autoFocus disabled={sending} />
            <FieldDescription>{t("pr.nameHelp")}</FieldDescription>
          </Field>
          <Field>
            <FieldLabel htmlFor="pr-body">{t("pr.body")}</FieldLabel>
            <Textarea id="pr-body" name="body" rows={5} disabled={sending} />
          </Field>
          {/* Picker takes no disabled prop; the fieldset disables its trigger while sending, like the fields above. */}
          <fieldset disabled={sending} className="contents">
            <Field>
              <FieldLabel>{t("pr.base")}</FieldLabel>
              <Picker label={t("pr.base")} value={chosen} onChange={setBase} options={bases} />
            </Field>
          </fieldset>
          <Label className="font-normal">
            <Checkbox checked={draft} onCheckedChange={setDraft} disabled={sending} />
            {t("pr.draft")}
          </Label>
          {status.files.length > 0 && <p className="text-muted-foreground text-sm">{t("pr.uncommitted", { count: String(status.files.length) })}</p>}
          <DialogFooter>
            <Button type="submit" disabled={sending}>
              {sending && <Spinner data-icon="inline-start" />}
              {t("pr.submit")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
