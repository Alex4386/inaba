import { PlusIcon, ServerIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { hosts as hostsApi } from "@/api";
import { DataTable, PageHeader } from "@/components/data-table";
import { lastSeen } from "@/components/host-about";
import { HostAvatar, platformName } from "@/components/host-icon";
import { Badge } from "@/components/reui/badge";
import { Button } from "@/components/ui/button";
import type { Host } from "@/gen/inaba/v1/server_pb";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** Every host on the server, others' personal ones too; a row opens the host's page. */
export function AdminHostsPanel({ onDetail, onAdd }: { onDetail: (id: string) => void; /** Shows how to connect another machine. */ onAdd: () => void }) {
  const { t, locale } = useI18n();
  const [list, setList] = useState<Host[]>();
  useEffect(() => {
    hostsApi.listHosts({ all: true }).then((r) => setList(r.hosts), showError);
  }, []);
  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={t("adminHosts.title")}
        description={t("adminHosts.description")}
        count={list?.length}
        actions={
          <Button onClick={onAdd}>
            <PlusIcon data-icon="inline-start" />
            {t("host.add")}
          </Button>
        }
      />
      <DataTable
        rows={list}
        getRowKey={(h) => h.id}
        onRowClick={(h) => onDetail(h.id)}
        search={{ placeholder: t("adminHosts.filter"), matches: (h, q) => h.name.toLowerCase().includes(q) || h.ownerName.toLowerCase().includes(q) }}
        empty={{ icon: ServerIcon, title: t("adminHosts.none") }}
        columns={[
          {
            header: t("adminHosts.host"),
            sortBy: (h) => h.name,
            cell: (h) => (
              <span className="flex items-center gap-2 font-medium">
                <HostAvatar host={h} label={h.name} className="size-6" />
                {h.name}
              </span>
            ),
          },
          {
            header: t("adminHosts.owner"),
            sortBy: (h) => (h.shared ? "" : h.ownerName),
            cell: (h) =>
              h.shared ? (
                <Badge size="sm" variant="outline">
                  {t(h.restricted ? "hostAccess.restrictedShared" : "adminHosts.shared")}
                </Badge>
              ) : (
                <span className="text-muted-foreground">{h.ownerName}</span>
              ),
          },
          { header: t("adminHosts.system"), sortBy: (h) => platformName(h), cell: (h) => <span className="text-muted-foreground">{platformName(h) || "—"}</span> },
          {
            header: t("detail.status"),
            sortBy: (h) => (h.online ? 0 : h.lastSeen ? 1 : 2),
            cell: (h) => (
              <span className="flex items-center gap-2">
                <span className={cn("size-2 rounded-full", h.online ? "bg-success" : "bg-muted-foreground/40")} aria-hidden />
                <span className={cn(!h.online && "text-muted-foreground")}>{h.online ? t("host.online") : lastSeen(h, t, locale)}</span>
              </span>
            ),
          },
        ]}
      />
    </div>
  );
}
