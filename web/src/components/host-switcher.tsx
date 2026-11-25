import { ChevronsUpDownIcon, MoreHorizontalIcon, PlusIcon, SettingsIcon, Trash2Icon } from "lucide-react";
import { HostAvatar, HostIcon, hostSubtitle } from "@/components/host-icon";
import { Logo } from "@/components/logo";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { SidebarMenu, SidebarMenuButton, SidebarMenuItem, useSidebar } from "@/components/ui/sidebar";
import type { Host } from "@/gen/inaba/v1/server_pb";
import { useI18n } from "@/lib/i18n";

/** Picks which host the rest of the sidebar shows, the way a workspace switcher does; each host has its settings and removal beside it. */
export function HostSwitcher({
  hosts,
  active,
  onSelect,
  onAdd,
  onSettings,
  onRemove,
  mayManage,
  mayRemove,
}: {
  hosts: Host[];
  active?: Host;
  onSelect: (host: Host) => void;
  onAdd: () => void;
  onSettings: (host: Host) => void;
  onRemove: (host: Host) => void;
  /** Whether the account may change the host's settings. */
  mayManage: (host: Host) => boolean;
  /** Whether the account may remove the host. */
  mayRemove: (host: Host) => boolean;
}) {
  const { isMobile } = useSidebar();
  const { t } = useI18n();
  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger render={<SidebarMenuButton size="lg" className="data-open:bg-sidebar-accent data-open:text-sidebar-accent-foreground" />}>
            <div className="bg-sidebar-primary text-sidebar-primary-foreground flex aspect-square size-8 items-center justify-center rounded-lg">
              {active ? <HostIcon host={active} className="size-4" /> : <Logo className="size-5" />}
            </div>
            <div className="grid flex-1 text-left text-sm leading-tight">
              <span className="truncate font-medium">{active?.name ?? "inaba"}</span>
              <span className="text-muted-foreground truncate text-xs">{active ? t(active.online ? "host.connected" : "host.notConnected") : t("host.choose")}</span>
            </div>
            <ChevronsUpDownIcon className="ml-auto" />
          </DropdownMenuTrigger>
          <DropdownMenuContent className="min-w-80" align="start" side={isMobile ? "bottom" : "right"} sideOffset={4}>
            <DropdownMenuGroup>
              <DropdownMenuLabel className="text-muted-foreground text-xs">{t("host.hosts")}</DropdownMenuLabel>
              {hosts.length === 0 && <div className="text-muted-foreground px-2 py-1.5 text-sm">{t("host.none")}</div>}
              {hosts.map((host) => (
                <div key={host.id} className="group/host flex items-center gap-0.5">
                  <DropdownMenuItem onClick={() => onSelect(host)} className="min-w-0 flex-1 gap-2.5 p-2">
                    <HostAvatar host={host} className="text-muted-foreground size-4" label={t(host.online ? "host.online" : "host.offline")} />
                    <span className="grid min-w-0 leading-tight">
                      <span className="truncate">{host.name}</span>
                      {hostSubtitle(host, t("host.personal")) && <span className="text-muted-foreground truncate text-xs">{hostSubtitle(host, t("host.personal"))}</span>}
                    </span>
                  </DropdownMenuItem>
                  {/* What a host offers sits behind its …, always shown so it is found. */}
                  {(mayManage(host) || mayRemove(host)) && (
                    <DropdownMenuSub>
                      <DropdownMenuSubTrigger
                        openOnHover={false}
                        className="text-muted-foreground size-8 justify-center p-0 [&>svg:last-child]:hidden"
                        aria-label={t("project.more", { name: host.name })}
                      >
                        <MoreHorizontalIcon />
                      </DropdownMenuSubTrigger>
                      <DropdownMenuSubContent>
                        {mayManage(host) && (
                          <DropdownMenuItem onClick={() => onSettings(host)}>
                            <SettingsIcon />
                            {t("host.settings")}
                          </DropdownMenuItem>
                        )}
                        {mayManage(host) && mayRemove(host) && <DropdownMenuSeparator />}
                        {mayRemove(host) && (
                          <DropdownMenuItem variant="destructive" onClick={() => onRemove(host)}>
                            <Trash2Icon />
                            {t("host.remove")}
                          </DropdownMenuItem>
                        )}
                      </DropdownMenuSubContent>
                    </DropdownMenuSub>
                  )}
                </div>
              ))}
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <DropdownMenuGroup>
              <DropdownMenuItem className="gap-2 p-2" onClick={onAdd}>
                <div className="flex size-6 items-center justify-center rounded-md border">
                  <PlusIcon className="size-4" />
                </div>
                <span className="text-muted-foreground font-medium">{t("host.add")}</span>
              </DropdownMenuItem>
            </DropdownMenuGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
