import { Code, ConnectError } from "@connectrpc/connect";
import { KeyRoundIcon, LanguagesIcon, SearchIcon, ServerOffIcon, SunMoonIcon } from "lucide-react";
import { signInWithPasskey } from "@/components/profile-panel";
import { type FormEvent, type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useLocation, useRoute } from "wouter";
import { HostLink, auth, hosts as hostsApi } from "./api";
import { type GetConfigResponse, type Host, HostSchema, Permission, type User } from "./gen/inaba/v1/server_pb";
import type { Project } from "./gen/inaba/v1/host_pb";
import { NewThreadView } from "./NewThread";
import { ThreadView } from "./Thread";
import { CommandPalette, type PalettePage } from "@/components/command-palette";
import { ConfirmDialog } from "@/components/dialogs";
import { AddHostDialog, DEVICE_CODE_KEY, DeviceApproval } from "@/components/host-login";
import { HostSettingsDialog, type HostSection } from "@/components/host-settings-dialog";
import { InviteDialog } from "@/components/invite-dialog";
import { ProjectsPage, ProjectThreadsPage } from "@/components/projects-pages";
import { HostSwitcher } from "@/components/host-switcher";
import { PresenceContext, usePresence } from "@/components/presence";
import { Logo } from "@/components/logo";
import { RecentThreads } from "@/components/recent-threads";
import { Welcome } from "@/components/onboarding";
import { Alert, AlertDescription } from "@/components/reui/alert";
import { AutomationsView } from "./modules/automations/AutomationsView";
import { HostTerminalPage, TerminalTree } from "./components/terminals";
import { AdminNav, AdminPage, type AdminSection } from "@/components/admin-dialog";
import { SettingsDialog, type SettingsSection } from "@/components/settings-dialog";
import { ProjectTree } from "@/components/project-tree";
import { SidebarResizeRail } from "@/components/sidebar-resize-rail";
import { Button, buttonVariants } from "@/components/ui/button";
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { Sidebar, SidebarContent, SidebarFooter, SidebarHeader, SidebarInset, SidebarMenu, SidebarMenuButton, SidebarMenuItem, SidebarProvider, SidebarTrigger } from "@/components/ui/sidebar";
import { UserMenu } from "@/components/user-menu";
import { errorText, showError } from "@/lib/errors";
import { cached, cacheUser, clearCache, keep } from "@/lib/host-cache";
import { poll } from "@/lib/poll";
import { disablePush, syncPush } from "@/lib/push";
import { bindingOf, formatBinding, matches, useKeybindings } from "@/lib/keybindings";
import { type Locale, localeNames, useI18n } from "@/lib/i18n";
import { useTheme } from "next-themes";
import { Picker } from "@/components/picker";
import { toast } from "sonner";

export function App() {
  const [config, setConfig] = useState<GetConfigResponse>();
  // undefined while checking, null when signed out.
  const [user, setUser] = useState<User | null>();
  const [bootError, setBootError] = useState("");
  const boot = useCallback(() => {
    setBootError("");
    auth.getConfig({}).then(setConfig, (e) => setBootError(errorText(e)));
    auth.whoAmI({}).then(setUser, () => setUser(null));
  }, []);
  useEffect(boot, [boot]);

  if (bootError) return <BootError error={bootError} onRetry={boot} />;
  if (!config || user === undefined) return null;
  // A machine's sign-in link: the code waits through signing in, which may leave the page.
  if (location.pathname === "/ui/device") {
    sessionStorage.setItem(DEVICE_CODE_KEY, new URLSearchParams(location.search).get("code") ?? "");
    history.replaceState(null, "", "/ui/");
  }
  const deviceCode = sessionStorage.getItem(DEVICE_CODE_KEY);
  if (user && deviceCode !== null) {
    return (
      <DeviceApproval
        code={deviceCode}
        canShare={user.admin || user.permissions.includes(Permission.MANAGE_HOSTS)}
        onDone={(host) => {
          sessionStorage.removeItem(DEVICE_CODE_KEY);
          if (host) localStorage.setItem(HOST_KEY, host.id);
          setUser({ ...user });
        }}
      />
    );
  }
  if (user === null && location.pathname === "/ui/onboarding") {
    return (
      <Onboarding
        onDone={(created) => {
          // Leave the setup address, and its token, out of the history.
          history.replaceState(null, "", "/ui/");
          setUser(created);
        }}
      />
    );
  }
  if (user === null) return <SignIn config={config} onSignedIn={setUser} />;
  return <Workspace config={config} user={user} onUser={setUser} onSignedOut={() => setUser(null)} />;
}

/** The server could not be reached on load: say so, with a way to try again, not a blank page. */
function BootError({ error, onRetry }: { error: string; onRetry: () => void }) {
  const { t } = useI18n();
  return (
    <main className="bg-muted grid min-h-svh place-items-center p-4">
      <div className="grid w-full max-w-sm gap-4">
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
        <Button onClick={onRetry}>{t("common.retry")}</Button>
        <AuthPrefs />
      </div>
    </main>
  );
}

function SignIn({ config, onSignedIn }: { config: GetConfigResponse; onSignedIn: (user: User) => void }) {
  const { t } = useI18n();
  const [error, setError] = useState("");
  // Set once the password checked out for an account that also asks for an app's code.
  const [needCode, setNeedCode] = useState(false);
  const [busy, setBusy] = useState<"password" | "passkey">();
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy) return;
    const form = new FormData(event.currentTarget);
    setBusy("password");
    try {
      onSignedIn(await auth.login({ username: String(form.get("username")), password: String(form.get("password")), totpCode: String(form.get("totp") ?? "") }));
    } catch (e) {
      if (ConnectError.from(e).code === Code.FailedPrecondition && !needCode) {
        setNeedCode(true);
        setError("");
        return;
      }
      setError(errorText(e));
    } finally {
      setBusy(undefined);
    }
  };
  // An SSO sign-in that did not finish comes back here with a fixed code saying why.
  const ssoError = ({ expired: "signIn.ssoExpired", provider: "signIn.ssoProvider", refused: "signIn.ssoRefused", session: "signIn.ssoSession" } as const)[
    new URLSearchParams(location.search).get("sso_error") as "expired"
  ];
  const passkey = () => {
    setBusy("passkey");
    return signInWithPasskey()
      .then(onSignedIn, (e) => {
        // Closing the browser's own prompt is a choice, not a failure.
        if (!(e instanceof DOMException && e.name === "NotAllowedError")) setError(errorText(e));
      })
      .finally(() => setBusy(undefined));
  };
  return (
    <AuthCard onSubmit={submit}>
      {config.needsOnboarding && (
        <Alert variant="info">
          <AlertDescription>{t("signIn.needsOnboarding")}</AlertDescription>
        </Alert>
      )}
      {/* The name and password stay in the form, hidden, for the second step's submit. */}
      <div className={needCode ? "hidden" : "grid gap-4"}>
        <Field>
          <FieldLabel htmlFor="username">{t("signIn.username")}</FieldLabel>
          <Input id="username" name="username" autoComplete="username webauthn" autoFocus required />
        </Field>
        <Field>
          <FieldLabel htmlFor="password">{t("signIn.password")}</FieldLabel>
          <Input id="password" name="password" type="password" autoComplete="current-password" required />
        </Field>
      </div>
      {needCode && (
        <Field>
          <FieldLabel htmlFor="totp">{t("profile.totpCode")}</FieldLabel>
          <Input id="totp" name="totp" inputMode="numeric" autoComplete="one-time-code" placeholder="123456" maxLength={6} required autoFocus />
          <p className="text-muted-foreground text-xs">{t("signIn.codeHelp")}</p>
        </Field>
      )}
      {(error || ssoError) && (
        <Alert variant="destructive">
          <AlertDescription>{error || t(ssoError!)}</AlertDescription>
        </Alert>
      )}
      <Button type="submit" disabled={!!busy}>
        {busy === "password" && <Spinner data-icon="inline-start" />}
        {t("signIn.submit")}
      </Button>
      {!needCode && typeof PublicKeyCredential !== "undefined" && "parseRequestOptionsFromJSON" in PublicKeyCredential && (
        <Button type="button" variant="outline" disabled={!!busy} onClick={() => void passkey()}>
          {busy === "passkey" ? <Spinner data-icon="inline-start" /> : <KeyRoundIcon data-icon="inline-start" />}
          {t("signIn.passkey")}
        </Button>
      )}
      {config.oidcEnabled && (
        <a className={buttonVariants({ variant: "outline" })} href="/auth/oidc/login">
          {t("signIn.with", { name: config.oidcName })}
        </a>
      )}
    </AuthCard>
  );
}

function AuthCard({ children, onSubmit }: { children: ReactNode; onSubmit: (event: FormEvent<HTMLFormElement>) => void }) {
  return (
    <main className="bg-muted grid min-h-svh place-items-center p-4">
      <div className="grid w-full max-w-sm gap-6">
        <div className="flex items-center justify-center gap-2 text-lg font-semibold">
          <div className="bg-primary text-primary-foreground flex size-8 items-center justify-center rounded-lg">
            <Logo className="size-5" />
          </div>
          inaba
        </div>
        <form onSubmit={onSubmit} className="bg-card grid gap-4 rounded-xl border p-6 shadow-xs">
          {children}
        </form>
        <AuthPrefs />
      </div>
    </main>
  );
}

/** Language, then theme under it, for whoever has not signed in yet to reach the settings. */
function AuthPrefs() {
  const { t, locale, setLocale } = useI18n();
  const { theme = "system", setTheme } = useTheme();
  return (
    <div className="grid justify-items-center gap-2">
      <div className="inline-flex items-center gap-1">
        <LanguagesIcon aria-hidden className="text-muted-foreground size-4" />
        <Picker
          size="sm"
          label={t("user.language")}
          value={locale}
          onChange={setLocale}
          options={(Object.keys(localeNames) as Locale[]).map((l) => ({ value: l, label: localeNames[l] }))}
        />
      </div>
      <div className="inline-flex items-center gap-1">
        <SunMoonIcon aria-hidden className="text-muted-foreground size-4" />
        <Picker
          size="sm"
          label={t("user.theme")}
          value={theme}
          onChange={setTheme}
          options={[
            { value: "system", label: t("user.themeSystem") },
            { value: "light", label: t("user.themeLight") },
            { value: "dark", label: t("user.themeDark") },
          ]}
        />
      </div>
    </div>
  );
}

/** First-time setup, opened from the link the server prints: /onboarding#token=... */
function Onboarding({ onDone }: { onDone: (user: User) => void }) {
  const { t } = useI18n();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const token = new URLSearchParams(location.hash.slice(1)).get("token") ?? "";
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy) return;
    const form = new FormData(event.currentTarget);
    setBusy(true);
    try {
      onDone(await auth.onboard({ token, username: String(form.get("username")), password: String(form.get("password")) }));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <AuthCard onSubmit={submit}>
      <div className="grid gap-1">
        <h1 className="font-medium">{t("onboarding.title")}</h1>
        <p className="text-muted-foreground text-sm">{token ? t("onboarding.description") : t("onboarding.noToken")}</p>
      </div>
      {token && (
        <>
          <Field>
            <FieldLabel htmlFor="username">{t("signIn.username")}</FieldLabel>
            <Input id="username" name="username" autoComplete="username" autoFocus required />
          </Field>
          <Field>
            <FieldLabel htmlFor="password">{t("onboarding.password")}</FieldLabel>
            <Input id="password" name="password" type="password" autoComplete="new-password" minLength={8} required />
          </Field>
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <Button type="submit" disabled={busy}>
            {busy && <Spinner data-icon="inline-start" />}
            {t("onboarding.submit")}
          </Button>
        </>
      )}
    </AuthCard>
  );
}

const HOST_KEY = "inaba.host";

function Workspace({ config, user, onUser, onSignedOut }: { config: GetConfigResponse; user: User; onUser: (user: User) => void; onSignedOut: () => void }) {
  const { t } = useI18n();
  // The hosts as this account last saw them, so the switcher and the sidebar show before the server
  // answers; listed says the server has answered since, and only then is a host taken to be gone or offline.
  cacheUser(user.id);
  const hostsCache = `server.${user.id}`;
  const [hostList, setHostList] = useState(() => cached(hostsCache, "hosts", HostSchema));
  const [listed, setListed] = useState(false);
  // Where one is lives in the address, so it can be shared, reloaded and gone back to:
  // /hosts/:host, and under it threads/:thread, projects/:project or automations; or /admin/:section.
  const [, navigate] = useLocation();
  const [, onThread] = useRoute<{ host: string; thread: string }>("/hosts/:host/threads/:thread");
  const [, onProject] = useRoute<{ host: string; project: string }>("/hosts/:host/projects/:project");
  const [onAutomations, automationsAt] = useRoute<{ host: string }>("/hosts/:host/automations");
  // Every project, and every thread of one: pages for what the sidebar keeps short.
  const [onProjects, projectsAt] = useRoute<{ host: string }>("/hosts/:host/projects");
  // The host's terminals, apart from its projects and threads.
  const [, onTerminal] = useRoute<{ host: string; terminal: string }>("/hosts/:host/terminals/:terminal");
  const [, onProjectThreads] = useRoute<{ host: string; project: string }>("/hosts/:host/projects/:project/threads");
  const [, onHost] = useRoute<{ host: string }>("/hosts/:host");
  const [onAdmin, adminAt] = useRoute<{ section?: string; item?: string; tab?: string }>("/admin/:section?/:item?/:tab?");
  const [onInvite, inviteAt] = useRoute<{ token: string }>("/invite/:token");
  // The host in the address, else the one used last, like a workspace switcher reopens on; that one
  // only if it is in this account's list, as the browser may last have been used by another account.
  const remembered = localStorage.getItem(HOST_KEY);
  const hostId = onThread?.host ?? onProject?.host ?? automationsAt?.host ?? projectsAt?.host ?? onTerminal?.host ?? onProjectThreads?.host ?? onHost?.host ?? (hostList?.some((h) => h.id === remembered) ? remembered! : undefined);
  const threadId = onThread?.thread;
  const automating = onAutomations;
  const administering = onAdmin ? ((adminAt?.section || "overview") as AdminSection) : undefined;
  const [addingHost, setAddingHost] = useState(false);
  const [hostSettings, setHostSettings] = useState<{ host: Host; section: HostSection; project?: string }>();
  const [removingHost, setRemovingHost] = useState<Host>();
  const [settings, setSettings] = useState<SettingsSection>();
  const [palette, setPalette] = useState<PalettePage>();
  // A clicked push notification moves this tab to its thread; the router's base is /ui.
  useEffect(() => {
    if (!("serviceWorker" in navigator)) return;
    const onMessage = (e: MessageEvent) => {
      const url = e.data?.navigate;
      if (typeof url === "string" && url.startsWith("/ui/")) navigate(url.slice(3));
    };
    navigator.serviceWorker.addEventListener("message", onMessage);
    return () => navigator.serviceWorker.removeEventListener("message", onMessage);
  }, [navigate]);
  // Keeps this browser's push subscription registered to this account.
  useEffect(() => {
    syncPush(user.id).catch(() => {});
  }, [user.id]);
  // The palette shortcut (Cmd/Ctrl+K unless changed) opens it from anywhere.
  useKeybindings();
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!matches(e, "palette")) return;
      e.preventDefault();
      setPalette((p) => (p ? undefined : "root"));
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, []);
  // The project a new thread is being started in, while that screen is open: the one picked, or
  // for an address opened directly, looked up on the host.
  const draftId = onProject?.project;
  const [picked, setPicked] = useState<Project>();
  const [looked, setLooked] = useState<Project>();
  const draft = draftId ? ([picked, looked].find((p) => p?.id === draftId)) : undefined;
  const canManageHosts = user.admin || user.permissions.includes(Permission.MANAGE_HOSTS);
  // What the caller may do on a host, as the server tells it (see grantsOn): an owner or an
  // administrator anything, a member or manager everything on the host itself (adding shared
  // hosts is not about one host), and on a shared host the account's own permissions too.
  const canOn = (h: Host | undefined, permission: Permission) => {
    if (user.admin || h?.role === "owner") return true;
    if (!h) return false;
    if ((h.role === "manager" || h.role === "member") && permission !== Permission.MANAGE_HOSTS) return true;
    return h.shared && user.permissions.includes(permission);
  };
  // A host's settings are for whoever may change them (the server's manageable).
  const mayManage = (h: Host) => canOn(h, Permission.HOST_SETTINGS);
  // Renaming or removing a host is its owner's, or for a shared host whoever may add shared
  // hosts (the server's removable); a member or manager of someone's host does not.
  const mayRemove = (h: Host) => user.admin || h.role === "owner" || (h.shared && canManageHosts);
  // Bumped when the link switches between relay and direct, to re-render.
  const [, setLinkVersion] = useState(0);

  // Hosts the server has said are offline twice running, or in its first answer: a host that drops
  // for a moment keeps its link, as making it anew restarts every stream and terminal on it.
  const [down, setDown] = useState<Set<string>>(new Set());
  const lastDown = useRef<Set<string>>(undefined);
  const refreshHosts = useCallback(
    () =>
      hostsApi.listHosts({}).then((r) => {
        keep(hostsCache, "hosts", HostSchema, r.hosts);
        const now = new Set(r.hosts.filter((h) => !h.online).map((h) => h.id));
        const was = lastDown.current;
        lastDown.current = now;
        setDown((d) => {
          const next = was ? new Set([...now].filter((id) => was.has(id))) : now;
          return next.size === d.size && [...next].every((id) => d.has(id)) ? d : next;
        });
        setHostList(r.hosts);
        setListed(true);
      }, showError),
    [hostsCache],
  );
  useEffect(() => poll(refreshHosts, 5_000), [refreshHosts]);

  // Someone else's personal host an administrator went into from the admin panel, which their own list leaves out.
  // shortcut: its online state is as it was when looked up; reopen it to see a change.
  const [visited, setVisited] = useState<Host>();
  const listedHost = hostList?.find((h) => h.id === hostId);
  useEffect(() => {
    if (user.admin && listed && hostId && !listedHost && visited?.id !== hostId) hostsApi.listHosts({ all: true }).then((r) => setVisited(r.hosts.find((h) => h.id === hostId)), showError);
  }, [user.admin, listed, hostId, listedHost, visited?.id]);
  // A lone host stands in for one the address names only once the server says that one is not there.
  const host = listedHost ?? (visited?.id === hostId ? visited : undefined) ?? (hostList?.length === 1 && (listed || !hostId) ? hostList[0] : undefined);
  // The host in the address, or the one used last, is connected to at once rather than after the
  // host list answers: on a slow link the sidebar and the thread start loading that much sooner.
  const activeId = host?.id ?? (listed ? undefined : hostId);
  // A visited host is not in this account's list, so its own flag says whether it is offline.
  const offline = listed && host !== undefined && (down.has(host.id) || (host === visited && !host.online));
  // A host that goes away while it is open, as it does when it restarts, keeps its link and what is on screen:
  // its streams wait for it and pick up where they were. Only one offline from the start shows as not connected.
  const reached = useRef<string>(undefined);
  // Only once the server has said it is there: before its first answer, offline is not known yet.
  if (activeId && listed && !offline) reached.current = activeId;
  const away = offline && reached.current === activeId;
  // The start page of a host that is there: inaba's own, with the threads last at work.
  const homePage = (hostList?.length ?? 0) > 0 && Boolean(host) && !(offline && !away);
  const link = useMemo(
    () => (activeId && (!offline || away) ? new HostLink(activeId, config.iceUrls, () => setLinkVersion((v) => v + 1)) : undefined),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [activeId, offline && !away, config.iceUrls],
  );
  // Back: everything waiting on it tries again at once.
  useEffect(() => {
    if (!offline) link?.wake();
  }, [link, offline]);
  useEffect(() => () => link?.close(), [link]);
  const draftKnown = Boolean(draft);
  useEffect(() => {
    if (!link || !draftId || draftKnown) return;
    link.projects.listProjects({}).then((r) => setLooked(r.projects.find((p) => p.id === draftId)), showError);
  }, [link, draftId, draftKnown]);
  // The address names its host, so a reload or a shared link opens the same place; the host used last is remembered.
  const shownHost = activeId;
  useEffect(() => {
    if (!shownHost) return;
    // A host looked into from the admin panel is not the one to reopen on.
    if (hostList?.some((h) => h.id === shownHost)) localStorage.setItem(HOST_KEY, shownHost);
    if (!onThread && !onProject && !onAutomations && !onProjects && !onTerminal && !onProjectThreads && !onHost && !onAdmin && !onInvite) navigate(`/hosts/${shownHost}`, { replace: true });
  }, [shownHost, hostList, onThread, onProject, onAutomations, onProjects, onTerminal, onProjectThreads, onHost, onAdmin, onInvite, navigate]);
  const home = (h = shownHost) => navigate(h ? `/hosts/${h}` : "/");
  const openThread = (id: string) => navigate(`/hosts/${shownHost}/threads/${id}`);
  const threadSettings = (id: string) => navigate(`/hosts/${shownHost}/threads/${id}?settings`);
  const projectSettings = host && mayManage(host) ? (p: Project) => setHostSettings({ host, section: "projects", project: p.id }) : undefined;
  const openProject = (project: Project) => {
    setPicked(project);
    navigate(`/hosts/${shownHost}/projects/${project.id}`);
  };
  // The host's optional parts that are on; what belongs to the others stays hidden.
  const [modulesOn, setModulesOn] = useState<Set<string>>(new Set());
  const refreshModules = useCallback(() => {
    if (!link) return;
    link.modules.listModules({}).then(
      (r) => setModulesOn(new Set(r.modules.filter((m) => m.enabled && !m.error).map((m) => m.name))),
      () => setModulesOn(new Set()),
    );
  }, [link]);
  useEffect(() => {
    refreshModules();
  }, [refreshModules]);


  // Who else is on this host, and where; this tab says where it is as it goes.
  const present = usePresence(host?.id, threadId, draftId, onTerminal?.terminal);

  const selectHost = (next: Host) => home(next.id);

  return (
    <PresenceContext.Provider value={present}>
    <SidebarProvider className="fixed inset-x-0 top-(--vvt) h-(--vvh) min-h-0">
      <Sidebar collapsible="icon">
        {administering ? (
          <SidebarContent>
            <AdminNav section={administering} onSection={(section) => navigate(`/admin/${section}`)} onLeave={() => home()} />
          </SidebarContent>
        ) : (
          <>
        <SidebarHeader>
          <HostSwitcher
            hosts={hostList ?? []}
            active={host}
            onSelect={selectHost}
            onAdd={() => setAddingHost(true)}
            onSettings={(h) => setHostSettings({ host: h, section: "about" })}
            onRemove={setRemovingHost}
            mayManage={mayManage}
            mayRemove={mayRemove}
          />
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton tooltip={t("search.open")} onClick={() => setPalette("root")}>
                <SearchIcon />
                <span className="text-muted-foreground">{t("search.open")}</span>
                <kbd className="text-muted-foreground ml-auto font-sans text-xs">{formatBinding(bindingOf("palette"))}</kbd>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarHeader>
        <SidebarContent>
          {link && canOn(host, Permission.THREADS_ANYWHERE) && modulesOn.has("terminal") && (
            <TerminalTree key={activeId} link={link} selected={onTerminal?.terminal} onOpen={(id) => navigate(`/hosts/${shownHost}/terminals/${id}`)} />
          )}
          {link && <ProjectTree
              key={activeId}
              link={link}
              selected={threadId}
              openProject={draft?.id}
              onSelect={(id) => (id ? openThread(id) : home())}
              onThreadSettings={threadSettings}
              onNewThread={openProject}
              onAddProject={() => setPalette("folder")}
              onAllProjects={() => navigate(`/hosts/${shownHost}/projects`)}
              onProjectThreads={(p) => navigate(`/hosts/${shownHost}/projects/${p.id}/threads`)}
              onProjectSettings={projectSettings}
              canManage={canOn(host, Permission.MANAGE_PROJECTS)}
            />}</SidebarContent>
          </>
        )}
        <SidebarFooter>
          <UserMenu
            id={user.id}
            avatarUrl={user.avatarUrl}
            name={user.name}
            onSettings={() => setSettings("profile")}
            onAutomations={
              canOn(host, Permission.AUTOMATIONS) && link && modulesOn.has("automations")
                ? () => navigate(`/hosts/${shownHost}/automations`)
                : undefined
            } onAdmin={user.admin ? () => navigate("/admin/overview") : undefined} onSignOut={() => {
              const pending = toast.loading(t("user.signOut"));
              // Whoever signs in next on this browser must not get this account's notifications.
              disablePush()
                .catch(() => {})
                .then(() => auth.logout({}))
                .then(clearCache, clearCache)
                .then(() => {
                  // Nor land on this account's thread or host.
                  localStorage.removeItem(HOST_KEY);
                  navigate("/", { replace: true });
                })
                .then(onSignedOut, onSignedOut)
                .finally(() => toast.dismiss(pending));
            }} />
        </SidebarFooter>
        <SidebarResizeRail />
      </Sidebar>
      <SidebarInset className="min-h-0 min-w-0">
        {away && host && (
          <div role="status" className="bg-warning/15 text-foreground flex h-8 shrink-0 items-center justify-center gap-2 border-b px-3 text-xs">
            <Spinner className="size-3.5" />
            {t("host.reconnecting", { name: host.name })}
          </div>
        )}
        {administering ? (
          <AdminPage
            section={administering}
            item={adminAt?.item}
            tab={adminAt?.tab}
            onAddHost={() => setAddingHost(true)}
            onItem={(section, id, tab) => navigate(["/admin", section, id, tab].filter(Boolean).join("/"))}
            me={user.id}
            hosts={hostList ?? []}
            onOpenHost={(h) => {
              setVisited(h);
              navigate(`/hosts/${h.id}`);
            }}
          />
        ) : link && onProjects ? (
          <ProjectsPage
            link={link}
            onOpen={(p) => navigate(`/hosts/${shownHost}/projects/${p.id}/threads`)}
            onAddProject={canOn(host, Permission.MANAGE_PROJECTS) ? () => setPalette("folder") : undefined}
            onNewThread={openProject}
            onProjectSettings={projectSettings}
            canManage={canOn(host, Permission.MANAGE_PROJECTS)}
          />
        ) : link && onTerminal ? (
          <HostTerminalPage key={onTerminal.terminal} link={link} id={onTerminal.terminal} onClosed={() => home()} stealth={user.admin && user.stealth} />
        ) : link && onProjectThreads ? (
          <ProjectThreadsPage
            key={onProjectThreads.project}
            link={link}
            projectId={onProjectThreads.project}
            onProjects={() => navigate(`/hosts/${shownHost}/projects`)}
            onOpen={openThread}
            onNewThread={openProject}
            onProjectSettings={projectSettings}
            onThreadSettings={threadSettings}
            canManage={canOn(host, Permission.MANAGE_PROJECTS)}
          />
        ) : link && draft ? (
          <NewThreadView
            key={draft.id}
            link={link}
            host={host}
            project={draft}
            onCreated={openThread}
          />
        ) : link && automating && !threadId && modulesOn.has("automations") ? (
          <AutomationsView
            link={link}
            onOpenThread={openThread}
          />
        ) : link && threadId ? (
          <ThreadView key={threadId} link={link} host={host} threadId={threadId} onEnded={() => home()} onOpen={openThread} onProject={(p) => navigate(`/hosts/${shownHost}/projects/${p.id}/threads`)} modules={modulesOn} admin={user.admin} stealth={user.admin && user.stealth} />
        ) : (
          <>
            <header className="flex h-12 shrink-0 items-center px-3">
              <SidebarTrigger />
            </header>
            <Welcome
              key={activeId}
              link={link}
              hostId={activeId}
              onAddProject={canOn(host, Permission.MANAGE_PROJECTS) ? () => setPalette("folder") : undefined}
              onProviders={host && mayManage(host) ? () => setHostSettings({ host, section: "providers" }) : undefined}
            >
            <Empty>
              <EmptyHeader>
                <EmptyMedia variant="icon">{offline && !away ? <ServerOffIcon /> : <Logo />}</EmptyMedia>
                <EmptyTitle className={homePage ? "text-lg" : undefined}>{!hostList || (!host && !listed) ? t("empty.loading") : hostList.length === 0 ? t("empty.firstHost") : !host ? t("empty.chooseHost") : offline && !away ? t("empty.offline", { name: host.name }) : "inaba"}</EmptyTitle>
                <EmptyDescription>
                  {hostList?.length === 0 ? t("empty.firstHostHelp") : !host ? t("empty.chooseHostHelp") : offline && !away ? t("empty.offlineHelp") : t("empty.noThreadHelp")}
                </EmptyDescription>
              </EmptyHeader>
              {hostList?.length === 0 && (
                <EmptyContent>
                  <Button onClick={() => setAddingHost(true)}>{t("host.add")}</Button>
                </EmptyContent>
              )}
              {/* On a host to work on: the threads last at work, to go back to one. */}
              {homePage && link && (
                <EmptyContent className="max-w-md">
                  <RecentThreads link={link} onOpen={openThread} />
                </EmptyContent>
              )}
            </Empty>
            </Welcome>
          </>
        )}
      </SidebarInset>
      <AddHostDialog open={addingHost} onClose={() => setAddingHost(false)} />
      <InviteDialog
        token={onInvite ? inviteAt?.token : undefined}
        hosts={hostList}
        onJoined={(h) => {
          void refreshHosts();
          home(h.id);
        }}
        onClose={() => home()}
      />
      <CommandPalette
        page={palette}
        onPage={setPalette}
        onClose={() => setPalette(undefined)}
        hosts={hostList ?? []}
        activeHost={host}
        link={link}
        canManageProjects={canOn(host, Permission.MANAGE_PROJECTS)}
        onSelectHost={selectHost}
        onOpenThread={openThread}
        onNewThread={openProject}
        onSettings={() => setSettings("profile")}
        terminals={!!link && canOn(host, Permission.THREADS_ANYWHERE) && modulesOn.has("terminal")}
        onOpenTerminal={(id) => navigate(`/hosts/${shownHost}/terminals/${id}`)}
      />
      <SettingsDialog
        section={settings}
        onSection={setSettings}
        onClose={() => setSettings(undefined)}
        user={user}
        onUser={onUser}
        host={host && link ? { link, hostName: host.name } : undefined}
      />
      <HostSettingsDialog
        // The listing's copy, so a change made in the dialog shows at once.
        host={hostList?.find((h) => h.id === hostSettings?.host.id) ?? hostSettings?.host}
        connection={hostSettings?.host.id === host?.id && link ? (link.direct ? "direct" : "relayed") : undefined}
        project={hostSettings?.project}
        canRename={hostSettings ? mayRemove(hostSettings.host) : false}
        onHostChanged={refreshHosts}
        section={hostSettings?.section}
        onSection={(section) => setHostSettings((now) => now && { ...now, section })}
        onClose={() => setHostSettings(undefined)}
        onModulesChanged={refreshModules}
        onOpenThread={(id) => {
          const h = hostSettings?.host.id;
          setHostSettings(undefined);
          if (h) navigate(`/hosts/${h}/threads/${id}`);
        }}
      />
      <ConfirmDialog
        open={removingHost !== undefined}
        onClose={() => setRemovingHost(undefined)}
        title={t("accounts.remove", { name: removingHost?.name ?? "" })}
        description={t("settings.hostRemoveHelp")}
        cancel={t("project.keep")}
        confirm={t("accounts.remove", { name: removingHost?.name ?? "" })}
        destructive
        onConfirm={() => removingHost && hostsApi.deleteHost({ id: removingHost.id }).then(refreshHosts, showError)}
      />
    </SidebarProvider>
    </PresenceContext.Provider>
  );
}
