import { timestampDate } from "@bufbuild/protobuf/wkt";
import { ArrowRightIcon, KeyRoundIcon, LogOutIcon, ServerIcon, ShieldCheckIcon, ShieldOffIcon, Trash2Icon, UserCogIcon, UserIcon, UserXIcon } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { admin, hosts as hostsApi } from "@/api";
import { grantable, ResetPasswordDialog } from "@/components/accounts-panel";
import { DataTable, Stat, StatGrid } from "@/components/data-table";
import { DetailFields, DetailPage, DetailSection, ResourceMenu } from "@/components/detail-page";
import { SplitBar } from "@/components/metrics";
import { ConfirmDialog } from "@/components/dialogs";
import { HostAvatar } from "@/components/host-icon";
import { Picker } from "@/components/picker";
import { UserAvatar } from "@/components/presence";
import { Badge } from "@/components/reui/badge";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import type { HostAccess, Permission, UserDetail } from "@/gen/inaba/v1/server_pb";
import { showError } from "@/lib/errors";
import { type MessageKey, useI18n } from "@/lib/i18n";

/** The access an account has to a host, as the picker offers it: none, member or manager. */
export function AccessPicker({ access, onChange }: { access: Pick<HostAccess, "role"> & { host?: { shared: boolean; restricted: boolean } }; onChange: (role: string) => void }) {
  const { t } = useI18n();
  if (access.role === "owner") return <span className="text-muted-foreground text-sm">{t("hostAccess.owner")}</span>;
  // An open shared host is everyone's already; no role there means that.
  const open = access.host?.shared && !access.host.restricted;
  return (
    <Picker
      size="sm"
      label={t("hostAccess.role")}
      value={access.role === "member" || access.role === "manager" ? access.role : "none"}
      onChange={onChange}
      options={[
        { value: "none", label: t(open ? "hostAccess.everyone" : "hostAccess.none") },
        { value: "member", label: t("hostAccess.member") },
        { value: "manager", label: t("hostAccess.manager") },
      ]}
    />
  );
}

/** What an account's place on a host is called. */
export function accessLabel(a: HostAccess): MessageKey {
  switch (a.role) {
    case "owner":
      return "hostAccess.owner";
    case "manager":
      return "hostAccess.manager";
    case "member":
      return "hostAccess.member";
    case "everyone":
      return "hostAccess.everyone";
    default:
      return "hostAccess.none";
  }
}

type SecurityRow = { id: string; label: MessageKey; status: string; action?: { label: string; icon: typeof KeyRoundIcon; run: () => void; disabled?: boolean } };

/** One account in full, for an administrator, on tabs: an overview, what it may do, its hosts, and how it signs in. */
export function AccountDetail({
  id,
  me,
  tab,
  onTab,
  onOpenHost,
  onName,
  onRemoved,
}: {
  id: string;
  me: string;
  tab?: string;
  onTab: (tab: string) => void;
  onOpenHost: (hostId: string) => void;
  /** Tells the page whose account this is, for its breadcrumb. */
  onName?: (name: string) => void;
  onRemoved: () => void;
}) {
  const { t, locale } = useI18n();
  const [detail, setDetail] = useState<UserDetail>();
  const [resetting, setResetting] = useState(false);
  const [removing, setRemoving] = useState(false);
  // A change of access on its way: each is built from the access as last read, so the next waits for it.
  const [saving, setSaving] = useState(false);
  const load = useCallback(() => admin.getUserDetail({ user: id }).then(setDetail, showError), [id]);
  useEffect(() => {
    void load();
  }, [load]);
  const name = detail?.user?.name;
  useEffect(() => {
    if (name) onName?.(name);
  }, [name, onName]);
  const user = detail?.user;
  if (!detail || !user) return <DetailPage loading title="" tabs={[]} onTab={onTab} />;

  const setAccess = (isAdmin: boolean, permissions: Permission[]) => {
    setSaving(true);
    return admin
      .setAccess({ user: user.id, admin: isAdmin, permissions })
      .then(load, showError)
      .finally(() => setSaving(false));
  };
  const setRole = (hostId: string, role: string) => hostsApi.setHostMember({ hostId, userId: user.id, role }).then(load, showError);
  const count = (role: string) => detail.hosts.filter((a) => a.role === role).length;
  const owned = count("owner");
  const usable = detail.hosts.length - count("");
  const signIn = t(user.oidc ? "accounts.oidc" : "accounts.password");

  const security: SecurityRow[] = [
    {
      id: "password",
      label: "profile.password",
      status: user.oidc ? t("accountDetail.viaOidc") : t("accountDetail.passwordSet"),
      action: user.oidc ? undefined : { label: t("accountDetail.setPassword"), icon: KeyRoundIcon, run: () => setResetting(true) },
    },
    {
      id: "totp",
      label: "profile.totp",
      status: t(user.totp ? "accountDetail.on" : "accountDetail.off"),
      action: user.totp
        ? { label: t("accountDetail.disableTotp"), icon: ShieldOffIcon, run: () => void admin.disableUserTotp({ user: user.id }).then(() => (toast.success(t("accountDetail.totpDisabled")), load()), showError) }
        : undefined,
    },
    { id: "passkeys", label: "accountDetail.passkeys", status: String(detail.passkeys) },
    {
      id: "sessions",
      label: "accountDetail.sessions",
      status: t("accountDetail.browsers", { count: String(detail.sessions) }),
      action: {
        label: t("accountDetail.signOut"),
        icon: LogOutIcon,
        disabled: detail.sessions === 0,
        run: () => void admin.endUserSessions({ user: user.id }).then(() => (toast.success(t("accountDetail.signedOut")), load()), showError),
      },
    },
  ];

  return (
    <>
      <DetailPage
        media={<UserAvatar id={user.id} name={user.displayName || user.name} url={user.avatarUrl} size="lg" className="size-12 text-base" />}
        title={user.displayName || user.name}
        subtitle={
          <>
            <span>@{user.name}</span>
            {user.admin && (
              <Badge size="sm" variant="info-light">
                <ShieldCheckIcon />
                {t("accounts.admin")}
              </Badge>
            )}
            {user.id === me && (
              <Badge size="sm" variant="outline">
                {t("accounts.you")}
              </Badge>
            )}
          </>
        }
        actions={
          <ResourceMenu
            menu={[
              user.admin
                ? { label: t("accountDetail.revokeAdmin"), icon: UserIcon, onClick: () => void setAccess(false, user.permissions), hidden: user.id === me }
                : { label: t("accountDetail.grantAdmin"), icon: UserCogIcon, onClick: () => void setAccess(true, user.permissions) },
              { label: t("accounts.remove", { name: user.name }), icon: UserXIcon, destructive: true, separatorBefore: true, onClick: () => setRemoving(true), hidden: user.id === me },
            ]}
          />
        }
        activeTab={tab}
        onTab={onTab}
        tabs={[
          {
            id: "overview",
            label: t("detail.overview"),
            content: (
              <div className="flex flex-col gap-6">
                <StatGrid className="grid-cols-2 lg:grid-cols-4">
                  <Stat label={t("accounts.role")} value={t(user.admin ? "accounts.admin" : "accounts.member")} icon={user.admin ? ShieldCheckIcon : UserIcon} onClick={() => onTab("permissions")} />
                  <Stat label={t("accountDetail.usesHosts")} value={t("overview.ofThreads", { running: String(usable), total: String(detail.hosts.length) })} icon={ServerIcon} onClick={() => onTab("hosts")} />
                  <Stat label={t("profile.totp")} value={user.oidc ? t("overview.viaOidc") : t(user.totp ? "accountDetail.on" : "accountDetail.off")} icon={KeyRoundIcon} tone={!user.oidc && !user.totp ? "text-warning" : undefined} onClick={() => onTab("security")} />
                  <Stat label={t("accountDetail.sessions")} value={t("accountDetail.browsers", { count: String(detail.sessions) })} icon={LogOutIcon} onClick={() => onTab("security")} />
                </StatGrid>
                <section className="bg-card flex flex-col gap-3 rounded-lg border p-4">
                  <h3 className="text-sm font-medium">{t("accountDetail.accessAcross")}</h3>
                  <SplitBar
                    parts={[
                      { label: t("hostAccess.owner"), value: count("owner"), tone: "bg-chart-1" },
                      { label: t("hostAccess.manager"), value: count("manager"), tone: "bg-chart-2" },
                      { label: t("hostAccess.member"), value: count("member"), tone: "bg-chart-3" },
                      { label: t("hostAccess.everyone"), value: count("everyone"), tone: "bg-chart-4" },
                      { label: t("hostAccess.none"), value: count(""), tone: "bg-muted-foreground/30" },
                    ]}
                  />
                </section>
                <DetailSection title={t("detail.details")}>
              <DetailFields
                fields={[
                  { label: t("accounts.newName"), value: user.name },
                  { label: t("profile.displayName"), value: user.displayName || "—" },
                  { label: "ID", value: <code className="text-xs">{user.id}</code> },
                  { label: t("accounts.role"), value: t(user.admin ? "accounts.admin" : "accounts.member") },
                  { label: t("accounts.signIn"), value: signIn },
                  { label: t("profile.totp"), value: t(user.totp ? "accountDetail.on" : "accountDetail.off") },
                  { label: t("accounts.ownHosts"), value: owned },
                  { label: t("accountDetail.sessions"), value: t("accountDetail.browsers", { count: String(detail.sessions) }) },
                  { label: t("accountDetail.created"), value: detail.createdAt ? timestampDate(detail.createdAt).toLocaleString(locale) : "—" },
                ]}
              />
                </DetailSection>
              </div>
            ),
          },
          {
            id: "permissions",
            label: t("detail.permissions"),
            content: (
              <DataTable
                rows={grantable.map(([permission, label]) => ({ permission, label }))}
                getRowKey={(r) => String(r.permission)}
                empty={{ title: "" }}
                columns={[
                  { header: t("detail.permission"), cell: (r) => <span className="font-medium">{t(r.label)}</span>, className: "whitespace-normal wrap-anywhere" },
                  { header: t("detail.allows"), cell: (r) => <span className="text-muted-foreground">{t(`${r.label}Help`)}</span>, className: "whitespace-normal wrap-anywhere" },
                  {
                    header: t("detail.granted"),
                    align: "right",
                    // Room for the switch's touch area, which reaches past it, so the table does not scroll for it.
                    className: "pr-4",
                    // An administrator has every permission, so there is nothing to choose.
                    cell: (r) => (
                      <Switch
                        aria-label={t(r.label)}
                        checked={user.admin || user.permissions.includes(r.permission)}
                        disabled={user.admin || saving}
                        onCheckedChange={(on) => void setAccess(user.admin, on ? [...user.permissions, r.permission] : user.permissions.filter((p) => p !== r.permission))}
                      />
                    ),
                  },
                ]}
              />
            ),
          },
          {
            id: "hosts",
            label: t("accountDetail.hosts"),
            content: (
              <DataTable
                rows={detail.hosts}
                getRowKey={(a) => a.host!.id}
                onRowClick={(a) => onOpenHost(a.host!.id)}
                search={{ placeholder: t("adminHosts.filter"), matches: (a, q) => a.host!.name.toLowerCase().includes(q) }}
                empty={{ icon: ServerIcon, title: t("adminHosts.none") }}
                columns={[
                  {
                    header: t("adminHosts.host"),
                    sortBy: (a) => a.host!.name,
                    cell: (a) => (
                      <span className="flex items-center gap-2 font-medium">
                        <HostAvatar host={a.host!} label={a.host!.name} className="size-6" />
                        {a.host!.name}
                      </span>
                    ),
                  },
                  { header: t("adminHosts.owner"), className: "hidden md:table-cell", sortBy: (a) => a.host!.ownerName, cell: (a) => <span className="text-muted-foreground">{a.host!.shared ? t(a.host!.restricted ? "hostAccess.restrictedShared" : "adminHosts.shared") : a.host!.ownerName}</span> },
                  { header: t("hostAccess.role"), sortBy: (a) => a.role, cell: (a) => <AccessPicker access={a} onChange={(role) => void setRole(a.host!.id, role)} /> },
                ]}
                rowActions={[
                  { label: t("adminHosts.details"), icon: ArrowRightIcon, onSelect: (a) => onOpenHost(a.host!.id) },
                  { label: t("hostAccess.removeAccess"), icon: Trash2Icon, destructive: true, separatorBefore: true, hidden: (a) => a.role !== "member" && a.role !== "manager", onSelect: (a) => void setRole(a.host!.id, "none") },
                ]}
              />
            ),
          },
          {
            id: "security",
            label: t("detail.security"),
            content: (
              <DataTable
                rows={security}
                getRowKey={(r) => r.id}
                empty={{ title: "" }}
                columns={[
                  { header: t("detail.method"), cell: (r) => <span className="font-medium">{t(r.label)}</span>, className: "whitespace-normal" },
                  { header: t("detail.status"), cell: (r) => <span className="text-muted-foreground">{r.status}</span>, className: "whitespace-normal" },
                  {
                    header: <span className="sr-only">{t("table.actions")}</span>,
                    align: "right",
                    cell: (r) =>
                      r.action && (
                        <Button size="sm" variant="outline" disabled={r.action.disabled} onClick={r.action.run}>
                          <r.action.icon data-icon="inline-start" />
                          {r.action.label}
                        </Button>
                      ),
                  },
                ]}
              />
            ),
          },
        ]}
      />
      <ResetPasswordDialog user={resetting ? user : undefined} onClose={() => setResetting(false)} />
      <ConfirmDialog
        open={removing}
        onClose={() => setRemoving(false)}
        title={t("accounts.removeTitle", { name: user.name })}
        description={t("accounts.removeHelp", { count: String(owned) })}
        cancel={t("project.keep")}
        confirm={t("accounts.remove", { name: user.name })}
        destructive
        onConfirm={() => admin.deleteUser({ user: user.id }).then(onRemoved, showError)}
      />
    </>
  );
}
