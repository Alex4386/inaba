import { ArrowDownIcon, ArrowUpIcon, CheckIcon, PaperclipIcon, PencilIcon, SendHorizontalIcon, XIcon } from "lucide-react";
import { type ReactNode, useState } from "react";
import type { QueuedMessage } from "@/gen/inaba/v1/host_pb";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
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
  /** Resolves once the new text is saved; a failure keeps the editor open. */
  onEdit: (queuedId: string, text: string) => Promise<unknown>;
  /** toIndex 0 runs next. */
  onMove: (queuedId: string, toIndex: number) => Promise<unknown>;
  onSendNow: (queuedId: string) => Promise<unknown>;
  onRemove: (queuedId: string) => Promise<unknown>;
  onResume: () => Promise<unknown>;
}) {
  const { t } = useI18n();
  // The message being edited, and its text so far.
  const [editing, setEditing] = useState<string>();
  const [text, setText] = useState("");
  const [saving, setSaving] = useState(false);
  // The message a move, send or removal is on its way for, and which button did it; one at a time.
  const [busy, setBusy] = useState<{ id: string; what: string }>();
  const act = (id: string, what: string, call: () => Promise<unknown>) => {
    if (busy) return;
    setBusy({ id, what });
    void call().finally(() => setBusy(undefined));
  };
  const icon = (id: string, what: string, node: ReactNode) => (busy?.id === id && busy.what === what ? <Spinner /> : node);
  const save = (queued: QueuedMessage) => {
    // The host refuses a message left with nothing in it; say so here instead of round-tripping.
    if (saving || (!text.trim() && queued.attachments.length === 0)) return;
    setSaving(true);
    onEdit(queued.id, text)
      .then(() => setEditing(undefined), showError)
      .finally(() => setSaving(false));
  };
  if (queue.length === 0) return null;
  return (
    <ul className="grid grid-cols-1 gap-1" aria-label={t("queue.title")}>
      {queue.map((queued, i) =>
        editing === queued.id ? (
          <li key={queued.id} className="flex items-start gap-1 rounded-lg border border-dashed p-1">
            <Textarea
              autoFocus
              value={text}
              disabled={saving}
              aria-label={t("queue.editLabel")}
              className="min-h-9 flex-1 resize-none"
              onChange={(e) => setText(e.target.value)}
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
            <Button type="button" size="icon-xs" variant="ghost" aria-label={t("common.save")} title={t("common.save")} disabled={saving || (!text.trim() && queued.attachments.length === 0)} onClick={() => save(queued)}>
              {saving ? <Spinner /> : <CheckIcon />}
            </Button>
            <Button type="button" size="icon-xs" variant="ghost" aria-label={t("providers.cancel")} title={t("providers.cancel")} onClick={() => setEditing(undefined)}>
              <XIcon />
            </Button>
          </li>
        ) : (
          <li key={queued.id} className={cn("text-muted-foreground flex items-center gap-1 rounded-lg border border-dashed py-1 pr-1 pl-3 text-sm", busy?.id === queued.id && "opacity-60")}>
            <span className="min-w-0 flex-1 truncate">{queued.text}</span>
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
