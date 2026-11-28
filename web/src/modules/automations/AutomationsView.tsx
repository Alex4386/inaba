import { create } from "@bufbuild/protobuf";
import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import { CircleAlertIcon, CopyIcon, KeyRoundIcon, MoreHorizontalIcon, PencilIcon, PlayIcon, PlusIcon, Trash2Icon, WebhookIcon, ZapIcon } from "lucide-react";
import { type FormEvent, useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import type { HostLink } from "@/api";
import { type Automation, AutomationSchema } from "@/gen/inaba/v1/automation_pb";
import { type Project, ProjectSchema, type Provider, ProviderSchema, type Thread, ThreadMode, ThreadSchema } from "@/gen/inaba/v1/host_pb";
import { ConfirmDialog, Secret } from "@/components/dialogs";
import { OptionsBar } from "@/components/options-bar";
import { Picker } from "@/components/picker";
import { threadName } from "@/components/project-tree";
import { Badge } from "@/components/reui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { showError } from "@/lib/errors";
import { cached } from "@/lib/host-cache";
import { type MessageKey, useI18n } from "@/lib/i18n";
import { agentLanguageChoice, agentLanguageTag } from "@/lib/settings";
import { cn } from "@/lib/utils";

const weekdays: MessageKey[] = ["day.sun", "day.mon", "day.tue", "day.wed", "day.thu", "day.fri", "day.sat"];

/** A webhook's address, as callers outside reach it. */
const hookUrl = (hostId: string, id: string) => `${location.origin}/hooks/${hostId}/${id}`;

/** The automations of the host in use: what starts by itself, and how it went. */
export function AutomationsView({ link, onOpenThread }: { link: HostLink; onOpenThread: (threadId: string) => void }) {
  const { t, locale } = useI18n();
  const [list, setList] = useState<Automation[]>();
  // The threads last read from this host, at once, for the targets' names; the sidebar keeps them.
  const [threads, setThreads] = useState<Thread[]>(() => cached(link.hostId, "threads", ThreadSchema) ?? []);
  const [editing, setEditing] = useState<Automation>();
  const [removing, setRemoving] = useState<Automation>();
  // A secret just made, shown once, with what it goes with.
  const [secret, setSecret] = useState<{ help: string; fields: [string, string][] }>();
  // The automation being run, by id.
  const [running, setRunning] = useState<string>();
  const showHook = (id: string, value: string) =>
    setSecret({
      help: t("auto.secretHelp"),
      fields: [
        [t("auto.url"), hookUrl(link.hostId, id)],
        [t("auto.secret"), value],
      ],
    });

  const refresh = useCallback(() => {
    link.automations.listAutomations({}).then((r) => setList(r.automations), (e) => (showError(e), setList((l) => l ?? [])));
    link.threads.listThreads({}).then((r) => setThreads(r.threads), showError);
  }, [link]);
  useEffect(() => {
    refresh();
  }, [refresh]);

  const when = (a: Automation) => {
    const w = a.schedule?.when;
    switch (w?.case) {
      case "everyMinutes":
        return t("auto.everyN", { n: String(w.value) });
      case "daily":
        return w.value.weekdays.length ? t("auto.dailyOn", { at: w.value.at, days: w.value.weekdays.map((d) => t(weekdays[d])).join(", ") }) : t("auto.dailyAt", { at: w.value.at });
      case "onceAt":
        return t("auto.onceAt", { at: timestampDate(w.value).toLocaleString(locale) });
    }
    return t("auto.byHand");
  };
  const save = (a: Automation) => {
    if (!a.id) {
      return link.automations.createAutomation(a).then((res) => {
        if (res.webhookSecret) showHook(res.automation!.id, res.webhookSecret);
        refresh();
      });
    }
    return link.automations.updateAutomation(a).then(refresh);
  };
  const runNow = (a: Automation) => {
    setRunning(a.id);
    link.automations
      .runAutomation({ id: a.id })
      .then((run) => {
        if (run.error) toast.error(run.error);
        else toast.success(t("auto.ran"), { action: { label: t("auto.openThread"), onClick: () => onOpenThread(run.threadId) } });
        refresh();
      }, showError)
      .finally(() => setRunning(undefined));
  };
  const setEnabled = (a: Automation, enabled: boolean) => {
    setList((now) => now?.map((x) => (x.id === a.id ? { ...x, enabled } : x)));
    save({ ...a, enabled }).catch((e) => {
      showError(e);
      refresh();
    });
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex h-12 shrink-0 items-center gap-2 border-b px-3">
        <SidebarTrigger />
        <ZapIcon className="text-muted-foreground size-4" />
        <h1 className="text-sm font-medium">{t("auto.title")}</h1>
        <Button className="ml-auto" size="sm" onClick={() => setEditing(create(AutomationSchema, { enabled: true }))}>
          <PlusIcon data-icon="inline-start" />
          {t("auto.new")}
        </Button>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {!list ? (
          <Spinner className="text-muted-foreground mx-auto my-8" />
        ) : list.length === 0 ? (
          <Empty>
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <ZapIcon />
              </EmptyMedia>
              <EmptyTitle>{t("auto.emptyTitle")}</EmptyTitle>
              <EmptyDescription>{t("auto.emptyHelp")}</EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button onClick={() => setEditing(create(AutomationSchema, { enabled: true }))}>{t("auto.new")}</Button>
            </EmptyContent>
          </Empty>
        ) : (
          <ul className="mx-auto grid max-w-3xl gap-3 p-4">
            {list.map((a) => {
              const last = a.runs[0];
              const target = a.threadId ? threads.find((th) => th.id === a.threadId) : undefined;
              return (
                <li key={a.id} className="bg-card grid gap-2 rounded-xl border px-4 py-3">
                  <div className="flex items-center gap-2">
                    <span className="min-w-0 truncate font-medium">{a.name}</span>
                    {a.webhook && (
                      <Badge size="sm" variant="outline">
                        <WebhookIcon />
                        {t("auto.webhook")}
                      </Badge>
                    )}
                    <Switch
                      className="ml-auto"
                      checked={a.enabled}
                      aria-label={t("auto.enabled")}
                      title={t("auto.enabled")}
                      onCheckedChange={(enabled) => setEnabled(a, enabled)}
                    />
                    <Button size="icon-sm" variant="ghost" aria-label={t("auto.runNow")} title={t("auto.runNow")} disabled={running !== undefined} onClick={() => runNow(a)}>
                      {running === a.id ? <Spinner /> : <PlayIcon />}
                    </Button>
                    <DropdownMenu>
                      <DropdownMenuTrigger render={<Button size="icon-sm" variant="ghost" />}>
                        <MoreHorizontalIcon />
                        <span className="sr-only">{t("project.more", { name: a.name })}</span>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent className="w-auto whitespace-nowrap" align="end">
                        <DropdownMenuItem onClick={() => setEditing(a)}>
                          <PencilIcon />
                          {t("auto.edit")}
                        </DropdownMenuItem>
                        {a.webhook && (
                          <>
                            <DropdownMenuItem onClick={() => navigator.clipboard.writeText(hookUrl(link.hostId, a.id)).then(() => toast.success(t("common.copied")), showError)}>
                              <CopyIcon />
                              {t("auto.copyUrl")}
                            </DropdownMenuItem>
                            <DropdownMenuItem onClick={() => link.automations.rotateWebhookSecret({ id: a.id }).then((r) => showHook(a.id, r.webhookSecret), showError)}>
                              <KeyRoundIcon />
                              {t("auto.newSecret")}
                            </DropdownMenuItem>
                          </>
                        )}
                        <DropdownMenuSeparator />
                        <DropdownMenuItem variant="destructive" onClick={() => setRemoving(a)}>
                          <Trash2Icon />
                          {t("auto.delete")}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                  <p className="text-muted-foreground line-clamp-2 text-sm whitespace-pre-wrap">{a.prompt}</p>
                  <div className="text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs">
                    <span>{when(a)}</span>
                    <span>{a.threadId ? t("auto.toThread", { name: target ? threadName(target) : a.threadId }) : t("auto.toNew", { provider: a.newThread?.provider ?? "" })}</span>
                    {a.enabled && a.nextRun && <span>{t("auto.next", { at: timestampDate(a.nextRun).toLocaleString(locale) })}</span>}
                  </div>
                  {last && (
                    <details className="group/runs text-xs">
                      <summary className={cn("flex cursor-pointer list-none items-center gap-1.5 [&::-webkit-details-marker]:hidden", last.error ? "text-destructive" : "text-muted-foreground")}>
                        {last.error && <CircleAlertIcon className="size-3.5" />}
                        {t("auto.lastRun", { at: timestampDate(last.time!).toLocaleString(locale), trigger: t(`auto.trigger.${last.trigger}` as MessageKey) })}
                        <span className="underline-offset-2 group-open/runs:underline">· {t("auto.runs", { count: String(a.runs.length) })}</span>
                      </summary>
                      <ul className="mt-1.5 grid gap-1">
                        {a.runs.map((run, i) => (
                          <li key={i} className="flex items-center gap-2">
                            <span className="text-muted-foreground tabular-nums">{timestampDate(run.time!).toLocaleString(locale)}</span>
                            <span className="text-muted-foreground">{t(`auto.trigger.${run.trigger}` as MessageKey)}</span>
                            {run.error ? (
                              <span className="text-destructive min-w-0 truncate" title={run.error}>
                                {run.error}
                              </span>
                            ) : (
                              <Button size="xs" variant="link" className="h-auto p-0" onClick={() => onOpenThread(run.threadId)}>
                                {t("auto.openThread")}
                              </Button>
                            )}
                          </li>
                        ))}
                      </ul>
                    </details>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </div>
      <AutomationDialog link={link} automation={editing} threads={threads} onClose={() => setEditing(undefined)} onSave={save} />
      <ConfirmDialog
        open={removing !== undefined}
        onClose={() => setRemoving(undefined)}
        title={t("auto.deleteTitle", { name: removing?.name ?? "" })}
        description={t("auto.deleteHelp")}
        cancel={t("project.keep")}
        confirm={t("auto.delete")}
        destructive
        onConfirm={() => removing && link.automations.deleteAutomation({ id: removing.id }).then(refresh, showError)}
      />
      <Dialog open={secret !== undefined} onOpenChange={(open) => !open && setSecret(undefined)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("auto.secretTitle")}</DialogTitle>
            <DialogDescription>{secret?.help}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-3">
            {secret?.fields.map(([label, value]) => (
              <Field key={label}>
                <FieldLabel>{label}</FieldLabel>
                <Secret value={value} />
              </Field>
            ))}
          </div>
          <DialogFooter>
            <Button onClick={() => setSecret(undefined)}>{t("common.done")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

type Trigger = "hand" | "every" | "daily" | "once";

/** A date as the value of a datetime-local input, in local time. */
const localInput = (d: Date) => new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);

/** Making an automation, or changing one: what it sends, where, and when. */
function AutomationDialog({
  link,
  automation,
  threads,
  onClose,
  onSave,
}: {
  link: HostLink;
  automation?: Automation;
  threads: Thread[];
  onClose: () => void;
  onSave: (a: Automation) => Promise<unknown>;
}) {
  const { t, locale } = useI18n();
  const [name, setName] = useState("");
  const [prompt, setPrompt] = useState("");
  const [target, setTarget] = useState<"thread" | "new">("new");
  const [threadId, setThreadId] = useState("");
  const [provider, setProvider] = useState("");
  const [projectId, setProjectId] = useState("");
  const [worktree, setWorktree] = useState(false);
  // Option values chosen for each run's thread, by option id.
  const [chosen, setChosen] = useState<Record<string, string>>({});
  const [trigger, setTrigger] = useState<Trigger>("hand");
  const [every, setEvery] = useState("60");
  const [at, setAt] = useState("09:00");
  const [days, setDays] = useState<number[]>([]);
  const [once, setOnce] = useState("");
  const [webhook, setWebhook] = useState(false);
  // What was last read from this host, so the pickers fill at once and are read again on opening.
  const [providers, setProviders] = useState<Provider[]>(() => (cached(link.hostId, "providers", ProviderSchema) ?? []).filter((p) => p.structured));
  const [projects, setProjects] = useState<Project[]>(() => cached(link.hostId, "projects", ProjectSchema) ?? []);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!automation) return;
    link.threads.listProviders({}).then((r) => setProviders(r.providers.filter((p) => p.structured)), showError);
    link.projects.listProjects({}).then((r) => setProjects(r.projects), showError);
    const a = automation;
    setName(a.name);
    setPrompt(a.prompt);
    setTarget(a.id && a.threadId ? "thread" : "new");
    setThreadId(a.threadId);
    setProvider(a.newThread?.provider ?? "");
    setProjectId(a.newThread?.projectId ?? "");
    setWorktree(a.newThread?.worktree ?? false);
    setChosen({ ...a.newThread?.options });
    setWebhook(a.webhook);
    const w = a.schedule?.when;
    setTrigger(w?.case === "everyMinutes" ? "every" : w?.case === "daily" ? "daily" : w?.case === "onceAt" ? "once" : "hand");
    setEvery(w?.case === "everyMinutes" ? String(w.value) : "60");
    setAt(w?.case === "daily" ? w.value.at : "09:00");
    setDays(w?.case === "daily" ? w.value.weekdays : []);
    setOnce(localInput(w?.case === "onceAt" ? timestampDate(w.value) : new Date(Date.now() + 3600_000)));
  }, [automation, link]);
  // Defaults once the choices have loaded.
  useEffect(() => {
    if (!provider && providers[0]) setProvider(providers[0].id);
  }, [provider, providers]);
  useEffect(() => {
    if (!projectId && projects[0]) setProjectId(projects[0].id);
  }, [projectId, projects]);
  useEffect(() => {
    if (!threadId && threads[0]) setThreadId(threads[0].id);
  }, [threadId, threads]);

  // What this provider's agent offered last time it ran here; nothing before it has run.
  const offered = providers.find((p) => p.id === provider)?.options;

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!automation || busy) return;
    const schedule =
      trigger === "every"
        ? { when: { case: "everyMinutes" as const, value: Number(every) } }
        : trigger === "daily"
          ? { when: { case: "daily" as const, value: { at, weekdays: days } } }
          : trigger === "once"
            ? { when: { case: "onceAt" as const, value: timestampFromDate(new Date(once)) } }
            : undefined;
    const next = create(AutomationSchema, {
      id: automation.id,
      name,
      prompt,
      enabled: automation.enabled,
      webhook,
      schedule,
      threadId: target === "thread" ? threadId : "",
      // Its runs talk the language this person's new threads do, as set when it is saved.
      newThread: target === "new" ? { provider, projectId, worktree, options: chosen, language: agentLanguageTag(agentLanguageChoice(), locale) } : undefined,
    });
    setBusy(true);
    try {
      await onSave(next);
      onClose();
    } catch (e) {
      showError(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={automation !== undefined} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{automation?.id ? t("auto.edit") : t("auto.new")}</DialogTitle>
          <DialogDescription>{t("auto.dialogHelp")}</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <Field>
            <FieldLabel htmlFor="auto-name">{t("common.name")}</FieldLabel>
            <Input id="auto-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus required />
          </Field>
          <Field>
            <FieldLabel htmlFor="auto-prompt">{t("auto.prompt")}</FieldLabel>
            <Textarea id="auto-prompt" value={prompt} onChange={(e) => setPrompt(e.target.value)} rows={4} required />
            <FieldDescription>{t("auto.promptHelp")}</FieldDescription>
          </Field>
          <Field>
            <FieldLabel>{t("auto.sendTo")}</FieldLabel>
            <Picker
              label={t("auto.sendTo")}
              value={target}
              onChange={setTarget}
              options={[
                { value: "new", label: t("auto.newEachRun") },
                { value: "thread", label: t("auto.existing") },
              ]}
            />
          </Field>
          {target === "thread" ? (
            <Field>
              <FieldLabel>{t("auto.thread")}</FieldLabel>
              <Picker
                label={t("auto.thread")}
                value={threadId}
                onChange={setThreadId}
                options={threads.filter((th) => th.mode !== ThreadMode.TERMINAL).map((th) => ({ value: th.id, label: threadName(th) }))}
              />
            </Field>
          ) : (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field>
                <FieldLabel>{t("auto.provider")}</FieldLabel>
                <Picker
                  label={t("auto.provider")}
                  value={provider}
                  onChange={(next) => {
                    setProvider(next);
                    // Another agent has other options.
                    setChosen({});
                  }}
                  options={providers.map((p) => ({ value: p.id, label: p.id }))}
                />
              </Field>
              <Field>
                <FieldLabel>{t("auto.project")}</FieldLabel>
                <Picker label={t("auto.project")} value={projectId} onChange={setProjectId} options={projects.map((p) => ({ value: p.id, label: p.name }))} />
              </Field>
              {(offered?.length ?? 0) > 0 && (
                <Field className="sm:col-span-2">
                  <FieldLabel>{t("auto.options")}</FieldLabel>
                  <OptionsBar options={offered!.map((o) => ({ ...o, value: chosen[o.id] ?? o.value }))} onSet={(id, value) => setChosen((c) => ({ ...c, [id]: value }))} />
                  <FieldDescription>{t("auto.optionsHelp")}</FieldDescription>
                </Field>
              )}
              <Label className="font-normal sm:col-span-2">
                <Switch checked={worktree} onCheckedChange={setWorktree} />
                {t("auto.worktree")}
              </Label>
            </div>
          )}
          <Field>
            <FieldLabel>{t("auto.when")}</FieldLabel>
            <Picker
              label={t("auto.when")}
              value={trigger}
              onChange={setTrigger}
              options={[
                { value: "hand", label: t("auto.byHand") },
                { value: "every", label: t("auto.every") },
                { value: "daily", label: t("auto.daily") },
                { value: "once", label: t("auto.once") },
              ]}
            />
          </Field>
          {trigger === "every" && (
            <Field>
              <FieldLabel htmlFor="auto-every">{t("auto.minutes")}</FieldLabel>
              <Input id="auto-every" type="number" min={1} value={every} onChange={(e) => setEvery(e.target.value)} required />
            </Field>
          )}
          {trigger === "daily" && (
            <div className="grid gap-3">
              <Field>
                <FieldLabel htmlFor="auto-at">{t("auto.at")}</FieldLabel>
                <Input id="auto-at" type="time" value={at} onChange={(e) => setAt(e.target.value)} required />
                <FieldDescription>{t("auto.hostClock")}</FieldDescription>
              </Field>
              <div className="flex flex-wrap gap-1" role="group" aria-label={t("auto.days")}>
                {weekdays.map((day, i) => (
                  <Button
                    key={day}
                    type="button"
                    size="xs"
                    variant={days.includes(i) ? "secondary" : "outline"}
                    aria-pressed={days.includes(i)}
                    onClick={() => setDays((now) => (now.includes(i) ? now.filter((d) => d !== i) : [...now, i].sort()))}
                  >
                    {t(day)}
                  </Button>
                ))}
                <span className="text-muted-foreground self-center pl-1 text-xs">{days.length === 0 && t("auto.everyDay")}</span>
              </div>
            </div>
          )}
          {trigger === "once" && (
            <Field>
              <FieldLabel htmlFor="auto-once">{t("auto.at")}</FieldLabel>
              <Input id="auto-once" type="datetime-local" value={once} onChange={(e) => setOnce(e.target.value)} required />
            </Field>
          )}
          <Label className="font-normal">
            <Switch checked={webhook} onCheckedChange={setWebhook} />
            <span className="grid gap-0.5">
              {t("auto.webhookToggle")}
              <span className="text-muted-foreground text-xs">{t("auto.webhookHelp")}</span>
            </span>
          </Label>
          <DialogFooter>
            <Button type="submit" disabled={busy}>
              {busy && <Spinner data-icon="inline-start" />}
              {automation?.id ? t("common.save") : t("common.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

