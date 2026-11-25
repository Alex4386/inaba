import { ArrowDownIcon, ArrowUpIcon, CheckIcon, PaperclipIcon, PencilIcon, SendHorizontalIcon, XIcon } from "lucide-react";
import { useState } from "react";
import type { QueuedMessage } from "@/gen/inaba/v1/host_pb";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

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
  onMove: (queuedId: string, toIndex: number) => void;
  onSendNow: (queuedId: string) => void;
  onRemove: (queuedId: string) => void;
  onResume: () => void;
}) {
  const { t } = useI18n();
  // The message being edited, and its text so far.
  const [editing, setEditing] = useState<string>();
  const [text, setText] = useState("");
  const save = (queued: QueuedMessage) => {
    // The host refuses a message left with nothing in it; say so here instead of round-tripping.
    if (!text.trim() && queued.attachments.length === 0) return;
    onEdit(queued.id, text).then(() => setEditing(undefined), showError);
  };
  if (queue.length === 0) return null;
  return (
    <ul className="grid gap-1" aria-label={t("queue.title")}>
      {queue.map((queued, i) =>
        editing === queued.id ? (
          <li key={queued.id} className="flex items-start gap-1 rounded-lg border border-dashed p-1">
            <Textarea
              autoFocus
              value={text}
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
            <Button type="button" size="icon-xs" variant="ghost" aria-label={t("common.save")} title={t("common.save")} disabled={!text.trim() && queued.attachments.length === 0} onClick={() => save(queued)}>
              <CheckIcon />
            </Button>
            <Button type="button" size="icon-xs" variant="ghost" aria-label={t("providers.cancel")} title={t("providers.cancel")} onClick={() => setEditing(undefined)}>
              <XIcon />
            </Button>
          </li>
        ) : (
          <li key={queued.id} className="text-muted-foreground flex items-center gap-1 rounded-lg border border-dashed py-1 pr-1 pl-3 text-sm">
            <span className="min-w-0 flex-1 truncate">{queued.text}</span>
            {queued.attachments.length > 0 && <PaperclipIcon className="size-3.5 shrink-0" />}
            <Button type="button" size="icon-xs" variant="ghost" aria-label={t("queue.moveUp")} title={t("queue.moveUp")} disabled={i === 0} onClick={() => onMove(queued.id, i - 1)}>
              <ArrowUpIcon />
            </Button>
            <Button type="button" size="icon-xs" variant="ghost" aria-label={t("queue.moveDown")} title={t("queue.moveDown")} disabled={i === queue.length - 1} onClick={() => onMove(queued.id, i + 1)}>
              <ArrowDownIcon />
            </Button>
            <Button
              type="button"
              size="icon-xs"
              variant="ghost"
              aria-label={t("queue.edit")}
              title={t("queue.edit")}
              onClick={() => {
                setText(queued.text);
                setEditing(queued.id);
              }}
            >
              <PencilIcon />
            </Button>
            <Button type="button" size="icon-xs" variant="ghost" aria-label={t("queue.sendNow")} title={t("queue.sendNow")} onClick={() => onSendNow(queued.id)}>
              <SendHorizontalIcon />
            </Button>
            <Button type="button" size="icon-xs" variant="ghost" aria-label={t("queue.remove")} title={t("queue.remove")} onClick={() => onRemove(queued.id)}>
              <XIcon />
            </Button>
          </li>
        ),
      )}
      {paused && (
        <li className="text-muted-foreground flex items-center gap-2 text-xs">
          {t("queue.paused")}
          <Button type="button" size="xs" variant="outline" onClick={onResume}>
            {t("queue.resume")}
          </Button>
        </li>
      )}
    </ul>
  );
}
