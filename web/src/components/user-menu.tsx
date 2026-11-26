import { BookOpenIcon, ChevronsUpDownIcon, LanguagesIcon, LogOutIcon, SettingsIcon, ShieldIcon, SunMoonIcon, ZapIcon } from "lucide-react";
import { useTheme } from "next-themes";
import { UserAvatar } from "@/components/presence";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { SidebarMenu, SidebarMenuButton, SidebarMenuItem, useSidebar } from "@/components/ui/sidebar";
import { type Locale, localeNames, useI18n } from "@/lib/i18n";

/** The signed-in user: the way into settings, administration, the API reference, and signing out. */
export function UserMenu({ id, avatarUrl, name, onSettings, onAutomations, onAdmin, onSignOut }: { id: string; avatarUrl?: string; name: string; onSettings: () => void; /** Absent when the automations module is off or the account may not manage projects. */ onAutomations?: () => void; /** Absent for an account that is not an administrator. */ onAdmin?: () => void; onSignOut: () => void }) {
  const { isMobile } = useSidebar();
  const { t, locale, setLocale } = useI18n();
  const { theme = "system", setTheme } = useTheme();
  const themes = { system: t("user.themeSystem"), light: t("user.themeLight"), dark: t("user.themeDark") };
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
            {/* What people change often, here rather than only in settings. */}
            <DropdownMenuGroup>
              <DropdownMenuSub>
                <DropdownMenuSubTrigger>
                  <SunMoonIcon />
                  {t("user.theme")}
                  <span className="text-muted-foreground ml-auto pl-2 text-xs">{themes[theme as keyof typeof themes]}</span>
                </DropdownMenuSubTrigger>
                <DropdownMenuSubContent>
                  <DropdownMenuRadioGroup value={theme} onValueChange={setTheme}>
                    {Object.entries(themes).map(([value, label]) => (
                      <DropdownMenuRadioItem key={value} value={value}>
                        {label}
                      </DropdownMenuRadioItem>
                    ))}
                  </DropdownMenuRadioGroup>
                </DropdownMenuSubContent>
              </DropdownMenuSub>
              <DropdownMenuSub>
                <DropdownMenuSubTrigger>
                  <LanguagesIcon />
                  {t("user.language")}
                  <span className="text-muted-foreground ml-auto pl-2 text-xs">{localeNames[locale]}</span>
                </DropdownMenuSubTrigger>
                <DropdownMenuSubContent>
                  <DropdownMenuRadioGroup value={locale} onValueChange={(l: Locale) => setLocale(l)}>
                    {(Object.keys(localeNames) as Locale[]).map((l) => (
                      <DropdownMenuRadioItem key={l} value={l}>
                        {localeNames[l]}
                      </DropdownMenuRadioItem>
                    ))}
                  </DropdownMenuRadioGroup>
                </DropdownMenuSubContent>
              </DropdownMenuSub>
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
