import {
  ArrowUpIcon,
  FolderIcon,
  GitForkIcon,
  PanelTopIcon,
  SquareTerminalIcon,
  XIcon,
} from "lucide-react";
import { type FormEvent, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import type { HostLink } from "./api";
import {
  type Attachment,
  type GitStatus,
  type Project,
  type Provider,
  ThreadMode,
} from "./gen/inaba/v1/host_pb";
import type { Host } from "./gen/inaba/v1/server_pb";
import { AttachmentImage } from "@/components/composer";
import { WhereCard } from "@/components/where-card";
import { PeopleStack, usePeople } from "@/components/presence";
import { ProjectIcon } from "@/components/project-tree";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Picker } from "@/components/picker";
import { OptionsBar } from "@/components/options-bar";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { attachmentProblem } from "@/lib/composer-input";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { shortPath } from "@/lib/utils";

/**
 * Whether a branch name looks valid to git, so a typo shows before the thread is started.
 * shortcut: a simplified `git check-ref-format --branch`, the host still has the final say when it runs git.
 */
export function validBranchName(name: string): boolean {
  return (
    name !== "" &&
    name !== "@" &&
    !/[\s~^:?*[\\\x00-\x1f\x7f]/.test(name) &&
    !/\.\.|@\{|\/\/|\/\.|^[-./]|[./]$|\.lock$|\.lock\//.test(name)
  );
}

/** Starts a thread in a project straight from its first message. */
export function NewThreadView({
  link,
  host,
  project,
  onCreated,
}: {
  link: HostLink;
  host?: Host;
  project: Project;
  onCreated: (threadId: string) => void;
}) {
  const { t } = useI18n();
  const people = usePeople();
  const [providers, setProviders] = useState<Provider[]>([]);
  // The project's defaults come first; each can still be changed here.
  const defaults = project.newThread;
  const [providerId, setProviderId] = useState(defaults?.provider ?? "");
  const [starting, setStarting] = useState(false);
  // Files still being read: a message sent now would go without them.
  const [reading, setReading] = useState(0);
  // The thread made by a send whose first message failed, so sending again uses it rather than making another.
  const made = useRef<string>(undefined);
  // Pasted or dropped files, sent with the first message as the thread's composer would.
  const [images, setImages] = useState<Attachment[]>([]);
  // Option choices made here, by option id, for the provider currently picked.
  const [chosen, setChosen] = useState<Record<string, string>>(
    defaults?.options ?? {},
  );
  // A worktree can be offered only where the project is a git repository.
  const [git, setGit] = useState<GitStatus>();
  const repository = Boolean(git?.repository);
  const [worktree, setWorktree] = useState(defaults?.worktree ?? false);
  // The new worktree's branch, empty for one the host names, and what it starts from.
  const [branch, setBranch] = useState("");
  const [base, setBase] = useState("");
  const [branches, setBranches] = useState<string[]>([]);
  const branchInvalid = branch.trim() !== "" && !validBranchName(branch.trim());
  const [info, setInfo] = useState(true);
  useEffect(() => {
    link.git
      .getStatus({ projectId: project.id })
      .then(setGit, () => setGit(undefined));
  }, [link, project.id]);
  // Read once a worktree is wanted; the current branch is the base unless another is picked.
  useEffect(() => {
    if (!worktree || !repository) return;
    link.git.listBranches({ projectId: project.id }).then(
      (r) => {
        setBranches(r.branches);
        // A detached HEAD has no current branch; "HEAD" names that commit to git and to the picker.
        setBase((b) => b || r.current || "HEAD");
      },
      () => setBranches([]),
    );
  }, [link, project.id, worktree, repository]);
  useEffect(() => {
    link.threads
      .listProviders({})
      .then((r) => setProviders(r.providers), showError);
  }, [link]);
  // Agents first: a message box is for talking to one.
  const provider =
    providers.find((p) => p.id === providerId) ??
    providers.find((p) => p.structured) ??
    providers[0];

  // As the thread's composer adds them, checked against the same limits.
  const addFiles = async (list: FileList) => {
    const held = { count: images.length, bytes: images.reduce((n, a) => n + a.data.length, 0) };
    const files = Array.from(list);
    setReading((n) => n + files.length);
    for (const file of files) {
      const problem = attachmentProblem(file, held);
      if (problem) {
        toast.error(t(`composer.${problem}`, { name: file.name }));
        setReading((n) => n - 1);
        continue;
      }
      held.count++;
      held.bytes += file.size;
      try {
        const data = new Uint8Array(await file.arrayBuffer());
        const mimeType = file.type || "application/octet-stream";
        setImages((now) => [...now, { $typeName: "inaba.v1.Attachment", name: file.name, mimeType, data, path: "" }]);
      } catch (e) {
        showError(e);
      } finally {
        setReading((n) => n - 1);
      }
    }
  };

  const start = async (text: string) => {
    if (!provider || starting || reading > 0 || (worktree && branchInvalid)) return;
    setStarting(true);
    let thread;
    try {
      thread = made.current ? { id: made.current } : await link.threads.createThread({
        provider: provider.id,
        projectId: project.id,
        mode: ThreadMode.STRUCTURED,
        // What the picker shows is what the thread starts with: the values offered, as changed here.
        options: { ...Object.fromEntries(provider.options.map((o) => [o.id, o.value])), ...chosen },
        // The toggle is hidden outside a git repository, so a default of "worktree" cannot be turned off there; drop it.
        worktree: worktree && repository ? { branch: branch.trim(), base } : undefined,
      });
    } catch (e) {
      showError(e);
      setStarting(false);
      return;
    }
    // The thread exists now. A failed first message stays here with its files, and sending again
    // goes to the same thread instead of starting a second one.
    made.current = thread.id;
    if (provider.structured && (text || images.length > 0)) {
      try {
        await link.threads.startTurn({ threadId: thread.id, text, attachments: images });
      } catch (e) {
        showError(e);
        setStarting(false);
        return;
      }
    }
    onCreated(thread.id);
  };
  const switchProvider = (id: string) => {
    setProviderId(id);
    // Another agent has other options.
    setChosen({});
  };
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const text = String(
      new FormData(event.currentTarget).get("text") ?? "",
    ).trim();
    if (text || images.length > 0 || !provider?.structured) void start(text);
  };

  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      <header className="flex h-12 shrink-0 items-center gap-2 border-b px-3">
        <SidebarTrigger />
        <ProjectIcon project={project} />
        <span className="text-muted-foreground truncate text-sm">
          {project.name}
        </span>
        <span className="text-muted-foreground">/</span>
        <h1 className="truncate text-sm font-medium">{t("thread.new")}</h1>
        {/* The same toggle a thread has, for the card saying where a thread here would run. */}
        <PeopleStack
          className="ml-auto"
          people={people.filter(
            (p) => p.projectId === project.id && !p.threadId,
          )}
        />
        <Button
          className={
            people.some((p) => p.projectId === project.id && !p.threadId)
              ? undefined
              : "ml-auto"
          }
          size="icon-sm"
          variant={info ? "secondary" : "ghost"}
          aria-pressed={info}
          aria-label={t("thread.info")}
          title={t("thread.info")}
          onClick={() => setInfo((v) => !v)}
        >
          <PanelTopIcon />
        </Button>
      </header>
      {info && (
        <WhereCard
          host={host}
          cwd={project.cwd}
          git={git}
          className="absolute top-14 right-3 z-10"
        />
      )}
      <form
        onSubmit={submit}
        className="mx-auto flex w-full max-w-3xl flex-1 flex-col justify-center gap-6 px-4 pb-24"
      >
        <div className="grid gap-1 text-center">
          <h2 className="text-xl font-medium tracking-tight text-balance sm:text-2xl">
            {t("new.heading", { name: project.name })}
          </h2>
          <p className="text-muted-foreground text-sm">
            {shortPath(project.cwd)}
          </p>
        </div>
        <div
          className="bg-card focus-within:ring-ring/50 grid gap-2 rounded-xl border p-2 shadow-xs focus-within:ring-3"
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => {
            if (e.dataTransfer.files.length === 0) return;
            e.preventDefault();
            if (provider?.structured && !starting) void addFiles(e.dataTransfer.files);
          }}
        >
          {provider?.structured && (images.length > 0 || reading > 0) && (
            <ul className="flex flex-wrap gap-1.5 px-1 pt-1" aria-label={t("composer.attachments")}>
              {images.map((image, i) => (
                <li key={i} className="relative">
                  <AttachmentImage attachment={image} className={image.mimeType.startsWith("image/") ? "size-14" : undefined} />
                  <Button
                    type="button"
                    size="icon-xs"
                    variant="secondary"
                    className="absolute -top-1.5 -right-1.5 rounded-full border"
                    aria-label={t("composer.remove", { name: image.name })}
                    title={t("composer.remove", { name: image.name })}
                    disabled={starting}
                    onClick={() => setImages((now) => now.filter((_, j) => j !== i))}
                  >
                    <XIcon />
                  </Button>
                </li>
              ))}
              {Array.from({ length: reading }, (_, i) => (
                <li key={`reading-${i}`} className="bg-muted flex size-14 items-center justify-center rounded-lg border">
                  <Spinner />
                </li>
              ))}
            </ul>
          )}
          {provider?.structured ? (
            <Textarea
              name="text"
              autoFocus
              disabled={starting}
              onPaste={(e) => {
                if (e.clipboardData.files.length === 0) return;
                e.preventDefault();
                void addFiles(e.clipboardData.files);
              }}
              placeholder={t("chat.placeholder")}
              aria-label={t("chat.message")}
              rows={3}
              className="max-h-64 resize-none border-0 bg-transparent shadow-none focus-visible:ring-0 dark:bg-transparent"
              onKeyDown={(e) => {
                if (
                  e.key === "Enter" &&
                  !e.shiftKey &&
                  !e.nativeEvent.isComposing
                ) {
                  e.preventDefault();
                  e.currentTarget.form?.requestSubmit();
                }
              }}
            />
          ) : (
            <p className="text-muted-foreground px-2.5 py-3 text-sm">
              {t("new.terminalHelp")}
            </p>
          )}
          <div className="flex flex-wrap items-center gap-1.5">
            {repository && (
              <Button
                type="button"
                size="sm"
                variant={worktree ? "secondary" : "ghost"}
                aria-pressed={worktree}
                title={t("git.worktreeHelp")}
                onClick={() => setWorktree((v) => !v)}
              >
                {/* Says where the thread will work, not what clicking does. */}
                {worktree ? <GitForkIcon data-icon="inline-start" /> : <FolderIcon data-icon="inline-start" />}
                {t(worktree ? "git.worktree" : "git.projectFolder")}
              </Button>
            )}
            {/* By the send button: the model picker, whose side switches providers, whether they have models or not. */}
            <div className="ml-auto flex min-w-0 items-center gap-1.5">
              {/* What this agent offered the last time it ran here; applied once the new thread is up. */}
              {provider && (
                <OptionsBar
                  options={provider.options.map((o) => ({
                    ...o,
                    value: chosen[o.id] ?? o.value,
                  }))}
                  onSet={(id, value) =>
                    setChosen((c) => ({ ...c, [id]: value }))
                  }
                  providers={providers.map((p) => ({
                    id: p.id,
                    name: p.displayName || p.id,
                    icon: p.icon,
                    terminal: !p.structured,
                  }))}
                  provider={provider.id}
                  onProvider={switchProvider}
                />
              )}
              {provider?.structured ? (
                <Button
                  type="submit"
                  size="icon"
                  disabled={starting || reading > 0 || (worktree && branchInvalid)}
                  aria-label={t("chat.send")}
                  title={t("chat.send")}
                >
                  {starting ? <Spinner /> : <ArrowUpIcon />}
                </Button>
              ) : (
                <Button type="submit" disabled={starting || !provider || (worktree && branchInvalid)}>
                  {starting ? <Spinner data-icon="inline-start" /> : <SquareTerminalIcon data-icon="inline-start" />}
                  {t("new.openTerminal")}
                </Button>
              )}
            </div>
          </div>
          {repository && worktree && (
            <div className="grid gap-1 border-t px-1 pt-2">
              <div className="flex flex-wrap items-center gap-1.5">
                <Input
                  value={branch}
                  onChange={(e) => setBranch(e.target.value)}
                  // Enter that commits an IME composition must not submit the form, as in the message box.
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && e.nativeEvent.isComposing) e.preventDefault();
                  }}
                  placeholder={t("new.branchPlaceholder")}
                  aria-label={t("new.branchName")}
                  aria-invalid={branchInvalid || undefined}
                  aria-describedby={branchInvalid ? "new-thread-branch-error" : undefined}
                  spellCheck={false}
                  autoCapitalize="off"
                  className="h-7 w-auto min-w-40 flex-1 md:text-xs"
                />
                <span className="text-muted-foreground text-xs">{t("new.branchFrom")}</span>
                {/* Until the list arrives the base stays empty, which the host reads as the current commit. */}
                {branches.length > 0 && (
                  <Picker
                    size="sm"
                    value={base}
                    onChange={setBase}
                    options={(branches.includes(base) ? branches : [base, ...branches]).map((b) => ({ value: b, label: b }))}
                    label={t("new.baseBranch")}
                    className="max-w-48"
                  />
                )}
              </div>
              {branchInvalid && (
                <p id="new-thread-branch-error" role="alert" className="text-destructive px-1 text-xs">
                  {t("new.branchInvalid")}
                </p>
              )}
            </div>
          )}
        </div>
      </form>
    </div>
  );
}
