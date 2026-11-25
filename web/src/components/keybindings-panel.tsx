import { KeyboardIcon, PencilIcon, RotateCcwIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import { DataTable } from "@/components/data-table";
import { Badge } from "@/components/reui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { type MessageKey, useI18n } from "@/lib/i18n";
import { type Scope, type Shortcut, bindingOf, conflictOf, eventBinding, formatBinding, reserved, resetAllBindings, resetBinding, setBinding, shortcuts, useKeybindings } from "@/lib/keybindings";

const scopeKey: Record<Scope, MessageKey> = {
  global: "keys.scope.global",
  thread: "keys.scope.thread",
  composer: "keys.scope.composer",
  picker: "keys.scope.picker",
  dock: "keys.scope.dock",
};

/** Settings › Keyboard: every shortcut, with its binding to change or reset; kept in this browser. */
export function KeybindingsPanel() {
  const { t } = useI18n();
  const overrides = useKeybindings();
  const [changing, setChanging] = useState<Shortcut>();
  const label = (s: Shortcut) => t(s.label, s.values);

  return (
    <>
      <DataTable
        rows={shortcuts}
        getRowKey={(s) => s.id}
        empty={{ icon: KeyboardIcon, title: t("keys.title") }}
        search={{ placeholder: t("keys.search"), matches: (s, term) => label(s).toLowerCase().includes(term.toLowerCase()) }}
        actions={
          <Button variant="outline" size="sm" disabled={Object.keys(overrides).length === 0} onClick={resetAllBindings}>
            <RotateCcwIcon data-icon="inline-start" />
            {t("keys.resetAll")}
          </Button>
        }
        onRowClick={setChanging}
        columns={[
          { header: t("keys.action"), sortBy: label, cell: label },
          {
            header: t("keys.binding"),
            cell: (s) => (
              <span className="flex items-center gap-2">
                <kbd className="bg-muted rounded border px-1.5 py-0.5 font-sans text-xs">{formatBinding(bindingOf(s.id))}</kbd>
                {s.id in overrides && (
                  <Badge size="sm" variant="outline">
                    {t("keys.custom")}
                  </Badge>
                )}
              </span>
            ),
          },
          { header: t("keys.scope"), sortBy: (s) => s.scope, cell: (s) => <span className="text-muted-foreground">{t(scopeKey[s.scope])}</span> },
        ]}
        rowActions={[
          { label: t("keys.change"), icon: PencilIcon, onSelect: setChanging },
          {
            label: t("keys.reset"),
            icon: RotateCcwIcon,
            hidden: (s) => !(s.id in overrides),
            onSelect: (s) => {
              // Refused rather than letting two shortcuts share the default; the user moves the other one first.
              const other = resetBinding(s.id);
              if (other) toast.error(t("keys.conflict", { keys: formatBinding(s.binding), name: label(other) }));
            },
          },
        ]}
      />
      <CaptureDialog shortcut={changing} label={changing ? label(changing) : ""} onClose={() => setChanging(undefined)} />
    </>
  );
}

/** Waits for the next key combo and saves it, refusing one another shortcut in reach already uses. */
function CaptureDialog({ shortcut, label, onClose }: { shortcut?: Shortcut; label: string; onClose: () => void }) {
  const { t } = useI18n();
  useEffect(() => {
    if (!shortcut) return;
    const onKey = (e: KeyboardEvent) => {
      // Escape is left to the dialog, to cancel.
      if (e.key === "Escape") return;
      // Caught before anything else sees it, so the old shortcut does not fire while choosing a new one.
      e.preventDefault();
      e.stopPropagation();
      const binding = eventBinding(e);
      if (!binding) return;
      // A plain key outside the dock would fire while typing a message.
      if (shortcut.scope !== "dock" && !/\b(ctrl|alt|meta)\+/.test(binding)) {
        toast.error(t("keys.needsModifier"));
        return;
      }
      if (reserved(binding, shortcut.scope)) {
        toast.error(t("keys.reserved", { keys: formatBinding(binding) }));
        return;
      }
      const other = conflictOf(shortcut.id, binding);
      if (other) {
        toast.error(t("keys.conflict", { keys: formatBinding(binding), name: t(other.label, other.values) }));
        return;
      }
      setBinding(shortcut.id, binding);
      onClose();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [shortcut, onClose, t]);

  return (
    <Dialog open={!!shortcut} onOpenChange={(next) => !next && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("keys.changeTitle", { name: label })}</DialogTitle>
          <DialogDescription>{t("keys.press")}</DialogDescription>
        </DialogHeader>
        <div className="flex justify-center py-4">
          <kbd className="bg-muted rounded border px-3 py-1.5 font-sans text-lg">{shortcut && formatBinding(bindingOf(shortcut.id))}</kbd>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("keys.cancel")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
