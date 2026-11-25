import { KeyRoundIcon, ShieldCheckIcon, Trash2Icon, UserPlusIcon, UsersIcon, WandSparklesIcon } from "lucide-react";
import { type FormEvent, useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { admin, hosts as hostsApi } from "@/api";
import { ConfirmDialog } from "@/components/dialogs";
import { Badge } from "@/components/reui/badge";
import { UserAvatar } from "@/components/presence";
import { Button } from "@/components/ui/button";
import { DataTable, PageHeader } from "@/components/data-table";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupButton, InputGroupInput } from "@/components/ui/input-group";
import { Label } from "@/components/ui/label";
import { Permission, type User } from "@/gen/inaba/v1/server_pb";
import { showError } from "@/lib/errors";
import { type MessageKey, useI18n } from "@/lib/i18n";

/** A permission's label, which has a matching `...Help` key saying what it allows. */
type GrantLabel = { [K in MessageKey]: `${K}Help` extends MessageKey ? K : never }[MessageKey];

export const grantable: [Permission, GrantLabel][] = [
  [Permission.MANAGE_HOSTS, "accounts.hosts"],
  [Permission.MANAGE_PROJECTS, "accounts.projects"],
  [Permission.THREADS_ANYWHERE, "accounts.threadsAnywhere"],
  [Permission.AUTOMATIONS, "accounts.automations"],
  [Permission.HOST_SETTINGS, "accounts.hostSettings"],
  [Permission.HOUSEKEEPING, "accounts.housekeeping"],
];

/** A password for someone else to sign in with once: long, random, and easy to copy. */
function randomPassword() {
  const bytes = crypto.getRandomValues(new Uint8Array(15));
  return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_");
}

/** A password field for an account being set up by an administrator: shown in the clear, with a generator. */
function PasswordField({ id, value, onChange }: { id: string; value: string; onChange: (value: string) => void }) {
  const { t } = useI18n();
  return (
    <Field>
      <FieldLabel htmlFor={id}>{t("accounts.password")}</FieldLabel>
      <InputGroup>
        {/* Shown, not masked: the administrator passes it on to its owner. */}
        <InputGroupInput id={id} value={value} onChange={(e) => onChange(e.target.value)} autoComplete="off" spellCheck={false} className="font-mono" required />
        <InputGroupAddon align="inline-end">
          <InputGroupButton size="xs" onClick={() => onChange(randomPassword())}>
            <WandSparklesIcon />
            {t("accounts.generate")}
          </InputGroupButton>
        </InputGroupAddon>
      </InputGroup>
      <FieldDescription>{t("accounts.passwordHelp")}</FieldDescription>
    </Field>
  );
}

/** For administrators: the accounts, and what each one is allowed to do. */
export function AccountsPanel({ me, onOpen }: { me: string; /** Opens an account in full. */ onOpen: (id: string) => void }) {
  const { t } = useI18n();
  const [users, setUsers] = useState<User[]>();
  const [adding, setAdding] = useState(false);
  const [resetting, setResetting] = useState<User>();
  const [removing, setRemoving] = useState<User>();

  const refresh = useCallback(() => admin.listUsers({}).then((r) => setUsers(r.users), showError), []);
  // Each account's own hosts, counted from every host.
  const [owned, setOwned] = useState<Record<string, number>>({});
  useEffect(() => {
    hostsApi.listHosts({ all: true }).then((r) => {
      const count: Record<string, number> = {};
      for (const h of r.hosts) if (h.ownerId) count[h.ownerId] = (count[h.ownerId] ?? 0) + 1;
      setOwned(count);
    }, showError);
  }, []);
  useEffect(() => {
    void refresh();
  }, [refresh]);

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={t("user.accounts")}
        description={t("accounts.description")}
        count={users?.length}
        actions={
          <Button onClick={() => setAdding(true)}>
            <UserPlusIcon data-icon="inline-start" />
            {t("accounts.add")}
          </Button>
        }
      />
      {/* A row opens the account, where its role, permissions, hosts and sign-in are changed. */}
      <DataTable
        rows={users}
        getRowKey={(u) => u.id}
        onRowClick={(u) => onOpen(u.id)}
        search={{ placeholder: t("accounts.filter"), matches: (u, q) => u.name.toLowerCase().includes(q) || u.displayName.toLowerCase().includes(q) }}
        empty={{ icon: UsersIcon, title: t("accounts.none") }}
        columns={[
          {
            header: t("accounts.account"),
            sortBy: (u) => u.name,
            cell: (u) => (
              <span className="flex items-center gap-2.5">
                <UserAvatar size="sm" id={u.id} name={u.name} url={u.avatarUrl} />
                <span className="grid min-w-0">
                  <span className="truncate font-medium">{u.displayName || u.name}</span>
                  <span className="text-muted-foreground truncate text-xs">@{u.name}</span>
                </span>
                {u.id === me && (
                  <Badge size="sm" variant="outline">
                    {t("accounts.you")}
                  </Badge>
                )}
              </span>
            ),
          },
          {
            header: t("accounts.role"),
            sortBy: (u) => u.admin,
            cell: (u) =>
              u.admin ? (
                <Badge size="sm" variant="info-light">
                  <ShieldCheckIcon />
                  {t("accounts.admin")}
                </Badge>
              ) : (
                <span className="text-muted-foreground">{t("accounts.member")}</span>
              ),
          },
          { header: t("accounts.signIn"), sortBy: (u) => u.oidc, cell: (u) => <span className="text-muted-foreground">{t(u.oidc ? "accounts.oidc" : "accounts.password")}</span> },
          {
            header: t("profile.totp"),
            sortBy: (u) => u.totp,
            cell: (u) =>
              u.oidc ? (
                <span className="text-muted-foreground">—</span>
              ) : u.totp ? (
                <Badge size="sm" variant="success-light">
                  {t("accountDetail.on")}
                </Badge>
              ) : (
                <Badge size="sm" variant="warning-light">
                  {t("accountDetail.off")}
                </Badge>
              ),
          },
          { header: t("accounts.ownHosts"), align: "right", sortBy: (u) => owned[u.id] ?? 0, cell: (u) => <span className="text-muted-foreground tabular-nums">{owned[u.id] ?? 0}</span> },
        ]}
        rowActions={[
          { label: t("accountDetail.setPassword"), icon: KeyRoundIcon, hidden: (u) => u.oidc, onSelect: (u) => setResetting(u) },
          { label: t("accounts.removeShort"), icon: Trash2Icon, destructive: true, separatorBefore: true, hidden: (u) => u.id === me, onSelect: (u) => setRemoving(u) },
        ]}
      />
      <AddAccountDialog open={adding} onClose={() => setAdding(false)} onAdded={refresh} />
      <ResetPasswordDialog user={resetting} onClose={() => setResetting(undefined)} />
      <ConfirmDialog
        open={removing !== undefined}
        onClose={() => setRemoving(undefined)}
        title={t("accounts.removeTitle", { name: removing?.name ?? "" })}
        description={t("accounts.removeHelp")}
        cancel={t("project.keep")}
        confirm={t("accounts.remove", { name: removing?.name ?? "" })}
        destructive
        onConfirm={() => removing && admin.deleteUser({ user: removing.id }).catch(showError).then(refresh)}
      />
    </div>
  );
}

function AddAccountDialog({ open, onClose, onAdded }: { open: boolean; onClose: () => void; onAdded: () => void }) {
  const { t } = useI18n();
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [isAdmin, setIsAdmin] = useState(false);
  const [permissions, setPermissions] = useState<Permission[]>([]);
  useEffect(() => {
    if (!open) return;
    setName("");
    setPassword(randomPassword());
    setIsAdmin(false);
    setPermissions([]);
  }, [open]);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    try {
      await admin.createUser({ name, password, admin: isAdmin, permissions: isAdmin ? [] : permissions });
      toast.success(t("accounts.added", { name }));
      onClose();
      onAdded();
    } catch (e) {
      showError(e);
    }
  };
  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("accounts.add")}</DialogTitle>
          <DialogDescription>{t("accounts.addHelp")}</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <Field>
            <FieldLabel htmlFor="account-name">{t("accounts.newName")}</FieldLabel>
            <Input id="account-name" value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" autoFocus required />
          </Field>
          <PasswordField id="account-password" value={password} onChange={setPassword} />
          <div className="grid gap-2">
            <Label className="font-normal" title={t("accounts.adminHelp")}>
              <Checkbox checked={isAdmin} onCheckedChange={setIsAdmin} />
              {t("accounts.admin")}
            </Label>
            {grantable.map(([permission, label]) => (
              <Label key={permission} className="font-normal">
                <Checkbox
                  checked={isAdmin || permissions.includes(permission)}
                  disabled={isAdmin}
                  onCheckedChange={(on) => setPermissions((now) => (on ? [...now, permission] : now.filter((p) => p !== permission)))}
                />
                {t(label)}
              </Label>
            ))}
          </div>
          <DialogFooter>
            <Button type="submit">{t("accounts.add")}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function ResetPasswordDialog({ user, onClose }: { user?: User; onClose: () => void }) {
  const { t } = useI18n();
  const [password, setPassword] = useState("");
  useEffect(() => {
    if (user) setPassword(randomPassword());
  }, [user]);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!user) return;
    try {
      await admin.setPassword({ user: user.id, password });
      toast.success(t("accounts.passwordSet", { name: user.name }));
      onClose();
    } catch (e) {
      showError(e);
    }
  };
  return (
    <Dialog open={user !== undefined} onOpenChange={(next) => !next && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("accounts.resetPassword", { name: user?.name ?? "" })}</DialogTitle>
          <DialogDescription>{t("accounts.resetHelp")}</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <PasswordField id="reset-password" value={password} onChange={setPassword} />
          <DialogFooter>
            <Button type="submit">{t("accounts.setPassword")}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
