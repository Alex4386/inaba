import { type FormEvent, type ReactNode, useState } from "react";
import { CheckIcon, CopyIcon } from "lucide-react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

/** A secret shown once, with a button to copy it. */
export function Secret({ value }: { value: string }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  const copy = () =>
    navigator.clipboard.writeText(value).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    }, showError);
  // The copy button sits in the box, beside what it copies.
  return (
    <div className="bg-muted flex items-start gap-2 rounded-md py-1.5 pr-1.5 pl-3">
      <pre className="min-w-0 flex-1 py-1.5 font-mono text-xs break-all whitespace-pre-wrap">{value}</pre>
      <Button size="icon-sm" variant="ghost" className="shrink-0" aria-label={t("common.copy")} title={t(copied ? "common.copied" : "common.copy")} onClick={copy}>
        {copied ? <CheckIcon /> : <CopyIcon />}
      </Button>
    </div>
  );
}

/** Asks for one name, then shows the secret the server made for it, once. */
export function CreateSecretDialog({
  open,
  onClose,
  title,
  description,
  create,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  description: string;
  /** Creates the thing and returns the text to show, such as a command holding its secret. */
  create: (name: string) => Promise<string>;
}) {
  const { t } = useI18n();
  const [secret, setSecret] = useState("");
  const close = () => {
    setSecret("");
    onClose();
  };
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    try {
      setSecret(await create(String(new FormData(event.currentTarget).get("name"))));
    } catch (e) {
      showError(e);
    }
  };
  return (
    <Dialog open={open} onOpenChange={(next) => !next && close()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{secret ? t("common.secretOnce") : description}</DialogDescription>
        </DialogHeader>
        {secret ? (
          <>
            <Secret value={secret} />
            <DialogFooter>
              <Button onClick={close}>{t("common.done")}</Button>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={submit} className="grid gap-4">
            <Field>
              <FieldLabel htmlFor="create-name">{t("common.name")}</FieldLabel>
              <Input id="create-name" name="name" autoFocus required />
            </Field>
            <DialogFooter>
              <Button type="submit">{t("common.create")}</Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

export function ConfirmDialog({
  open,
  onClose,
  title,
  description,
  cancel,
  confirm,
  destructive,
  onConfirm,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  description: ReactNode;
  cancel: string;
  confirm: string;
  destructive?: boolean;
  onConfirm: () => void;
}) {
  return (
    <AlertDialog open={open} onOpenChange={(next) => !next && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{cancel}</AlertDialogCancel>
          <AlertDialogAction
            variant={destructive ? "destructive" : "default"}
            onClick={() => {
              onConfirm();
              onClose();
            }}
          >
            {confirm}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

/** Asks for a new name for something that already has one. */
export function RenameDialog({ open, onClose, title, name, onRename }: { open: boolean; onClose: () => void; title: string; name: string; onRename: (name: string) => Promise<unknown> }) {
  const { t } = useI18n();
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    try {
      await onRename(String(new FormData(event.currentTarget).get("name")));
      onClose();
    } catch (e) {
      showError(e);
    }
  };
  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <Input name="name" defaultValue={name} aria-label={t("common.name")} autoFocus required onFocus={(e) => e.currentTarget.select()} />
          <DialogFooter>
            <Button type="submit">{t("common.save")}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
