import { timestampDate } from "@bufbuild/protobuf/wkt";
import { ActivityIcon, ServerIcon, ShieldCheckIcon, UsersIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { admin, hosts as hostsApi, onHost } from "@/api";
import { DataTable, PageHeader, Stat, StatGrid } from "@/components/data-table";
import { lastSeen } from "@/components/host-about";
import { HostAvatar, platformName } from "@/components/host-icon";
import { Meter, SplitBar } from "@/components/metrics";
import { type SystemInfo, SystemService } from "@/gen/inaba/v1/host_pb";
import type { Host, User } from "@/gen/inaba/v1/server_pb";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

/** Every online host's live system readings, read again every few seconds. */
export function useLiveSystems(hosts: Host[] | undefined, every = 5000) {
  const [systems, setSystems] = useState<Record<string, SystemInfo>>({});
  const online = (hosts ?? []).filter((h) => h.online).map((h) => h.id).join(",");
  useEffect(() => {
    if (!online) return;
    let live = true;
    const read = () =>
      online.split(",").forEach((id) =>
        onHost(id, SystemService)
          .getSystemInfo({})
          .then((info) => live && setSystems((s) => ({ ...s, [id]: info })), () => {}),
      );
    read();
    const timer = setInterval(read, every);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [online, every]);
  return systems;
}

const share = (used: bigint, total: bigint) => (total > 0n ? Number(used) / Number(total) : undefined);

/** How the server's hosts and accounts are doing, at a glance: the administration's first page. */
export function AdminOverview({ onSection, onHost: openHost }: { onSection: (section: "accounts" | "hosts") => void; onHost: (id: string) => void }) {
  const { t, locale } = useI18n();
  const [hosts, setHosts] = useState<Host[]>();
  const [users, setUsers] = useState<User[]>();
  useEffect(() => {
    hostsApi.listHosts({ all: true }).then((r) => setHosts(r.hosts), showError);
    admin.listUsers({}).then((r) => setUsers(r.users), showError);
  }, []);
  const systems = useLiveSystems(hosts);

  const online = hosts?.filter((h) => h.online).length;
  const never = hosts?.filter((h) => !h.online && !h.lastSeen).length ?? 0;
  const live = Object.values(systems);
  const running = live.reduce((n, s) => n + s.threadsRunning, 0);
  const threads = live.reduce((n, s) => n + s.threads, 0);
  const withTotp = users?.filter((u) => u.totp || u.oidc).length;
  // Offline hosts first, as they are what needs looking at; then by name.
  const rows = hosts && [...hosts].sort((a, b) => Number(a.online) - Number(b.online) || a.name.localeCompare(b.name));

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title={t("overview.title")} description={t("overview.description")} />

      <StatGrid className="grid-cols-2 lg:grid-cols-4">
        <Stat label={t("overview.hostsOnline")} value={hosts ? `${online} / ${hosts.length}` : "—"} icon={ServerIcon} tone={hosts && online! < hosts.length ? "text-warning" : undefined} onClick={() => onSection("hosts")} />
        <Stat label={t("overview.threadsRunning")} value={hosts ? t("overview.ofThreads", { running: String(running), total: String(threads) }) : "—"} icon={ActivityIcon} />
        <Stat label={t("overview.accounts")} value={users?.length ?? "—"} icon={UsersIcon} onClick={() => onSection("accounts")} />
        <Stat label={t("overview.twoStep")} value={users ? `${withTotp} / ${users.length}` : "—"} icon={ShieldCheckIcon} tone={users && withTotp! < users.length ? "text-warning" : undefined} onClick={() => onSection("accounts")} />
      </StatGrid>

      <div className="grid gap-4 lg:grid-cols-2">
        <section className="bg-card flex flex-col gap-3 rounded-lg border p-4">
          <h3 className="text-sm font-medium">{t("overview.fleet")}</h3>
          <SplitBar
            parts={[
              { label: t("overview.online"), value: online ?? 0, tone: "bg-success" },
              { label: t("overview.offline"), value: (hosts?.length ?? 0) - (online ?? 0) - never, tone: "bg-warning" },
              { label: t("overview.neverConnected"), value: never, tone: "bg-muted-foreground/40" },
            ]}
          />
        </section>
        <section className="bg-card flex flex-col gap-3 rounded-lg border p-4">
          <h3 className="text-sm font-medium">{t("overview.signIn")}</h3>
          <SplitBar
            parts={[
              { label: t("overview.withTwoStep"), value: users?.filter((u) => u.totp && !u.oidc).length ?? 0, tone: "bg-success" },
              { label: t("overview.viaOidc"), value: users?.filter((u) => u.oidc).length ?? 0, tone: "bg-chart-1" },
              { label: t("overview.passwordOnly"), value: users?.filter((u) => !u.totp && !u.oidc).length ?? 0, tone: "bg-warning" },
            ]}
          />
        </section>
      </div>

      <section className="flex flex-col gap-3">
        <h3 className="text-sm font-medium">{t("overview.resources")}</h3>
        <DataTable
          rows={rows}
          getRowKey={(h) => h.id}
          onRowClick={(h) => openHost(h.id)}
          empty={{ icon: ServerIcon, title: t("adminHosts.none") }}
          columns={[
            {
              header: t("adminHosts.host"),
              sortBy: (h) => h.name,
              cell: (h) => (
                <span className="flex items-center gap-2">
                  <HostAvatar host={h} label={h.name} className="size-6" />
                  <span className="grid min-w-0">
                    <span className="truncate font-medium">{h.name}</span>
                    <span className="text-muted-foreground truncate text-xs">{h.online ? platformName(h) : lastSeen(h, t, locale)}</span>
                  </span>
                </span>
              ),
            },
            { header: t("overview.cpu"), sortBy: (h) => systems[h.id]?.cpuPercent, cell: (h) => <Meter value={systems[h.id] && systems[h.id].cpuPercent / 100} label={systems[h.id] && t("overview.cores", { count: String(systems[h.id].cpuCores) })} /> },
            {
              header: t("overview.memory"),
              sortBy: (h) => systems[h.id] && share(systems[h.id].memoryUsed, systems[h.id].memoryTotal),
              cell: (h) => <Meter value={systems[h.id] && share(systems[h.id].memoryUsed, systems[h.id].memoryTotal)} />,
            },
            {
              header: t("overview.disk"),
              sortBy: (h) => systems[h.id] && share(systems[h.id].diskUsed, systems[h.id].diskTotal),
              cell: (h) => <Meter value={systems[h.id] && share(systems[h.id].diskUsed, systems[h.id].diskTotal)} />,
            },
            {
              header: t("overview.threads"),
              align: "right",
              sortBy: (h) => systems[h.id]?.threadsRunning,
              cell: (h) => (systems[h.id] ? <span className="tabular-nums">{t("overview.ofThreads", { running: String(systems[h.id].threadsRunning), total: String(systems[h.id].threads) })}</span> : <span className="text-muted-foreground">—</span>),
            },
            {
              header: t("overview.since"),
              align: "right",
              sortBy: (h) => (h.online ? Number(systems[h.id]?.uptimeSeconds ?? 0) : h.lastSeen ? -timestampDate(h.lastSeen).getTime() : undefined),
              cell: (h) => <span className="text-muted-foreground text-xs tabular-nums">{h.online && systems[h.id] ? uptime(Number(systems[h.id].uptimeSeconds), t) : "—"}</span>,
            },
          ]}
        />
      </section>
    </div>
  );
}

/** Uptime the short way: 3d 4h, 5h 12m, 7m. */
export function uptime(seconds: number, t: ReturnType<typeof useI18n>["t"]) {
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  return d ? t("overview.daysHours", { d: String(d), h: String(h) }) : h ? t("overview.hoursMinutes", { h: String(h), m: String(m) }) : t("overview.minutes", { m: String(m) });
}
