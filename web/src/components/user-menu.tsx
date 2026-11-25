import { BookOpenIcon, ChevronsUpDownIcon, LogOutIcon, SettingsIcon, ShieldIcon, ZapIcon } from "lucide-react";
import { UserAvatar } from "@/components/presence";
import { DropdownMenu, DropdownMenuContent, DropdownMenuGroup, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { SidebarMenu, SidebarMenuButton, SidebarMenuItem, useSidebar } from "@/components/ui/sidebar";
import { useI18n } from "@/lib/i18n";

/** The signed-in user: the way into settings, administration, the API reference, and signing out. */
export function UserMenu({ id, avatarUrl, name, onSettings, onAutomations, onAdmin, onSignOut }: { id: string; avatarUrl?: string; name: string; onSettings: () => void; /** Absent when the automations module is off or the account may not manage projects. */ onAutomations?: () => void; /** Absent for an account that is not an administrator. */ onAdmin?: () => void; onSignOut: () => void }) {
  const { isMobile } = useSidebar();
  const { t } = useI18n();
  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger render={<SidebarMenuButton size="lg" className="aria-expanded:bg-muted" />}>
            <UserAvatar id={id} name={name} url={avatarUrl} />
            <span className="flex-1 truncate text-left text-sm font-medium">{name}</span>
            <ChevronsUpDownIcon className="ml-auto size-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent className="min-w-52" side={isMobile ? "bottom" : "right"} align="end" sideOffset={4}>
            <DropdownMenuGroup>
              <DropdownMenuItem onClick={onSettings}>
                <SettingsIcon />
                {t("settings.title")}
              </DropdownMenuItem>
              {onAutomations && (
                <DropdownMenuItem onClick={onAutomations}>
                  <ZapIcon />
                  {t("auto.title")}
                </DropdownMenuItem>
              )}
              {onAdmin && (
                <DropdownMenuItem onClick={onAdmin}>
                  <ShieldIcon />
                  {t("admin.title")}
                </DropdownMenuItem>
              )}
              <DropdownMenuItem render={<a href="/docs" target="_blank" rel="noreferrer" />}>
                <BookOpenIcon />
                {t("user.apiDocs")}
              </DropdownMenuItem>
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={onSignOut}>
              <LogOutIcon />
              {t("user.signOut")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
