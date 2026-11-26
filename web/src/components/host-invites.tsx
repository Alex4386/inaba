import { timestampDate } from "@bufbuild/protobuf/wkt";
import { LinkIcon, MailIcon, Trash2Icon } from "lucide-react";
import { type FormEvent, useCallback, useEffect, useState } from "react";
import { hosts as hostsApi } from "@/api";
import { DataTable } from "@/components/data-table";
import { ConfirmDialog, Secret } from "@/components/dialogs";
import { Picker } from "@/components/picker";
import { Badge } from "@/components/reui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import type { Host, PendingHostInvite } from "@/gen/inaba/v1/server_pb";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

type Role = "member" | "manager";

/** A host's invite links: making one with a role, and the ones still pending, to end one by one or all. */
export function HostInvites({ host }: { host: Host }) {
  const { t, locale } = useI18n();
  const [invites, setInvites] = useState<PendingHostInvite[]>();
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<PendingHostInvite | "all">();
  const load = useCallback(() => hostsApi.listHostInvites({ hostId: host.id }).then((r) => setInvites(r.invites), showError), [host.id]);
  useEffect(() => {
    void load();
  }, [load]);
  const revoke = (target: PendingHostInvite | "all") =>
    (target === "all" ? hostsApi.revokeHostInvites({ hostId: host.id }) : hostsApi.revokeHostInvite({ hostId: host.id, id: target.id })).then(load, showError);
  const date = (ts?: PendingHostInvite["createdAt"]) => (ts ? timestampDate(ts).toLocaleDateString(locale) : "—");
  return (
    <div className="grid gap-2">
      <h3 className="text-sm font-medium">{t("invites.title")}</h3>
      <DataTable
        rows={invites}
        getRowKey={(i) => i.id}
        empty={{ icon: MailIcon, title: t("invites.none") }}
        actions={
          <>
            {(invites?.length ?? 0) > 1 && (
              <Button size="sm" variant="outline" onClick={() => setRevoking("all")}>
                {t("sharing.revoke", { count: String(invites?.length ?? 0) })}
              </Button>
            )}
            <Button size="sm" onClick={() => setCreating(true)}>
              <LinkIcon data-icon="inline-start" />
              {t("invites.create")}
            </Button>
          </>
        }
        columns={[
          {
            header: t("hostAccess.role"),
            sortBy: (i) => i.role,
            cell: (i) => (
              <Badge size="sm" variant={i.role === "manager" ? "primary-light" : "outline"}>
                {t(i.role === "manager" ? "hostAccess.manager" : "hostAccess.member")}
              </Badge>
            ),
          },
          { header: t("invites.note"), sortBy: (i) => i.note, cell: (i) => i.note || <span className="text-muted-foreground">—</span> },
          { header: t("invites.createdBy"), sortBy: (i) => i.createdBy, cell: (i) => i.createdBy || "—" },
          { header: t("invites.expires"), sortBy: (i) => (i.expiresAt ? Number(i.expiresAt.seconds) : null), cell: (i) => date(i.expiresAt) },
        ]}
        rowActions={[{ label: t("invites.revoke"), icon: Trash2Icon, destructive: true, onSelect: setRevoking }]}
      />
      <InviteCreateDialog host={host} open={creating} onClose={() => setCreating(false)} onCreated={() => void load()} />
      <ConfirmDialog
        open={revoking !== undefined}
        onClose={() => setRevoking(undefined)}
        title={t(revoking === "all" ? "invites.revokeAllTitle" : "invites.revokeTitle")}
        description={t("invites.revokeHelp")}
        cancel={t("providers.cancel")}
        confirm={t("invites.revoke")}
        destructive
        onConfirm={() => revoking && revoke(revoking)}
      />
    </div>
  );
}

/** Asks what an invite grants, then shows its link, once. */
function InviteCreateDialog({ host, open, onClose, onCreated }: { host: Host; open: boolean; onClose: () => void; onCreated: () => void }) {
  const { t } = useI18n();
  // Everyone uses an open shared host already, so what an invite adds there is mostly a manager.
  const openShared = host.shared && !host.restricted;
  const [role, setRole] = useState<Role>(openShared ? "manager" : "member");
  const [link, setLink] = useState("");
  const [busy, setBusy] = useState(false);
  const close = () => {
    setLink("");
    setRole(openShared ? "manager" : "member");
    onClose();
  };
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy) return;
    const note = String(new FormData(event.currentTarget).get("note") ?? "").trim();
    setBusy(true);
    hostsApi
      .createHostInvite({ hostId: host.id, role, note })
      .then((r) => {
        setLink(`${location.origin}/ui/invite/${r.token}`);
        onCreated();
      }, showError)
      .finally(() => setBusy(false));
  };
  return (
    <Dialog open={open} onOpenChange={(next) => !next && close()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("invites.dialogTitle", { host: host.name })}</DialogTitle>
          <DialogDescription>{link ? t("sharing.linkHelp") : t(openShared ? "invites.helpOpen" : host.shared ? "invites.helpRestricted" : "invites.help")}</DialogDescription>
        </DialogHeader>
        {link ? (
          <>
            <Secret value={link} />
            <DialogFooter>
              <Button onClick={close}>{t("common.done")}</Button>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={submit} className="grid gap-4">
            <Field>
              <FieldLabel>{t("hostAccess.role")}</FieldLabel>
              <Picker
                label={t("hostAccess.role")}
                value={role}
                onChange={setRole}
                options={[
                  { value: "member", label: t("hostAccess.member") },
                  { value: "manager", label: t("hostAccess.manager") },
                ]}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="invite-note">{t("invites.note")}</FieldLabel>
              <Input id="invite-note" name="note" maxLength={200} placeholder={t("invites.notePlaceholder")} />
            </Field>
            <DialogFooter>
              <Button type="submit" disabled={busy}>
                {busy && <Spinner data-icon="inline-start" />}
                {t("common.create")}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
