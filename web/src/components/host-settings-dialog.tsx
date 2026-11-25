import { BotIcon, ChartColumnIcon, FolderIcon, GlobeIcon, InfoIcon, PlugIcon, PuzzleIcon, UsersIcon } from "lucide-react";
import { type ReactNode, useEffect, useState } from "react";
import { onHost } from "@/api";
import { BrowserSettingsPanel } from "@/modules/browser/BrowserSettingsPanel";
import { McpServersPanel } from "@/modules/mcp/McpServersPanel";
import { HostAbout } from "@/components/host-about";
import { HostSharing } from "@/components/host-sharing";
import { HostUsage } from "@/components/host-usage";
import { ModulesPanel } from "@/components/modules-panel";
import { ProjectSettings } from "@/components/project-settings";
import { ProvidersPanel } from "@/components/providers-panel";
import { SectionDialog } from "@/components/section-dialog";
import { ModuleService } from "@/gen/inaba/v1/host_pb";
import type { Host } from "@/gen/inaba/v1/server_pb";
import { useI18n } from "@/lib/i18n";

export type HostSection = "about" | "usage" | "sharing" | "projects" | "providers" | "modules" | "mcp" | "browser";

/** One host's own settings: the agents it offers, its modules, and the MCP servers it gives them. */
export function HostSettingsDialog({
  host,
  section,
  onSection,
  onClose,
  connection,
  project: initialProject,
  canRename,
  onHostChanged,
  onModulesChanged,
  onOpenThread,
}: {
  host?: Host;
  /** The section shown; the dialog is open while it is set. */
  section?: HostSection;
  onSection: (section: HostSection) => void;
  onClose: () => void;
  /** A project to open straight to, under Projects. */
  project?: string;
  /** How this browser reaches the host, when it is the one in use. */
  connection?: "direct" | "relayed";
  /** Whether the account may rename the host, which its settings alone do not allow. */
  canRename: boolean;
  /** The host's name or look changed. */
  onHostChanged: () => void;
  /** A module was turned on or off. */
  onModulesChanged: () => void;
  /** A thread on this host was picked, such as from Usage. */
  onOpenThread: (id: string) => void;
}) {
  const { t } = useI18n();
  // MCP servers and the browser are modules'; their sections show while those are on.
  const [on, setOn] = useState<Set<string>>(new Set());
  // The project whose settings show under Projects, which the caller may open directly, and its name for the breadcrumb.
  const [project, setProject] = useState(initialProject);
  useEffect(() => setProject(initialProject), [initialProject]);
  const [projectName, setProjectName] = useState<string>();
  const [modulesRun, setModulesRun] = useState(0);
  const hostId = host?.online ? host.id : undefined;
  useEffect(() => {
    if (!hostId) return setOn(new Set());
    onHost(hostId, ModuleService)
      .listModules({})
      .then(
        (r) => setOn(new Set(r.modules.filter((m) => m.enabled && !m.error).map((m) => m.name))),
        () => setOn(new Set()),
      );
  }, [hostId, modulesRun]);
  return (
    <SectionDialog
      title={t("hostSettings.title", { name: host?.name ?? "" })}
      sections={[
        ["about", t("hostAbout.title"), <InfoIcon />],
        // Anyone who can use the host sees its usage: totals and thread titles they already see.
        ["usage", t("usage.title"), <ChartColumnIcon />],
        // A personal host is its owner's alone until they share it.
        // Who may invite: a personal host's owner, or any host's manager; administrators use the admin page.
        ...(host && (host.role === "owner" || host.role === "manager") ? ([["sharing", t("sharing.title"), <UsersIcon />]] as [HostSection, string, ReactNode][]) : []),
        ["projects", t("projectSettings.title"), <FolderIcon />],
        ["providers", t("providers.title"), <BotIcon />],
        ["modules", t("modules.title"), <PuzzleIcon />],
        ...(on.has("mcp") ? ([["mcp", t("mcp.title"), <PlugIcon />]] as [HostSection, string, ReactNode][]) : []),
        ...(on.has("browser") ? ([["browser", t("browserSettings.title"), <GlobeIcon />]] as [HostSection, string, ReactNode][]) : []),
      ]}
      section={host ? section : undefined}
      onSection={(s) => {
        setProject(undefined);
        onSection(s);
      }}
      page={section === "projects" && project && projectName ? { label: projectName, onBack: () => setProject(undefined) } : undefined}
      onClose={onClose}
    >
      {(current) =>
        !host ? null : current === "about" ? (
          <HostAbout key={host.id} host={host} connection={connection} canRename={canRename} onChanged={onHostChanged} />
        ) : current === "sharing" ? (
          <HostSharing key={host.id} host={host} />
        ) : !host.online ? (
          <p className="text-muted-foreground text-sm">{t("empty.offlineHelp")}</p>
        ) : current === "usage" ? (
          <HostUsage key={host.id} hostId={host.id} onOpenThread={onOpenThread} />
        ) : current === "projects" ? (
          <ProjectSettings key={host.id} hostId={host.id} open={project} onOpen={setProject} onName={setProjectName} />
        ) : current === "providers" ? (
          <ProvidersPanel key={host.id} hostId={host.id} />
        ) : current === "modules" ? (
          <ModulesPanel
            key={host.id}
            hostId={host.id}
            onChanged={() => {
              setModulesRun((n) => n + 1);
              onModulesChanged();
            }}
          />
        ) : current === "browser" ? (
          <BrowserSettingsPanel key={host.id} hostId={host.id} />
        ) : (
          <McpServersPanel key={host.id} hostId={host.id} />
        )
      }
    </SectionDialog>
  );
}
