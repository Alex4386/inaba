import { timestampDate } from "@bufbuild/protobuf/wkt";
import { ArrowUpIcon, AtSignIcon, FileIcon, ImageIcon, OctagonXIcon, PaperclipIcon, SlashIcon, SparklesIcon, SquareIcon, TriangleAlertIcon, XIcon } from "lucide-react";
import { type ClipboardEvent, type DragEvent, type KeyboardEvent, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import type { HostLink } from "@/api";
import { OptionsBar, type PickerProvider } from "@/components/options-bar";
import { Button } from "@/components/ui/button";
import { Command, CommandGroup, CommandItem, CommandList } from "@/components/ui/command";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import type { Attachment, Thread, Usage } from "@/gen/inaba/v1/host_pb";
import { attachmentProblem, type BuiltinCommand, builtinCommands, commandTakesText, formatBytes, parseBuiltin } from "@/lib/composer-input";
import { loadDraft, loadHistory, pushHistory, recallList, recallStep, saveDraft } from "@/lib/drafts";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { bindingOf, formatBinding, matches } from "@/lib/keybindings";
import { cn } from "@/lib/utils";

const noBuiltins: readonly BuiltinCommand[] = [];
// shortcut: the host sets no limit on message text, so this is a guess at what agents take in one prompt; use the host's limit once it has one.
const maxPromptChars = 200_000;
// Past this the length is shown, so a paste that went wrong is noticed before it is sent.
const warnPromptChars = 50_000;

/** What the composer holds: text, and files (images or not) not yet sent. */
/** What the composer is set to hold; without images, the attachments already there stay. A restore adds to what is there instead. */
export type Draft = { text: string; images?: Attachment[]; restore?: boolean };

type Suggestion = { key: string; label: string; detail?: string; insert: string; path?: string; builtin?: BuiltinCommand };

/** The token being typed at the caret that asks for suggestions: "/name" at the start, or "@path" anywhere. */
function trigger(text: string, caret: number): { kind: "command" | "file"; query: string; start: number } | undefined {
  const before = text.slice(0, caret);
  const command = /^\/(\S*)$/.exec(before);
  if (command) return { kind: "command", query: command[1], start: 0 };
  const file = /(^|\s)@([^\s@]*)$/.exec(before);
  if (file) return { kind: "file", query: file[2], start: before.length - file[2].length - 1 };
}

/** The message box: text with / commands and @ file suggestions, attached images, the agent's options, and send and stop. */
export function Composer({
  link,
  thread,
  busy,
  draft,
  onSend,
  onStop,
  onSetOption,
  mentions = true,
  provider,
  providers,
  onProvider,
  pastPrompts = [],
  onCommand,
}: {
  /**
   * Runs inaba's own slash commands (/side, /btw, /clear, /model, /compact) instead of sending them as a prompt.
   * They are offered only when this is given; /compact is the thread view's to pass on to the agent.
   */
  onCommand?: (command: BuiltinCommand, arg: string) => void;
  /** The thread's user messages, newest first, so ↑ recalls them too. */
  pastPrompts?: string[];
  /** The thread's provider, shown in the model picker. */
  provider?: PickerProvider;
  /** Providers the thread can move to; offered while it is idle. */
  providers?: PickerProvider[];
  onProvider?: (id: string) => unknown;
  link: HostLink;
  thread: Thread;
  busy: boolean;
  /** Whether @ looks up files to point the agent at: the host's files module is on. */
  mentions?: boolean;
  /** Replaces what the composer holds when it changes, as after a rewind. */
  draft?: Draft;
  onSend: (text: string, attachments: Attachment[], interrupt: boolean) => void;
  /** Resolves once the host has the stop. */
  onStop: (force: boolean) => Promise<unknown>;
  onSetOption: (optionId: string, value: string) => unknown;
}) {
  const { t, locale } = useI18n();
  const box = useRef<HTMLTextAreaElement>(null);
  const picker = useRef<HTMLInputElement>(null);
  // The thread view is remounted per thread, so the saved draft is read once here.
  const [text, setText] = useState(() => loadDraft(thread.id));
  // shortcut: attached files stay in memory only and are lost on reload or thread switch; persist them (IndexedDB) if people miss them.
  const [images, setImages] = useState<Attachment[]>([]);
  // Which sent prompt ArrowUp brought back, newest first; undefined while the draft is the user's own.
  const [recalled, setRecalled] = useState<number>();
  // Measured as sent, so the counter and the send guard agree.
  const length = text.trim().length;
  const tooLong = length > maxPromptChars;
  const compact = new Intl.NumberFormat(locale, { notation: "compact" });
  const latest = useRef({ id: thread.id, text });
  latest.current = { id: thread.id, text };
  useEffect(() => {
    // Writing on every key would be wasted work on long texts; a pause is enough.
    const timer = setTimeout(() => saveDraft(thread.id, text), 300);
    return () => clearTimeout(timer);
  }, [thread.id, text]);
  useEffect(() => {
    // Typing right before a reload or a thread switch would otherwise miss the pause.
    const flush = () => saveDraft(latest.current.id, latest.current.text);
    window.addEventListener("pagehide", flush);
    return () => (window.removeEventListener("pagehide", flush), flush());
  }, []);
  const [caret, setCaret] = useState(0);
  const [files, setFiles] = useState<string[]>([]);
  const [active, setActive] = useState("");
  // Closed with Escape until the token being typed changes.
  const [dismissed, setDismissed] = useState<string>();
  // Whether Stop was pressed for the turn still running; the button then offers to force it.
  const [stopping, setStopping] = useState(false);
  // Set while the stop is on its way, so it is not pressed again meanwhile.
  const [requesting, setRequesting] = useState(false);
  // Reset when a turn starts too: a stop that comes back after its turn ended must not carry into the next.
  useEffect(() => setStopping(false), [busy]);
  useEffect(() => {
    if (!draft) return;
    // A message that failed to send comes back ahead of whatever was typed since, which stays.
    if (draft.restore) {
      setText((now) => (now ? `${draft.text}\n\n${now}` : draft.text));
      setImages((now) => [...(draft.images ?? []), ...now]);
    } else {
      setText(draft.text);
      if (draft.images) setImages(draft.images);
    }
    setRecalled(undefined);
    box.current?.focus();
  }, [draft]);

  const found = trigger(text, caret);
  const typing = found?.kind === "file" && !mentions ? undefined : found;
  const token = typing && `${typing.kind}:${typing.start}:${typing.query}`;
  useEffect(() => {
    if (typing?.kind !== "file") return;
    // Wait for a pause in typing before asking the host.
    const timer = setTimeout(() => link.files.searchFiles({ threadId: thread.id, query: typing.query }).then((r) => setFiles(r.paths), showError), 120);
    return () => clearTimeout(timer);
  }, [link, thread.id, typing?.kind, typing?.query]);

  // Ours win a name clash with the agent's: the thread view hands /compact on to the agent when it has one.
  const builtins = onCommand ? builtinCommands : noBuiltins;
  const suggestions = useMemo<Suggestion[]>(() => {
    if (!typing || token === dismissed) return [];
    if (typing.kind === "command") {
      const q = typing.query.toLowerCase();
      const byPrefix = (a: string, b: string) => Number(!a.toLowerCase().startsWith(q)) - Number(!b.toLowerCase().startsWith(q));
      const ours = builtins
        .filter((c) => c.includes(q))
        .sort(byPrefix)
        .map((c) => ({ key: `inaba:/${c}`, label: `/${c}`, detail: t(`composer.cmd.${c}`), insert: `/${c} `, builtin: c }));
      const theirs = thread.commands
        .filter((c) => c.name.toLowerCase().includes(q) && !(builtins as readonly string[]).includes(c.name))
        .sort((a, b) => byPrefix(a.name, b.name))
        .map((c) => ({ key: `/${c.name}`, label: `/${c.name}`, detail: c.hint ? `${c.description} · ${c.hint}` : c.description, insert: `/${c.name} ` }));
      return [...ours, ...theirs];
    }
    return files.map((path) => ({ key: `@${path}`, label: path, insert: `@${path} `, path }));
  }, [typing?.kind, typing?.query, token, dismissed, thread.commands, files, builtins, t]);
  const shown = suggestions.length > 0;
  useEffect(() => {
    if (shown && !suggestions.some((s) => s.key === active)) setActive(suggestions[0].key);
  }, [shown, suggestions, active]);

  // A built-in command is done here and never reaches the agent or the recall history; `rest` is draft text that was not part of it.
  const runCommand = (command: BuiltinCommand, arg: string, rest = "") => {
    onCommand?.(command, arg);
    saveDraft(thread.id, rest);
    setRecalled(undefined);
    setText(rest);
    setCaret(0);
    requestAnimationFrame(() => box.current?.setSelectionRange(0, 0));
  };

  const pick = (s: Suggestion) => {
    if (!typing) return;
    // Commands with nothing to type after them run when picked, taking only the "/name" typed so far: text after the caret is the user's draft.
    if (s.builtin && !commandTakesText[s.builtin]) return runCommand(s.builtin, "", text.slice(0, typing.start) + text.slice(caret).trimStart());
    const next = text.slice(0, typing.start) + s.insert + text.slice(caret);
    const at = typing.start + s.insert.length;
    setText(next);
    setCaret(at);
    requestAnimationFrame(() => {
      box.current?.focus();
      box.current?.setSelectionRange(at, at);
    });
  };

  // Files still being read in; sending waits for them, or a screenshot pasted just before Enter would be left behind.
  const [reading, setReading] = useState(0);
  const addFiles = async (list: FileList | File[]) => {
    // Counted as files are accepted, since state set in this loop is not visible until it ends.
    // shortcut: two pastes/drops in flight both count from the same `images`, so together they can pass the caps; the host still refuses at send. Serialize addFiles if that bites.
    const held = { count: images.length, bytes: images.reduce((n, a) => n + a.data.length, 0) };
    setReading((n) => n + 1);
    try {
      for (const file of Array.from(list)) {
        const problem = attachmentProblem(file, held);
        if (problem) {
          toast.error(t(`composer.${problem}`, { name: file.name }));
          continue;
        }
        held.count++;
        held.bytes += file.size;
        try {
          const data = new Uint8Array(await file.arrayBuffer());
          // The host tells images from other files by the type, so an unknown type must not be left empty.
          const mimeType = file.type || "application/octet-stream";
          setImages((now) => [...now, { $typeName: "inaba.v1.Attachment", name: file.name, mimeType, data, path: "" }]);
        } catch (e) {
          showError(e);
        }
      }
    } finally {
      setReading((n) => n - 1);
    }
  };

  const send = (interrupt: boolean) => {
    if (reading > 0) return;
    const builtin = parseBuiltin(text, builtins);
    if (builtin) return runCommand(builtin.command, builtin.arg);
    const message = text.trim();
    if (!message && images.length === 0) return;
    if (message.length > maxPromptChars) {
      toast.error(t("composer.tooLong", { max: maxPromptChars.toLocaleString(locale) }));
      return;
    }
    if (message) pushHistory(thread.id, message);
    saveDraft(thread.id, "");
    setRecalled(undefined);
    // Files the message still names, each once.
    const named = [...new Set([...message.matchAll(/(?:^|\s)@([^\s@]+)/g)].map((m) => m[1]))];
    const mentions = named.map((path): Attachment => ({ $typeName: "inaba.v1.Attachment", name: path, mimeType: "", data: new Uint8Array(), path }));
    onSend(message, [...images, ...mentions], interrupt);
    setText("");
    setImages([]);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing) return;
    if (shown) {
      const i = suggestions.findIndex((s) => s.key === active);
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        const step = e.key === "ArrowDown" ? 1 : -1;
        setActive(suggestions[(i + step + suggestions.length) % suggestions.length].key);
        return;
      }
      if ((e.key === "Enter" && !e.shiftKey) || e.key === "Tab") {
        e.preventDefault();
        pick(suggestions[Math.max(0, i)]);
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        setDismissed(token);
        return;
      }
    }
    // Storage is read only when recall can happen, not on every caret move through long text.
    if ((e.key === "ArrowUp" || e.key === "ArrowDown") && !e.shiftKey && !e.altKey && !e.metaKey && !e.ctrlKey && (text === "" || recalled !== undefined)) {
      const history = recallList(loadHistory(thread.id), pastPrompts);
      const { selectionStart: from, selectionEnd: to } = e.currentTarget;
      // Recall only from an empty box or an untouched recalled prompt, and only at its edge, so arrows still move through text.
      const untouched = text === "" || (recalled !== undefined && text === history[recalled]);
      const atEdge = e.key === "ArrowUp" ? !text.slice(0, from).includes("\n") : !text.slice(to).includes("\n");
      const step = untouched && atEdge ? recallStep(history, recalled, e.key === "ArrowUp" ? "older" : "newer") : undefined;
      if (step) {
        e.preventDefault();
        setRecalled(step.index);
        setText(step.text);
        setCaret(step.text.length);
        requestAnimationFrame(() => box.current?.setSelectionRange(step.text.length, step.text.length));
        return;
      }
    }
    // The interrupt shortcut (Cmd/Ctrl+Enter unless changed) cuts in instead of waiting its turn.
    if (matches(e.nativeEvent, "interrupt")) {
      e.preventDefault();
      send(busy);
    } else if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      send(false);
    }
  };

  const onPaste = (e: ClipboardEvent) => {
    if (e.clipboardData.files.length === 0) return;
    e.preventDefault();
    void addFiles(e.clipboardData.files);
  };
  const onDrop = (e: DragEvent) => {
    if (e.dataTransfer.files.length === 0) return;
    e.preventDefault();
    // The drop target sits outside the driver lock's fieldset; a watcher's disabled box must not collect files.
    if (box.current?.matches(":disabled")) return;
    void addFiles(e.dataTransfer.files);
  };
  const ours = suggestions.filter((s) => s.builtin);
  const theirs = suggestions.filter((s) => !s.builtin);
  const item = (s: Suggestion) => (
    <CommandItem key={s.key} value={s.key} onSelect={() => pick(s)} onMouseDown={(e) => e.preventDefault()}>
      {s.builtin ? <SparklesIcon /> : typing?.kind === "command" ? <SlashIcon /> : <AtSignIcon />}
      <span className={cn("shrink-0", s.path && "min-w-0 truncate [direction:rtl]")}>
        <bdi>{s.label}</bdi>
      </span>
      {s.detail && <span className="text-muted-foreground min-w-0 truncate text-xs">{s.detail}</span>}
    </CommandItem>
  );

  return (
    <div className="relative" onDragOver={(e) => e.preventDefault()} onDrop={onDrop}>
      {shown && (
        <Command className="absolute inset-x-0 bottom-full z-10 mb-2 h-auto rounded-xl border shadow-md" value={active} onValueChange={setActive} shouldFilter={false}>
          <CommandList className="max-h-64">
            {ours.length > 0 && <CommandGroup heading={t("composer.inabaCommands")}>{ours.map(item)}</CommandGroup>}
            {theirs.length > 0 && <CommandGroup heading={t(typing?.kind === "command" ? "composer.commands" : "composer.files")}>{theirs.map(item)}</CommandGroup>}
          </CommandList>
        </Command>
      )}
      <div className="bg-card focus-within:ring-ring/50 grid gap-1 rounded-xl border p-2 shadow-xs focus-within:ring-3">
        {(images.length > 0 || reading > 0) && (
          <ul className="flex flex-wrap gap-1.5 px-1 pt-1" aria-label={t("composer.attachments")}>
            {images.map((image, i) => (
              <li key={i} className="group/image relative">
                <AttachmentImage attachment={image} className={image.mimeType.startsWith("image/") ? "size-14" : undefined} />
                <Button
                  type="button"
                  size="icon-xs"
                  variant="secondary"
                  className="absolute -top-1.5 -right-1.5 rounded-full border"
                  aria-label={t("composer.remove", { name: image.name })}
                  title={t("composer.remove", { name: image.name })}
                  onClick={() => setImages((now) => now.filter((_, j) => j !== i))}
                >
                  <XIcon />
                </Button>
              </li>
            ))}
            {reading > 0 && (
              <li className="bg-muted flex size-14 items-center justify-center rounded-lg">
                <Spinner className="text-muted-foreground" />
              </li>
            )}
          </ul>
        )}
        <Textarea
          ref={box}
          name="text"
          value={text}
          placeholder={busy ? t("chat.placeholderBusy") : t("chat.placeholder")}
          aria-label={t("chat.message")}
          rows={1}
          className="max-h-48 min-h-9 resize-none border-0 bg-transparent shadow-none focus-visible:ring-0 dark:bg-transparent"
          onChange={(e) => {
            setText(e.target.value);
            setCaret(e.target.selectionStart);
            // Edited text is the user's own again, so the next ArrowUp starts from the newest prompt.
            setRecalled(undefined);
          }}
          onSelect={(e) => setCaret(e.currentTarget.selectionStart)}
          onKeyDown={onKeyDown}
          onPaste={onPaste}
        />
        <div className="flex items-center gap-1.5">
          <input ref={picker} type="file" multiple hidden onChange={(e) => (void addFiles(e.target.files ?? []), (e.target.value = ""))} />
          <Button type="button" size="icon-sm" variant="ghost" aria-label={t("composer.attach")} title={t("composer.attach")} onClick={() => picker.current?.click()}>
            <PaperclipIcon />
          </Button>
          {/* The model and its settings sit by the send button, where the eye ends up before sending. */}
          <div className="ml-auto flex min-w-0 items-center gap-1.5">
            {/* Another provider takes over only between turns; mid-turn the picker keeps to the thread's own. */}
            <OptionsBar options={thread.options} onSet={onSetOption} providers={!busy && providers?.length ? providers : provider && [provider]} provider={provider?.id} onProvider={busy ? undefined : onProvider} />
            {thread.usage && thread.usage.size > 0n && <ContextMeter usage={thread.usage} />}
            {thread.usage && <LimitWarning usage={thread.usage} />}
            {busy && (
              <Button
                type="button"
                size="icon"
                variant="destructive"
                aria-label={stopping ? t("chat.forceStop") : t("chat.stop")}
                title={stopping ? t("chat.forceStopHelp") : t("chat.stop")}
                disabled={requesting}
                onClick={() => {
                  setRequesting(true);
                  void onStop(stopping)
                    .then(() => setStopping(true))
                    .finally(() => setRequesting(false));
                }}
              >
                {requesting ? <Spinner /> : stopping ? <OctagonXIcon /> : <SquareIcon />}
              </Button>
            )}
            {length > warnPromptChars && (
              <span className={cn("text-xs tabular-nums", tooLong ? "text-destructive" : "text-muted-foreground")} title={t("composer.length", { count: length.toLocaleString(locale), max: maxPromptChars.toLocaleString(locale) })}>
                {/* Over the limit the exact count shows, since compact rounding would read as just at it. */}
                <span aria-hidden>
                  {tooLong ? length.toLocaleString(locale) : compact.format(length)}/{compact.format(maxPromptChars)}
                </span>
                <span className="sr-only">{t("composer.length", { count: length.toLocaleString(locale), max: maxPromptChars.toLocaleString(locale) })}</span>
              </span>
            )}
            {/* Kept mounted so screen readers announce crossing the limit; a live region added with its text is often missed. */}
            <span className="sr-only" aria-live="polite">
              {tooLong ? t("composer.tooLong", { max: maxPromptChars.toLocaleString(locale) }) : ""}
            </span>
            <Button
              type="button"
              size="icon"
              disabled={tooLong || reading > 0}
              aria-label={tooLong ? t("composer.tooLong", { max: maxPromptChars.toLocaleString(locale) }) : busy ? t("chat.queue") : t("chat.send")}
              title={tooLong ? t("composer.tooLong", { max: maxPromptChars.toLocaleString(locale) }) : busy ? t("chat.queueHelp", { keys: formatBinding(bindingOf("interrupt")) }) : t("chat.send")}
              onClick={() => send(false)}
            >
              <ArrowUpIcon />
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}

/** The agent's fullest limit window once it passes 90%, so a turn that would hit it is not a surprise. */
function LimitWarning({ usage }: { usage: Usage }) {
  const { t, locale } = useI18n();
  const fullest = usage.limits.reduce<Usage["limits"][number] | undefined>((a, l) => (Number.isFinite(l.usedFraction) && l.usedFraction >= 0.9 && (!a || l.usedFraction > a.usedFraction) ? l : a), undefined);
  if (!fullest) return null;
  const percent = String(Math.round(Math.min(1, fullest.usedFraction) * 100));
  const resets = fullest.resetsAt ? ` · ${t("usage.resets", { when: timestampDate(fullest.resetsAt).toLocaleString(locale) })}` : "";
  const label = t("composer.limitNear", { name: fullest.name, percent }) + resets;
  return (
    <span className="text-destructive flex items-center gap-1 text-xs tabular-nums" title={label} aria-label={label} role="img">
      <TriangleAlertIcon className="size-3.5" />
      <span className="hidden @md:inline">{percent}%</span>
    </span>
  );
}

/** How full the agent's context window is, as a ring, with the numbers on hover. */
function ContextMeter({ usage }: { usage: Usage }) {
  const { t, locale } = useI18n();
  const share = Math.min(1, Number(usage.used) / Number(usage.size));
  const number = new Intl.NumberFormat(locale, { notation: "compact" });
  const cost = usage.cost > 0 ? ` · ${new Intl.NumberFormat(locale, { style: "currency", currency: usage.currency || "USD" }).format(usage.cost)}` : "";
  const label = t("composer.context", { percent: String(Math.round(share * 100)), used: number.format(usage.used), size: number.format(usage.size) }) + cost;
  const r = 7;
  const around = 2 * Math.PI * r;
  return (
    <span className="text-muted-foreground flex items-center gap-1 text-xs tabular-nums" title={label} aria-label={label} role="img">
      <svg viewBox="0 0 18 18" className="size-4 -rotate-90">
        <circle cx="9" cy="9" r={r} fill="none" strokeWidth="2" className="stroke-muted" />
        <circle cx="9" cy="9" r={r} fill="none" strokeWidth="2" strokeDasharray={around} strokeDashoffset={around * (1 - share)} className={share > 0.8 ? "stroke-destructive" : "stroke-foreground/60"} />
      </svg>
      <span className="hidden @md:inline">{Math.round(share * 100)}%</span>
    </span>
  );
}

/** An attached image, shown from its bytes; any other file shows as a chip with its name and size. */
export function AttachmentImage({ attachment, className }: { attachment: Attachment; className?: string }) {
  const { locale } = useI18n();
  const url = useMemo(() => URL.createObjectURL(new Blob([attachment.data as BlobPart], { type: attachment.mimeType })), [attachment]);
  useEffect(() => () => URL.revokeObjectURL(url), [url]);
  if (attachment.data.length > 0 && !attachment.mimeType.startsWith("image/"))
    return (
      <span className={cn("bg-muted/50 flex h-14 max-w-48 items-center gap-2 rounded-lg border px-2.5 text-left", className)} title={attachment.name}>
        <FileIcon className="text-muted-foreground size-4 shrink-0" />
        <span className="grid min-w-0 text-xs">
          <span className="truncate">{attachment.name}</span>
          <span className="text-muted-foreground tabular-nums">{formatBytes(attachment.data.length, locale)}</span>
        </span>
      </span>
    );
  return attachment.data.length > 0 ? (
    <img src={url} alt={attachment.name} title={attachment.name} className={cn("rounded-lg border object-cover", className)} />
  ) : (
    <span className={cn("bg-muted flex items-center justify-center rounded-lg", className)}>
      <ImageIcon className="text-muted-foreground size-4" />
    </span>
  );
}
