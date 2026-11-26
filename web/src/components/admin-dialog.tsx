import { ArrowLeftIcon, BrushCleaningIcon, LayoutDashboardIcon, ServerIcon, UsersIcon } from "lucide-react";
import { type ReactNode, useCallback, useState } from "react";
import { AccountsPanel } from "@/components/accounts-panel";
import { AccountDetail } from "@/components/account-detail";
import { AdminHostAccess } from "@/components/admin-host-access";
import { AdminHostsPanel } from "@/components/admin-hosts-panel";
import { AdminOverview } from "@/components/admin-overview";
import { HousekeepingPanel } from "@/components/housekeeping-panel";
import { SidebarGroup, SidebarGroupLabel, SidebarMenu, SidebarMenuButton, SidebarMenuItem, SidebarTrigger } from "@/components/ui/sidebar";
import type { Host } from "@/gen/inaba/v1/server_pb";
import { type MessageKey, useI18n } from "@/lib/i18n";

// A host's modules are in that host's own settings.
export type AdminSection = "overview" | "accounts" | "hosts" | "housekeeping";

const sections: [AdminSection, MessageKey, ReactNode][] = [
  ["overview", "overview.title", <LayoutDashboardIcon />],
  ["accounts", "user.accounts", <UsersIcon />],
  ["hosts", "adminHosts.title", <ServerIcon />],
  ["housekeeping", "housekeeping.title", <BrushCleaningIcon />],
];

/**
 * Administration as a mode of the whole window: the sidebar
 * lists its pages in place of the projects, and the page takes the room a thread would.
 */
export function AdminNav({ section, onSection, onLeave }: { section: AdminSection; onSection: (s: AdminSection) => void; onLeave: () => void }) {
  const { t } = useI18n();
  return (
    <>
      <SidebarMenu className="px-2 pt-2">
        <SidebarMenuItem>
          <SidebarMenuButton tooltip={t("admin.leave")} onClick={onLeave}>
            <ArrowLeftIcon />
            <span>{t("admin.leave")}</span>
          </SidebarMenuButton>
        </SidebarMenuItem>
      </SidebarMenu>
      <SidebarGroup>
        <SidebarGroupLabel>{t("admin.title")}</SidebarGroupLabel>
        <SidebarMenu>
          {sections.map(([id, label, icon]) => (
            <SidebarMenuItem key={id}>
              <SidebarMenuButton tooltip={t(label)} isActive={section === id} onClick={() => onSection(id)}>
                {icon}
                <span>{t(label)}</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          ))}
        </SidebarMenu>
      </SidebarGroup>
    </>
  );
}

/** The administration page shown, with the whole width to itself. */
export function AdminPage({
  section,
  item,
  tab,
  me,
  hosts,
  onOpenHost,
  onAddHost,
  onItem,
}: {
  onAddHost: () => void;
  section: AdminSection;
  /** An account or host opened in its section, by id. */
  item?: string;
  /** The tab shown on that account's or host's page. */
  tab?: string;
  me: string;
  hosts: Host[];
  /** Goes into a host, anyone's. */
  onOpenHost: (host: Host) => void;
  /** Opens an account or host in its section, on a tab; none goes back to the list. */
  onItem: (section: AdminSection, id?: string, tab?: string) => void;
}) {
  const { t } = useI18n();
  const label = sections.find(([id]) => id === section)?.[1] ?? "admin.title";
  // The open account's or host's name, once its page has loaded it.
  const [named, setNamed] = useState<{ id: string; name: string }>();
  const onName = useCallback((name: string) => item && setNamed({ id: item, name }), [item]);
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex h-12 shrink-0 items-center gap-2 border-b px-3">
        <SidebarTrigger />
        <span className="text-muted-foreground text-sm">{t("admin.title")}</span>
        <span className="text-muted-foreground">/</span>
        {item ? (
          <>
            <button type="button" className="text-muted-foreground hover:text-foreground text-sm" onClick={() => onItem(section)}>
              {t(label)}
            </button>
            <span className="text-muted-foreground">/</span>
            <h1 className="truncate text-sm font-medium">{named?.id === item ? named.name : ""}</h1>
          </>
        ) : (
          <h1 className="text-sm font-medium">{t(label)}</h1>
        )}
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto grid w-full max-w-6xl grid-cols-[minmax(0,1fr)] gap-4 p-6">{section === "overview" ? (
            <AdminOverview onSection={(s) => onItem(s)} onHost={(id) => onItem("hosts", id)} />
          ) : section === "accounts" ? (
            item ? (
              <AccountDetail key={item} id={item} me={me} tab={tab} onTab={(next) => onItem("accounts", item, next)} onName={onName} onOpenHost={(id) => onItem("hosts", id)} onRemoved={() => onItem("accounts")} />
            ) : (
              <AccountsPanel me={me} onOpen={(id) => onItem("accounts", id)} />
            )
          ) : section === "hosts" ? (
            item ? (
              <AdminHostAccess key={item} id={item} tab={tab} onTab={(next) => onItem("hosts", item, next)} onName={onName} onOpen={onOpenHost} onOpenAccount={(id) => onItem("accounts", id)} />
            ) : (
              <AdminHostsPanel onDetail={(id) => onItem("hosts", id)} onAdd={onAddHost} />
            )
          ) : (
            <HousekeepingPanel hosts={hosts} />
          )}</div>
      </div>
    </div>
  );
}
