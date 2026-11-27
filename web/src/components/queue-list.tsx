import { ArrowDownIcon, ArrowUpIcon, CheckIcon, FileIcon, PaperclipIcon, PencilIcon, SendHorizontalIcon, XIcon } from "lucide-react";
import { type DragEvent, type ReactNode, useRef, useState } from "react";
import { toast } from "sonner";
import type { Attachment, QueuedMessage } from "@/gen/inaba/v1/host_pb";
import { AttachmentImage } from "@/components/composer";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { attachmentProblem, formatBytes } from "@/lib/composer-input";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/**
 * The messages waiting for the running turn, each one editable, movable, sendable at once
 * or removable. Watchers get it inside a disabled fieldset, so every control is off for them.
 */
export function QueueList({
  queue,
  paused,
  onEdit,
  onMove,
  onSendNow,
  onRemove,
  onResume,
}: {
  queue: QueuedMessage[];
  paused: boolean;
  /**
   * Resolves once the new text is saved; a failure keeps the editor open. attachments, when
   * they changed, are the indexes of the message's own ones it keeps, the files added and
   * the names of the ones the indexes were taken against.
   */
  onEdit: (queuedId: string, text: string, attachments?: { keep: number[]; add: Attachment[]; seen: string[] }) => Promise<unknown>;
  /** toIndex 0 runs next. */
  onMove: (queuedId: string, toIndex: number) => Promise<unknown>;
  onSendNow: (queuedId: string) => Promise<unknown>;
  onRemove: (queuedId: string) => Promise<unknown>;
  onResume: () => Promise<unknown>;
}) {
  const { t, locale } = useI18n();
  // The message being edited, its text so far, which of its attachments it keeps (by index) and the files added.
  const [editing, setEditing] = useState<string>();
  const [text, setText] = useState("");
  // kept indexes into base, the attachments as they were when editing began, not the live queue.
  const [base, setBase] = useState<Attachment[]>([]);
  const [kept, setKept] = useState<number[]>([]);
  // Bumped each time an edit opens, so a file still being read for an earlier one is dropped.
  const session = useRef(0);
  const [added, setAdded] = useState<Attachment[]>([]);
  // Files still being read in; saving waits for them.
  const [reading, setReading] = useState(0);
  const [saving, setSaving] = useState(false);
  const box = useRef<HTMLTextAreaElement>(null);
  const picker = useRef<HTMLInputElement>(null);
  // The message a move, send or removal is on its way for, and which button did it; one at a time.
  const [busy, setBusy] = useState<{ id: string; what: string }>();
  const act = (id: string, what: string, call: () => Promise<unknown>) => {
    if (busy) return;
    setBusy({ id, what });
    void call().finally(() => setBusy(undefined));
  };
  const icon = (id: string, what: string, node: ReactNode) => (busy?.id === id && busy.what === what ? <Spinner /> : node);
  const empty = !text.trim() && kept.length + added.length === 0;
  const save = (queued: QueuedMessage) => {
    // The host refuses a message left with nothing in it; say so here instead of round-tripping.
    if (saving || reading > 0 || empty) return;
    const changed = added.length > 0 || kept.length !== base.length;
    setSaving(true);
    onEdit(queued.id, text, changed ? { keep: kept, add: added, seen: base.map((a) => a.name) } : undefined)
      .then(() => setEditing(undefined), showError)
      .finally(() => setSaving(false));
  };
  // As the composer adds them, checked against the same limits, counting what the message keeps.
  const addFiles = async (list: FileList | File[]) => {
    const held = [...kept.map((k) => base[k]), ...added];
    const mine = session.current;
    const count = { count: held.length, bytes: held.reduce((n, a) => n + a.data.length + Number(a.size ?? 0n), 0) };
    setReading((n) => n + 1);
    try {
      for (const file of Array.from(list)) {
        const problem = attachmentProblem(file, count);
        if (problem) {
          toast.error(t(`composer.${problem}`, { name: file.name }));
          continue;
        }
        count.count++;
        count.bytes += file.size;
        try {
          const data = new Uint8Array(await file.arrayBuffer());
          const mimeType = file.type || "application/octet-stream";
          if (session.current !== mine) return;
          setAdded((now) => [...now, { $typeName: "inaba.v1.Attachment", name: file.name, mimeType, data, path: "" }]);
        } catch (e) {
          showError(e);
        }
      }
    } finally {
      setReading((n) => n - 1);
    }
  };
  const onDrop = (e: DragEvent) => {
    if (e.dataTransfer.files.length === 0) return;
    e.preventDefault();
    // A watcher's disabled box must not collect files.
    if (saving || box.current?.matches(":disabled")) return;
    void addFiles(e.dataTransfer.files);
  };
  const chip = (a: Attachment, remove: () => void, key: string) => (
    <li key={key} className="group/image relative">
      {a.data.length > 0 ? (
        <AttachmentImage attachment={a} className={a.mimeType.startsWith("image/") ? "size-14" : undefined} />
      ) : (
        // A file the host wrote out, or one named by path: only its name and size are here.
        <span className="bg-muted/50 flex h-14 max-w-48 items-center gap-2 rounded-lg border px-2.5 text-left" title={a.path}>
          <FileIcon className="text-muted-foreground size-4 shrink-0" />
          <span className="grid min-w-0 text-xs">
            <span className="truncate">{a.name || a.path}</span>
            {a.size !== undefined && <span className="text-muted-foreground tabular-nums">{formatBytes(Number(a.size), locale)}</span>}
          </span>
        </span>
      )}
      <Button
        type="button"
        size="icon-xs"
        variant="secondary"
        className="absolute -top-1.5 -right-1.5 rounded-full border"
        aria-label={t("composer.remove", { name: a.name || a.path })}
        title={t("composer.remove", { name: a.name || a.path })}
        disabled={saving}
        onClick={remove}
      >
        <XIcon />
      </Button>
    </li>
  );
  if (queue.length === 0) return null;
  return (
    <ul className="grid grid-cols-1 gap-1" aria-label={t("queue.title")}>
      {queue.map((queued, i) =>
        editing === queued.id ? (
          <li key={queued.id} className="grid gap-1 rounded-lg border border-dashed p-1" onDragOver={(e) => e.preventDefault()} onDrop={onDrop}>
            {(kept.length > 0 || added.length > 0 || reading > 0) && (
              <ul className="flex flex-wrap gap-1.5 px-1 pt-1.5" aria-label={t("composer.attachments")}>
                {kept.map((k) => chip(base[k], () => setKept((now) => now.filter((j) => j !== k)), `kept-${k}`))}
                {added.map((a, i) => chip(a, () => setAdded((now) => now.filter((_, j) => j !== i)), `added-${i}`))}
                {reading > 0 && (
                  <li className="bg-muted flex size-14 items-center justify-center rounded-lg">
                    <Spinner className="text-muted-foreground" />
                  </li>
                )}
              </ul>
            )}
            <div className="flex items-start gap-1">
              <Textarea
                ref={box}
                autoFocus
                value={text}
                disabled={saving}
                aria-label={t("queue.editLabel")}
                className="min-h-9 flex-1 resize-none"
                onChange={(e) => setText(e.target.value)}
                onPaste={(e) => {
                  if (e.clipboardData.files.length === 0) return;
                  e.preventDefault();
                  void addFiles(e.clipboardData.files);
                }}
                onKeyDown={(e) => {
                  // A key that commits an IME syllable is not a save or a cancel.
                  if (e.nativeEvent.isComposing) return;
                  if (e.key === "Enter" && !e.shiftKey) {
                    e.preventDefault();
                    save(queued);
                  } else if (e.key === "Escape") {
                    e.preventDefault();
                    setEditing(undefined);
                  }
                }}
              />
              <input
                ref={picker}
                type="file"
                multiple
                hidden
                onChange={(e) => (void addFiles(e.target.files ?? []), (e.target.value = ""))}
              />
              <Button type="button" size="icon-xs" variant="ghost" aria-label={t("composer.attach")} title={t("composer.attach")} disabled={saving} onClick={() => picker.current?.click()}>
                <PaperclipIcon />
              </Button>
              <Button type="button" size="icon-xs" variant="ghost" aria-label={t("common.save")} title={t("common.save")} disabled={saving || reading > 0 || empty} onClick={() => save(queued)}>
                {saving ? <Spinner /> : <CheckIcon />}
              </Button>
              <Button type="button" size="icon-xs" variant="ghost" aria-label={t("providers.cancel")} title={t("providers.cancel")} onClick={() => setEditing(undefined)}>
                <XIcon />
              </Button>
            </div>
          </li>
        ) : (
          <li key={queued.id} className={cn("text-muted-foreground flex items-center gap-1 rounded-lg border border-dashed py-1 pr-1 pl-3 text-sm", busy?.id === queued.id && "opacity-60")}>
            <span className="min-w-0 flex-1 truncate">{queued.resume ? t("chat.restarted") : queued.text}</span>
            {queued.attachments.length > 0 && <PaperclipIcon className="size-3.5 shrink-0" />}
            <Button type="button" size="icon-xs" variant="ghost" aria-label={t("queue.moveUp")} title={t("queue.moveUp")} disabled={Boolean(busy) || i === 0} onClick={() => act(queued.id, "up", () => onMove(queued.id, i - 1))}>
              {icon(queued.id, "up", <ArrowUpIcon />)}
            </Button>
            <Button type="button" size="icon-xs" variant="ghost" aria-label={t("queue.moveDown")} title={t("queue.moveDown")} disabled={Boolean(busy) || i === queue.length - 1} onClick={() => act(queued.id, "down", () => onMove(queued.id, i + 1))}>
              {icon(queued.id, "down", <ArrowDownIcon />)}
            </Button>
            <Button
              type="button"
              size="icon-xs"
              variant="ghost"
              aria-label={t("queue.edit")}
              title={t("queue.edit")}
              disabled={busy?.id === queued.id}
              onClick={() => {
                setText(queued.text);
                session.current++;
                setBase(queued.attachments);
                setKept(queued.attachments.map((_, k) => k));
                setAdded([]);
                setEditing(queued.id);
              }}
            >
              <PencilIcon />
            </Button>
            <Button type="button" size="icon-xs" variant="ghost" aria-label={t("queue.sendNow")} title={t("queue.sendNow")} disabled={Boolean(busy)} onClick={() => act(queued.id, "send", () => onSendNow(queued.id))}>
              {icon(queued.id, "send", <SendHorizontalIcon />)}
            </Button>
            <Button type="button" size="icon-xs" variant="ghost" aria-label={t("queue.remove")} title={t("queue.remove")} disabled={Boolean(busy)} onClick={() => act(queued.id, "remove", () => onRemove(queued.id))}>
              {icon(queued.id, "remove", <XIcon />)}
            </Button>
          </li>
        ),
      )}
      {paused && (
        <li className="text-muted-foreground flex items-center gap-2 text-xs">
          {t("queue.paused")}
          <Button type="button" size="xs" variant="outline" disabled={Boolean(busy)} onClick={() => act("", "resume", onResume)}>
            {busy?.what === "resume" && <Spinner data-icon="inline-start" />}
            {t("queue.resume")}
          </Button>
        </li>
      )}
    </ul>
  );
}
