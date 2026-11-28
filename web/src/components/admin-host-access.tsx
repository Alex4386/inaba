import { timestampDate } from "@bufbuild/protobuf/wkt";
import { ActivityIcon, ArrowRightIcon, ClockIcon, CpuIcon, MemoryStickIcon, Trash2Icon, UserPlusIcon, UsersIcon } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { admin, hosts as hostsApi, onHost } from "@/api";
import { uptime } from "@/components/admin-overview";
import { DataTable, Stat, StatGrid } from "@/components/data-table";
import { DetailFields, DetailPage, DetailSection } from "@/components/detail-page";
import { lastSeen } from "@/components/host-about";
import { HostAvatar, platformName } from "@/components/host-icon";
import { HostInvites } from "@/components/host-invites";
import { bytes, Meter, Sparkline } from "@/components/metrics";
import { Picker } from "@/components/picker";
import { UserAvatar } from "@/components/presence";
import { Badge } from "@/components/reui/badge";
import { Button } from "@/components/ui/button";
import { UserPickerDialog } from "@/components/user-picker-dialog";
import { Switch } from "@/components/ui/switch";
import { type SystemInfo, SystemService } from "@/gen/inaba/v1/host_pb";
import type { Host, ListHostMembersResponse, User } from "@/gen/inaba/v1/server_pb";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** The host's live readings every two seconds, with the last minute of CPU use. */
function useLiveSystem(host?: Host) {
  const [info, setInfo] = useState<SystemInfo>();
  const [cpu, setCpu] = useState<number[]>([]);
  const id = host?.online ? host.id : undefined;
  useEffect(() => {
    if (!id) return;
    let live = true;
    const client = onHost(id, SystemService);
    const read = () =>
      client.getSystemInfo({}).then((r) => {
        if (!live) return;
        setInfo(r);
        setCpu((c) => [...c.slice(-29), r.cpuPercent]);
      }, () => {});
    void read();
    const timer = setInterval(read, 2000);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [id]);
  return { info, cpu };
}

const share = (used: bigint, total: bigint) => (total > 0n ? Number(used) / Number(total) : undefined);

/** One host for an administrator, on tabs: how it is doing, and who may use it. */
export function AdminHostAccess({
  id,
  tab,
  onTab,
  onOpen,
  onOpenAccount,
  onName,
}: {
  id: string;
  tab?: string;
  onTab: (tab: string) => void;
  /** Goes into the host's workspace. */
  onOpen: (host: Host) => void;
  onOpenAccount: (userId: string) => void;
  /** Tells the page which host this is, for its breadcrumb. */
  onName?: (name: string) => void;
}) {
  const { t, locale } = useI18n();
  const [host, setHost] = useState<Host>();
  const [members, setMembers] = useState<ListHostMembersResponse>();
  const [users, setUsers] = useState<User[]>([]);
  // The search for people to let in, open.
  const [adding, setAdding] = useState(false);
  const load = useCallback(() => {
    hostsApi.listHosts({ all: true }).then((r) => setHost(r.hosts.find((h) => h.id === id)), showError);
    hostsApi.listHostMembers({ hostId: id }).then(setMembers, showError);
  }, [id]);
  useEffect(() => {
    load();
    admin.listUsers({}).then((r) => setUsers(r.users), showError);
  }, [load]);
  const hostName = host?.name;
  const named = useRef(onName);
  named.current = onName;
  useEffect(() => {
    if (hostName) named.current?.(hostName);
  }, [hostName]);
  const { info, cpu } = useLiveSystem(host);
  if (!host) return <DetailPage loading title="" tabs={[]} onTab={onTab} />;

  const setRole = (userId: string, role: string) => hostsApi.setHostMember({ hostId: id, userId, role }).then(load, showError);
  const inside = new Set(members?.members.map((m) => m.userId));
  // Anyone not in yet, but not the owner, who has the host already.
  const addable = users.filter((u) => !inside.has(u.id) && u.id !== host.ownerId);
  // Live when online; otherwise what it reported when it last connected.
  const system = info ?? host.system;

  return (
    <DetailPage
      media={<HostAvatar host={host} label={host.name} className="size-12" />}
      title={host.name}
      subtitle={
        <>
          <span className="flex items-center gap-1.5">
            <span className={cn("size-2 rounded-full", host.online ? "bg-success" : "bg-muted-foreground/40")} aria-hidden />
            {host.online ? t("host.online") : lastSeen(host, t, locale)}
          </span>
          <span>·</span>
          {host.shared ? (
            <Badge size="sm" variant="outline">
              {t(host.restricted ? "hostAccess.restrictedShared" : "adminHosts.shared")}
            </Badge>
          ) : (
            <span>{host.ownerName}</span>
          )}
        </>
      }
      actions={
        <Button onClick={() => onOpen(host)}>
          {t("adminHosts.goToWorkspace")}
          <ArrowRightIcon data-icon="inline-end" />
        </Button>
      }
      activeTab={tab}
      onTab={onTab}
      tabs={[
        {
          id: "overview",
          label: t("detail.overview"),
          content: (
            <div className="flex flex-col gap-6">
              {host.online && info ? (
                <>
                  <StatGrid className="grid-cols-2 lg:grid-cols-4">
                    <Stat label={t("overview.cpu")} value={`${Math.round(info.cpuPercent)}%`} icon={CpuIcon} />
                    <Stat label={t("overview.memory")} value={`${bytes(info.memoryUsed)} / ${bytes(info.memoryTotal)}`} icon={MemoryStickIcon} />
                    <Stat label={t("overview.threadsRunning")} value={t("overview.ofThreads", { running: String(info.threadsRunning), total: String(info.threads) })} icon={ActivityIcon} />
                    <Stat label={t("overview.uptime")} value={uptime(Number(info.uptimeSeconds), t)} icon={ClockIcon} />
                  </StatGrid>
                  <div className="grid gap-4 lg:grid-cols-3">
                    <section className="bg-card flex flex-col gap-2 rounded-lg border p-4 lg:col-span-2">
                      <h3 className="text-sm font-medium">{t("overview.cpuLastMinute")}</h3>
                      <Sparkline points={cpu} />
                      <span className="text-muted-foreground text-xs">{t("overview.load", { a: info.load1.toFixed(2), b: info.load5.toFixed(2), c: info.load15.toFixed(2) })}</span>
                    </section>
                    <section className="bg-card flex flex-col gap-4 rounded-lg border p-4">
                      <div className="flex flex-col gap-1.5">
                        <h3 className="text-sm font-medium">{t("overview.memory")}</h3>
                        <Meter value={share(info.memoryUsed, info.memoryTotal)} label={`${bytes(info.memoryUsed)} / ${bytes(info.memoryTotal)}`} />
                      </div>
                      <div className="flex flex-col gap-1.5">
                        <h3 className="text-sm font-medium">{t("overview.disk")}</h3>
                        <Meter value={share(info.diskUsed, info.diskTotal)} label={`${bytes(info.diskUsed)} / ${bytes(info.diskTotal)}`} />
                      </div>
                    </section>
                  </div>
                </>
              ) : (
                !host.online && <p className="text-muted-foreground text-sm">{system ? t("hostAbout.lastKnown") : t("hostAbout.neverConnected")}</p>
              )}
              <DetailSection title={t("detail.details")}>
                <DetailFields
                  fields={[
                    { label: "ID", value: <code className="text-xs">{host.id}</code> },
                    { label: t("adminHosts.owner"), value: host.shared ? t("adminHosts.shared") : host.ownerName },
                    { label: t("hostAccess.restrict"), value: host.shared ? t(host.restricted ? "accountDetail.on" : "accountDetail.off") : "—" },
                    { label: t("adminHosts.system"), value: platformName(host) || "—" },
                    { label: t("hostAbout.hostname"), value: system?.hostname || "—" },
                    { label: t("hostAbout.kernel"), value: system?.kernel || "—" },
                    { label: t("overview.cpu"), value: system ? `${system.cpuModel} · ${t("overview.cores", { count: String(system.cpuCores) })}` : "—" },
                    { label: t("overview.memory"), value: system ? bytes(system.memoryTotal) : "—" },
                    { label: t("overview.disk"), value: system ? bytes(system.diskTotal) : "—" },
                    { label: t("hostAbout.lastSeenAt"), value: host.online ? t("host.online") : host.lastSeen ? timestampDate(host.lastSeen).toLocaleString(locale) : t("hostAbout.neverConnected") },
                  ]}
                />
              </DetailSection>
            </div>
          ),
        },
        {
          id: "members",
          label: t("hostAccess.members"),
          content: (
            <div className="flex flex-col gap-4">
              <UserPickerDialog open={adding} onOpenChange={setAdding} users={addable} title={t("hostAccess.add")} onPick={(u) => setRole(u.id, "member")} />
              {host.shared && (
                <div className="bg-card flex items-center justify-between gap-4 rounded-lg border px-4 py-3">
                  <div className="grid gap-0.5">
                    <span className="text-sm font-medium">{t("hostAccess.restrict")}</span>
                    <span className="text-muted-foreground text-xs">{t("hostAccess.restrictHelp")}</span>
                  </div>
                  <Switch checked={host.restricted} aria-label={t("hostAccess.restrict")} onCheckedChange={(restricted) => hostsApi.updateHost({ id, restricted }).then(load, showError)} />
                </div>
              )}
              <p className="text-muted-foreground text-sm">{t(host.shared ? (host.restricted ? "hostAccess.membersRestricted" : "hostAccess.membersOpen") : "hostAccess.membersPersonal")}</p>
              <DataTable
                rows={members?.members}
                getRowKey={(m) => m.userId}
                onRowClick={(m) => onOpenAccount(m.userId)}
                empty={{ icon: UsersIcon, title: t("hostAccess.noMembers") }}
                actions={
                  addable.length > 0 && (
                    <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
                      <UserPlusIcon data-icon="inline-start" />
                      {t("hostAccess.add")}
                    </Button>
                  )
                }
                columns={[
                  {
                    header: t("accounts.account"),
                    sortBy: (m) => m.name,
                    cell: (m) => (
                      <span className="flex items-center gap-2 font-medium">
                        <UserAvatar id={m.userId} name={m.name} url={m.avatarUrl} size="sm" />
                        {m.name}
                      </span>
                    ),
                  },
                  {
                    header: t("hostAccess.role"),
                    sortBy: (m) => m.role,
                    cell: (m) => (
                      <Picker
                        size="sm"
                        label={t("hostAccess.role")}
                        value={m.role}
                        onChange={(role) => void setRole(m.userId, role)}
                        options={[
                          { value: "member", label: t("hostAccess.member") },
                          { value: "manager", label: t("hostAccess.manager") },
                        ]}
                      />
                    ),
                  },
                ]}
                rowActions={[{ label: t("hostAccess.removeAccess"), icon: Trash2Icon, destructive: true, onSelect: (m) => void setRole(m.userId, "none") }]}
              />
            </div>
          ),
        },
        {
          id: "invites",
          label: t("invites.title"),
          content: <HostInvites host={host} />,
        },
      ]}
    />
  );
}
