import { useEffect, useState, type ReactNode } from "react";
import { XIcon } from "lucide-react";
import type { HostLink } from "@/api";
import { ProjectSchema, type Provider } from "@/gen/inaba/v1/host_pb";
import { Card, Row } from "@/components/section-dialog";
import { Logo } from "@/components/logo";
import { Badge } from "@/components/reui/badge";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { cached } from "@/lib/host-cache";
import { useI18n } from "@/lib/i18n";
import { landed } from "@/components/project-tree";

const dismissKey = (host: string) => `inaba.welcome.dismissed.${host}`;

/**
 * A first-run guide in place of the empty page while the host has no projects: whether its
 * agents can run, adding a project, and starting a thread. It goes once a project exists or
 * when dismissed on this browser; until then, and while the host is not yet looked at, the
 * usual empty page (children) shows.
 */
export function Welcome({
  link,
  hostId,
  onAddProject,
  onProviders,
  children,
}: {
  /** The connected host; without one only the children show. */
  link?: HostLink;
  hostId?: string;
  /** Opens the folder picker; left out when the user may not add projects on this host. */
  onAddProject?: () => void;
  /** Opens Host settings › Providers; left out when the user may not change them. */
  onProviders?: () => void;
  children: ReactNode;
}) {
  const { t } = useI18n();
  const [empty, setEmpty] = useState<boolean>();
  // null = the list failed to load, kept apart from an empty list so an error is not shown as "no agents".
  const [providers, setProviders] = useState<Provider[] | null>();
  const [dismissed, setDismissed] = useState(() => {
    try {
      return Boolean(hostId) && localStorage.getItem(dismissKey(hostId!)) === "1";
    } catch {
      return false;
    }
  });
  const shown = empty === true && !dismissed;

  useEffect(() => {
    setEmpty(undefined);
    if (!link || dismissed) return;
    // Polled like the sidebar's tree, so a project added from the picker hides the guide.
    // Stops at the first project: this page is the default landing, so a host with projects
    // would otherwise pay a second 3s poll forever.
    // shortcut: deleting every project later does not bring the guide back until a remount (host switch/reload).
    // The sidebar keeps the host's projects in the cache: once some are there, no call of its own is needed.
    let live = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const refresh = () => {
      if (cached(link.hostId, "projects", ProjectSchema)?.length) return setEmpty(false);
      link.projects.listProjects({}).then((r) => {
        if (!live) return;
        landed(link.hostId, "projects", ProjectSchema, r.projects);
        setEmpty(r.projects.length === 0);
        if (r.projects.length === 0) timer = setTimeout(refresh, 3_000);
      }, () => live && (timer = setTimeout(refresh, 3_000)));
    };
    refresh();
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [link, dismissed]);

  useEffect(() => {
    if (link && shown) link.threads.listProviders({}).then((r) => setProviders(r.providers), () => setProviders(null));
  }, [link, shown]);

  if (!shown || !hostId) return children;

  const dismiss = () => {
    try {
      localStorage.setItem(dismissKey(hostId), "1");
    } catch {
      // Without storage it stays dismissed only for this page.
    }
    setDismissed(true);
  };

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="mx-auto grid w-full max-w-2xl gap-6 px-4 py-8">
        <div className="flex justify-end">
          <Button variant="ghost" size="sm" onClick={dismiss}>
            <XIcon />
            {t("welcome.dismiss")}
          </Button>
        </div>
        <Empty className="p-0 md:p-0">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <Logo />
            </EmptyMedia>
            <EmptyTitle>{t("welcome.title")}</EmptyTitle>
            <EmptyDescription>{t("welcome.help")}</EmptyDescription>
          </EmptyHeader>
        </Empty>

        <section className="grid gap-2">
          <h2 className="text-sm font-medium">{t("welcome.agents")}</h2>
          <Card>
            {providers === undefined ? (
              <Row title={t("empty.loading")} description="">
                {null}
              </Row>
            ) : providers === null ? (
              <Row title={t("welcome.agentsError")} description="">
                {null}
              </Row>
            ) : providers.length === 0 ? (
              <Row title={t("welcome.noAgents")} description={onProviders ? t("welcome.noAgentsHelp") : t("welcome.noAgentsAskOwner")}>
                <Badge variant="warning-light">{t("welcome.notReady")}</Badge>
              </Row>
            ) : (
              // shortcut: "Ready" means only that the provider is structured; Provider has no installed/available field, add one server-side to check the command exists.
              providers.map((p) => (
                <Row key={p.id} title={p.displayName || p.id} description={p.structured ? t("welcome.agentChat") : t("welcome.agentTerminal")}>
                  <Badge variant={p.structured ? "success-light" : "info-light"}>{p.structured ? t("welcome.ready") : t("welcome.terminalOnly")}</Badge>
                </Row>
              ))
            )}
            {onProviders && (
              <Row title={t("welcome.providers")} description={t("welcome.providersHelp")}>
                <Button variant="outline" size="sm" onClick={onProviders}>
                  {t("welcome.openProviders")}
                </Button>
              </Row>
            )}
          </Card>
        </section>

        <section className="grid gap-2">
          <h2 className="text-sm font-medium">{t("welcome.project")}</h2>
          <Card>
            <Row title={t("project.add")} description={onAddProject ? t("welcome.projectHelp") : t("welcome.projectNoPermission")}>
              {onAddProject && (
                <Button size="sm" onClick={onAddProject}>
                  {t("project.add")}
                </Button>
              )}
            </Row>
          </Card>
        </section>

        <section className="grid gap-2">
          <h2 className="text-sm font-medium">{t("welcome.thread")}</h2>
          <Card>
            <Row title={t("thread.new")} description={t("welcome.threadHelp")}>
              {null}
            </Row>
          </Card>
        </section>
      </div>
    </div>
  );
}
