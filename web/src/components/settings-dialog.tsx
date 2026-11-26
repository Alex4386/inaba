import { timestampDate } from "@bufbuild/protobuf/wkt";
import { BellIcon, KeyboardIcon, KeyRoundIcon, PaletteIcon, SendIcon, SlidersHorizontalIcon, Trash2Icon, UserRoundIcon } from "lucide-react";
import { toast } from "sonner";
import { CodeFontPicker, UiFontPicker } from "@/components/font-picker";
import { ProfilePanel } from "@/components/profile-panel";
import { type PushDevice, PushPlatform, type User } from "@/gen/inaba/v1/server_pb";
import { useTheme } from "next-themes";
import { type ReactNode, useState } from "react";
import { type HostLink, notifications } from "@/api";
import { MutedList, NotificationChannels } from "@/components/notification-channels";
import { DataTable } from "@/components/data-table";
import { ConfirmDialog } from "@/components/dialogs";
import { KeybindingsPanel } from "@/components/keybindings-panel";
import { timeAgo } from "@/components/project-tree";
import { Badge } from "@/components/reui/badge";
import { ApiTokensPanel } from "@/components/api-tokens-panel";
import { Picker } from "@/components/picker";
import { Card, Row, SectionDialog } from "@/components/section-dialog";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ThemeEditor } from "@/components/theme-editor";
import { type Locale, type MessageKey, localeNames, useI18n } from "@/lib/i18n";
import { showError } from "@/lib/errors";
import { disablePush, enablePush, pushSupport, usePushDevices } from "@/lib/push";
import { setNotify, useNotify } from "@/lib/settings";

// Hosts, and what each one offers, are set in each host's own settings, from the host switcher.
export type SettingsSection = "profile" | "general" | "notifications" | "appearance" | "keyboard" | "tokens";

export function SettingsDialog({
  section,
  onSection,
  onClose,
  user,
  onUser,
  host,
}: {
  /** The connected host, whose snoozes are listed under Muted. */
  host?: { link: HostLink; hostName: string };
  /** The signed-in account, for its profile. */
  user: User;
  onUser: (user: User) => void;
  /** The section shown; the dialog is open while it is set. */
  section?: SettingsSection;
  onSection: (section: SettingsSection) => void;
  onClose: () => void;
}) {
  const { t, locale, setLocale } = useI18n();
  const { theme = "system", setTheme } = useTheme();
  const notify = useNotify();

  const sections: [SettingsSection, MessageKey, ReactNode][] = [
    ["profile", "profile.title", <UserRoundIcon />],
    ["general", "settings.general", <SlidersHorizontalIcon />],
    ["notifications", "notify.title", <BellIcon />],
    ["appearance", "settings.appearance", <PaletteIcon />],
    ["keyboard", "keys.title", <KeyboardIcon />],
    ["tokens", "user.apiTokens", <KeyRoundIcon />],
  ];

  return (
    <SectionDialog title={t("settings.title")} sections={sections.map(([id, label, icon]) => [id, t(label), icon])} section={section} onSection={onSection} onClose={onClose}>
      {(current) => (
        <>
          {current === "general" && (
            <div className="grid gap-4">
            <Card>
              <Row title={t("user.language")} description={t("settings.languageHelp")}>
                <Picker
                  label={t("user.language")}
                  value={locale}
                  onChange={setLocale}
                  options={(Object.keys(localeNames) as Locale[]).map((l) => ({
                    value: l,
                    label: localeNames[l],
                  }))}
                />
              </Row>
            </Card>
            </div>
          )}
          {/* Every way to hear about a thread: in this tab, by push and the other channels, and what is muted. */}
          {current === "notifications" && (
            // This browser, the other ways to be told, and what is muted: one tab each.
            <Tabs defaultValue="browser" className="gap-4">
              <TabsList>
                <TabsTrigger value="browser">{t("notify.browser")}</TabsTrigger>
                <TabsTrigger value="channels">{t("channel.title")}</TabsTrigger>
                {host && <TabsTrigger value="muted">{t("notify.muted")}</TabsTrigger>}
              </TabsList>
              <TabsContent value="browser" className="grid gap-4">
                <Card>
                  <Row title={t("settings.notify")} description={t("settings.notifyHelp")}>
                    <Switch checked={notify} onCheckedChange={setNotify} aria-label={t("settings.notify")} />
                  </Row>
                </Card>
                <PushSettings userId={user.id} />
              </TabsContent>
              <TabsContent value="channels" className="grid gap-4">
                <NotificationChannels />
              </TabsContent>
              {host && (
                <TabsContent value="muted" className="grid gap-4">
                  <h3 className="text-sm font-medium">{t("muted.title", { host: host.hostName })}</h3>
                  <MutedList link={host.link} />
                </TabsContent>
              )}
            </Tabs>
          )}
          {current === "appearance" && (
            <Card>
              <Row title={t("user.theme")} description={t("settings.themeHelp")}>
                <Picker
                  label={t("user.theme")}
                  value={theme}
                  onChange={setTheme}
                  options={[
                    { value: "system", label: t("user.themeSystem") },
                    { value: "light", label: t("user.themeLight") },
                    { value: "dark", label: t("user.themeDark") },
                  ]}
                />
              </Row>
              <Row title={t("uiFont.title")} description={t("uiFont.help")}>
                <UiFontPicker />
              </Row>
              <Row title={t("codeFont.title")} description={t("codeFont.help")}>
                <CodeFontPicker />
              </Row>
              <ThemeEditor />
            </Card>
          )}
          {current === "keyboard" && <KeybindingsPanel />}
          {current === "tokens" && <ApiTokensPanel />}
          {current === "profile" && <ProfilePanel user={user} onUser={onUser} />}
        </>
      )}
    </SectionDialog>
  );
}

const platformKey: Record<PushPlatform, MessageKey> = {
  [PushPlatform.UNSPECIFIED]: "settings.platform.web",
  [PushPlatform.WEB]: "settings.platform.web",
  [PushPlatform.APNS]: "settings.platform.apns",
  [PushPlatform.FCM]: "settings.platform.fcm",
};

/** Push on this browser, and every device of the account that gets push, to test or remove. */
function PushSettings({ userId }: { userId: string }) {
  const { t, locale } = useI18n();
  const { devices, endpoint, enabled, refresh } = usePushDevices();
  const [busy, setBusy] = useState(false);
  const [removing, setRemoving] = useState<PushDevice>();
  const support = pushSupport();
  const why: Record<typeof support, MessageKey> = { ok: "settings.pushHelp", unsupported: "settings.pushUnsupported", denied: "settings.pushDenied", "needs-install": "settings.pushInstall" };
  const toggle = (on: boolean) => {
    setBusy(true);
    // enablePush asks for permission first, so it has to start inside this click.
    (on
      ? enablePush(userId).then((done) => {
          // A dismissed prompt leaves permission at "default", which the description does not cover.
          if (!done && Notification.permission === "default") toast.error(t("settings.pushDismissed"));
        })
      : disablePush(devices)
    )
      .catch(showError)
      .finally(() => {
        setBusy(false);
        void refresh().catch(showError);
      });
  };
  const remove = (d: PushDevice) =>
    (d.endpoint === endpoint ? disablePush(devices) : notifications.deleteDevice({ id: d.id }).then(() => {})).then(() => refresh(), showError);

  return (
    <>
      <Card>
        <Row title={t("settings.push")} description={t(why[support])}>
          <Switch checked={enabled} disabled={busy || support !== "ok"} onCheckedChange={toggle} aria-label={t("settings.push")} />
        </Row>
      </Card>
      <h3 className="text-sm font-medium">{t("settings.devices")}</h3>
      <DataTable
        rows={devices}
        getRowKey={(d) => d.id}
        empty={{ icon: BellIcon, title: t("settings.devicesNone") }}
        columns={[
          {
            header: t("settings.device"),
            sortBy: (d) => d.label,
            cell: (d) => (
              <span className="flex items-center gap-2">
                <span className="truncate">{d.label || t("settings.platform.web")}</span>
                {d.endpoint === endpoint && (
                  <Badge size="sm" variant="primary-light">
                    {t("settings.deviceThis")}
                  </Badge>
                )}
              </span>
            ),
          },
          {
            header: t("settings.devicePlatform"),
            cell: (d) => (
              <Badge size="sm" variant="outline">
                {t(platformKey[d.platform])}
              </Badge>
            ),
          },
          {
            header: t("settings.deviceLastUsed"),
            sortBy: (d) => (d.lastUsedAt ? Number(d.lastUsedAt.seconds) : null),
            cell: (d) => (
              <span className="text-muted-foreground">{d.lastUsedAt ? timeAgo(timestampDate(d.lastUsedAt), locale, t("common.justNow")) : t("settings.deviceNever")}</span>
            ),
          },
        ]}
        rowActions={[
          {
            label: t("settings.deviceTest"),
            icon: SendIcon,
            onSelect: (d) => notifications.testDevice({ id: d.id }).then(() => (toast.success(t("settings.deviceTestSent")), refresh()), showError),
          },
          { label: t("settings.deviceRemove"), icon: Trash2Icon, destructive: true, separatorBefore: true, onSelect: setRemoving },
        ]}
      />
      <ConfirmDialog
        open={removing !== undefined}
        onClose={() => setRemoving(undefined)}
        title={t("settings.deviceRemoveTitle", { name: removing?.label || t("settings.platform.web") })}
        description={t("settings.deviceRemoveConfirm")}
        cancel={t("project.keep")}
        confirm={t("settings.deviceRemove")}
        destructive
        onConfirm={() => removing && remove(removing)}
      />
    </>
  );
}
