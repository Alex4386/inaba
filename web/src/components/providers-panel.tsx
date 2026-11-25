import { create } from "@bufbuild/protobuf";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { PlusIcon, RefreshCwIcon, Trash2Icon, Undo2Icon, XIcon } from "lucide-react";
import { type ChangeEvent, type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { onHost } from "@/api";
import { AddProviderDialog, AgentIcon, registryIcon } from "@/components/add-provider-dialog";
import { ConfirmDialog } from "@/components/dialogs";
import { pictureOf } from "@/components/host-about";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { type AcpAgent, type ProviderConfig, ProviderConfigSchema, ProviderConfigService } from "@/gen/inaba/v1/host_pb";
import { errorText, showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

// shortcut: arguments split at spaces, so one holding a space cannot be written here; edit the config file for that.
export const words = (text: string) => text.split(/\s+/).filter(Boolean);

/** One setting in the detail: what it is on the left, its control on the right. */
export function Setting({ title, help, children }: { title: string; help?: string; children: ReactNode }) {
  return (
    <div className="flex flex-wrap items-start gap-x-4 gap-y-2 py-3">
      <div className="grid min-w-40 flex-1 gap-0.5">
        <span className="text-sm font-medium">{title}</span>
        {help && <span className="text-muted-foreground text-xs">{help}</span>}
      </div>
      <div className="w-full sm:w-64">{children}</div>
    </div>
  );
}

/** Environment variables as rows, each a name and a value, with a row added on demand. Rows without a name are not kept. */
function EnvRows({ value, onChange }: { value: Record<string, string>; onChange: (env: Record<string, string>) => void }) {
  const { t } = useI18n();
  const [rows, setRows] = useState<[string, string][]>(() => Object.entries(value));
  const update = (next: [string, string][]) => {
    setRows(next);
    onChange(Object.fromEntries(next.filter(([k]) => k.trim()).map(([k, v]) => [k.trim(), v])));
  };
  return (
    <div className="grid gap-2 py-3">
      <div className="flex flex-wrap items-start gap-x-4 gap-y-2">
        <div className="grid min-w-40 flex-1 gap-0.5">
          <span className="text-sm font-medium">{t("providers.variables")}</span>
          <span className="text-muted-foreground text-xs">{t("providers.variablesHelp")}</span>
        </div>
        <Button type="button" size="sm" variant="outline" onClick={() => update([...rows, ["", ""]])}>
          <PlusIcon data-icon="inline-start" />
          {t("providers.addVariable")}
        </Button>
      </div>
      {rows.map(([k, v], i) => (
        <div key={i} className="flex items-center gap-2">
          <Input className="w-2/5 font-mono text-xs" value={k} placeholder="NAME" aria-label={t("providers.variableName")} onChange={(e) => update(rows.map((r, j) => (j === i ? [e.target.value, r[1]] : r)))} />
          <Input className="flex-1 font-mono text-xs" value={v} placeholder="value" aria-label={t("providers.variableValue")} onChange={(e) => update(rows.map((r, j) => (j === i ? [r[0], e.target.value] : r)))} />
          <Button type="button" size="icon-sm" variant="ghost" aria-label={t("palette.close")} onClick={() => update(rows.filter((_, j) => j !== i))}>
            <XIcon />
          </Button>
        </div>
      ))}
    </div>
  );
}

/** How a provider runs: the commands that start it and its environment. */
export function RuntimeFields({ draft, onDraft }: { draft: ProviderConfig; onDraft: (c: ProviderConfig) => void }) {
  const { t } = useI18n();
  return (
    <>
      <h4 className="pt-5 pb-1 text-sm font-semibold">{t("providers.runtime")}</h4>
      <Setting title={t("providers.command")} help={t("providers.commandHelp")}>
        <Input required value={draft.command.join(" ")} onChange={(e) => onDraft({ ...draft, command: words(e.target.value) })} placeholder="npx -y @agentclientprotocol/claude-agent-acp" />
      </Setting>
      <Setting title={t("providers.terminal")} help={t("providers.terminalHelp")}>
        <Input value={draft.terminal.join(" ")} onChange={(e) => onDraft({ ...draft, terminal: words(e.target.value) })} placeholder="claude" />
      </Setting>
      <Setting title={t("providers.resume")} help={t("providers.resumeHelp", { session: "{session}" })}>
        <Input value={draft.terminalResume.join(" ")} onChange={(e) => onDraft({ ...draft, terminalResume: words(e.target.value) })} placeholder="claude --resume {session}" />
      </Setting>
      <h4 className="pt-5 pb-1 text-sm font-semibold">{t("providers.environment")}</h4>
      <EnvRows key={draft.id} value={draft.env} onChange={(env) => onDraft({ ...draft, env })} />
    </>
  );
}

/** "3m", the way a status line says how long ago. */
export function ago(date: Date, now = Date.now()) {
  const s = Math.max(0, Math.round((now - date.getTime()) / 1000));
  return s < 60 ? `${s}s` : s < 3600 ? `${Math.round(s / 60)}m` : `${Math.round(s / 3600)}h`;
}

/** The models the agent offered, each shown in the pickers or left out of them. */
function Models({ config, onHidden }: { config: ProviderConfig; onHidden: (hidden: string[]) => void }) {
  const { t } = useI18n();
  const [filter, setFilter] = useState("");
  const q = filter.trim().toLowerCase();
  const listed = config.models.filter((m) => !q || m.name.toLowerCase().includes(q) || m.value.toLowerCase().includes(q));
  const hidden = new Set(config.hiddenModels);
  if (config.models.length === 0) return <p className="text-muted-foreground py-3 text-sm">{t("providers.noModels")}</p>;
  return (
    <div className="grid gap-2 py-3">
      <div className="flex flex-wrap items-center gap-3">
        <Input className="h-8 max-w-56" value={filter} onChange={(e) => setFilter(e.target.value)} placeholder={t("providers.filterModels")} />
        <button type="button" className="text-muted-foreground hover:text-foreground text-sm" onClick={() => onHidden(hidden.size ? [] : config.models.map((m) => m.value))}>
          {t(hidden.size ? "providers.showAll" : "providers.hideAll")}
        </button>
        <span className="text-muted-foreground text-sm">{t("providers.modelCount", { count: String(config.models.length) })}</span>
      </div>
      <div className="grid">
        {listed.map((m) => (
          <div key={m.value} className="flex items-center gap-2 py-1.5">
            <span className={cn("truncate text-sm", hidden.has(m.value) && "text-muted-foreground")}>{m.name}</span>
            <code className="text-muted-foreground truncate text-xs">{m.value}</code>
            <Switch
              className="ml-auto"
              checked={!hidden.has(m.value)}
              aria-label={m.name}
              onCheckedChange={(on) => onHidden(on ? config.hiddenModels.filter((v) => v !== m.value) : [...config.hiddenModels, m.value])}
            />
          </div>
        ))}
      </div>
    </div>
  );
}

/** The agents a host offers, from its config file: a list to pick from and the picked one's settings. */
export function ProvidersPanel({ hostId }: { hostId: string }) {
  const { t } = useI18n();
  const client = useMemo(() => onHost(hostId, ProviderConfigService), [hostId]);
  const [configs, setConfigs] = useState<ProviderConfig[]>();
  const [path, setPath] = useState("");
  const [error, setError] = useState("");
  const [picked, setPicked] = useState("");
  const [adding, setAdding] = useState(false);
  const [registry, setRegistry] = useState<AcpAgent[]>([]);
  // The picked provider as being edited.
  const [draft, setDraft] = useState<ProviderConfig>();
  const [saving, setSaving] = useState(false);
  const [checking, setChecking] = useState(false);
  const [removing, setRemoving] = useState(false);

  const take = useCallback((r: { configs: ProviderConfig[]; path: string }) => {
    setConfigs(r.configs);
    setPath(r.path);
    return r.configs;
  }, []);
  const load = useCallback(
    () =>
      client.listProviderConfigs({}).then(take, (e) => {
        setError(errorText(e));
        return [] as ProviderConfig[];
      }),
    [client, take],
  );
  const check = useCallback(() => {
    setChecking(true);
    return client
      .checkProviders({})
      .then(take, showError)
      .finally(() => setChecking(false));
  }, [client, take]);
  useEffect(() => {
    void load().then((list) => {
      setPicked((now) => now || list[1]?.id || list[0]?.id || "");
      // A status older than ten minutes, or none, is worth a fresh look.
      const stale = list.some((c) => c.enabled && !c.builtin && (!c.status?.checkedAt || Date.now() - timestampDate(c.status.checkedAt).getTime() > 10 * 60_000));
      if (stale) void check();
    });
    // Icons only; the panel works without the registry.
    client.listAcpAgents({}).then(
      (r) => setRegistry(r.agents),
      () => {},
    );
  }, [load, check, client]);
  const saved = configs?.find((c) => c.id === picked);
  // Editing starts over from what is saved whenever another provider is picked.
  useEffect(() => {
    if (saved) setDraft(create(ProviderConfigSchema, saved));
  }, [saved]);

  const save = (c: ProviderConfig) => {
    setSaving(true);
    return client
      .saveProviderConfig({ config: c })
      .then(async (next) => {
        await load();
        setPicked(next.id);
      }, showError)
      .finally(() => setSaving(false));
  };
  const toggle = (c: ProviderConfig, enabled: boolean) => save(create(ProviderConfigSchema, { ...c, enabled }));
  const iconFile = useRef<HTMLInputElement>(null);
  const pickIcon = async (e: ChangeEvent<HTMLInputElement>, c: ProviderConfig) => {
    const f = e.target.files?.[0];
    e.target.value = "";
    if (!f) return;
    try {
      await save(create(ProviderConfigSchema, { ...c, icon: await pictureOf(f) }));
    } catch (err) {
      showError(err);
    }
  };

  if (error) return <p className="text-destructive text-sm">{error}</p>;
  if (!configs) return <Spinner />;
  // What the form edits; the status and models are the host's, and models save on their own.
  const editable = (c?: ProviderConfig) => c && JSON.stringify({ ...c, status: undefined, models: undefined, hiddenModels: undefined });
  const changed = draft && saved && editable(draft) !== editable(saved);
  const checkedAt = configs
    .flatMap((c) => (c.status?.checkedAt ? [timestampDate(c.status.checkedAt)] : []))
    .sort((a, b) => b.getTime() - a.getTime())[0];
  const statusLine = (c: ProviderConfig) =>
    c.builtin ? t("providers.builtin") : !c.enabled ? t("providers.disabled") : !c.status ? c.id : c.status.ok ? t("providers.ready", { name: c.status.name || c.id }) : t("providers.notFound", { error: c.status.error });

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center gap-3">
        <p className="text-muted-foreground min-w-48 flex-1 text-sm">{t("providers.description", { path })}</p>
        <Button size="sm" variant="ghost" disabled={checking} onClick={() => void check()}>
          {checking ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" />}
          {checking ? t("providers.checking") : checkedAt ? t("providers.checkedAgo", { ago: ago(checkedAt) }) : t("providers.check")}
        </Button>
      </div>
      <div className="bg-card grid overflow-hidden rounded-xl border md:grid-cols-[17rem_1fr]">
        <div className="divide-y border-b md:border-r md:border-b-0">
          {configs.map((c) => (
            <div key={c.id} role="button" tabIndex={0} onClick={() => setPicked(c.id)} onKeyDown={(e) => e.key === "Enter" && setPicked(c.id)} className={cn("hover:bg-muted/50 flex cursor-pointer items-start gap-3 px-4 py-3", c.id === picked && "bg-muted")}>
              <AgentIcon src={registryIcon(c, registry)} />
              <div className="grid min-w-0 flex-1 gap-0.5">
                <span className="flex items-baseline gap-1.5">
                  <span className={cn("truncate text-sm font-medium", !c.enabled && "text-muted-foreground")}>{c.displayName || c.id}</span>
                  {c.status?.version && <code className="text-muted-foreground shrink-0 text-xs">v{c.status.version}</code>}
                </span>
                <span className="text-muted-foreground line-clamp-2 text-xs">
                  {c.enabled && c.status && !c.status.ok && <span className="bg-destructive mr-1.5 inline-block size-1.5 rounded-full align-middle" />}
                  {statusLine(c)}
                </span>
              </div>
              <Switch checked={c.enabled} disabled={saving} aria-label={c.displayName || c.id} onClick={(e) => e.stopPropagation()} onCheckedChange={(on) => void toggle(c, on)} />
            </div>
          ))}
          <button type="button" className="text-muted-foreground hover:bg-muted/50 flex w-full items-center gap-2 px-4 py-3 text-sm" onClick={() => setAdding(true)}>
            <PlusIcon className="size-4" />
            {t("providers.add")}
          </button>
        </div>
        {draft && saved && (
          <form
            className="min-w-0 px-5 py-3"
            onSubmit={(e) => {
              e.preventDefault();
              void save(draft);
            }}
          >
            <div className="flex items-center gap-2 py-2">
              {/* The icon is a button: a picture of one's own replaces the registry's. */}
              <input ref={iconFile} type="file" accept="image/png,image/jpeg,image/webp,image/svg+xml" className="hidden" onChange={(e) => void pickIcon(e, saved)} />
              <button type="button" className="hover:bg-muted rounded-md p-1" title={t("providers.changeIcon")} aria-label={t("providers.changeIcon")} onClick={() => iconFile.current?.click()}>
                <AgentIcon src={registryIcon(saved, registry)} />
              </button>
              {saved.icon && (
                <Button type="button" size="xs" variant="ghost" onClick={() => void save(create(ProviderConfigSchema, { ...saved, icon: "" }))}>
                  {t("providers.resetIcon")}
                </Button>
              )}
              <h3 className="flex-1 truncate font-medium">{draft.displayName || draft.id}</h3>
              {saved.status?.version && <code className="text-muted-foreground text-xs">v{saved.status.version}</code>}
              {changed && (
                <Button type="button" size="icon-sm" variant="ghost" aria-label={t("providers.revert")} title={t("providers.revert")} onClick={() => setDraft(create(ProviderConfigSchema, saved))}>
                  <Undo2Icon />
                </Button>
              )}
              {!draft.builtin && (
                <Button type="button" size="icon-sm" variant="ghost" aria-label={t("accounts.remove", { name: draft.id })} title={t("accounts.remove", { name: draft.id })} onClick={() => setRemoving(true)}>
                  <Trash2Icon />
                </Button>
              )}
            </div>
            <div className="divide-y">
              {!saved.builtin && (
                <div className="grid gap-0.5 py-3">
                  <span className="text-sm font-medium">{t("providers.status")}</span>
                  <span className={cn("text-xs", saved.status && !saved.status.ok ? "text-destructive" : "text-muted-foreground")}>
                    {checking && !saved.status ? t("providers.checking") : saved.status ? statusLine(saved) : t("providers.notChecked")}
                  </span>
                </div>
              )}
              <Setting title={t("providers.displayName")}>
                <Input value={draft.displayName} onChange={(e) => setDraft({ ...draft, displayName: e.target.value })} placeholder={draft.id} />
              </Setting>
              {!draft.builtin && <RuntimeFields draft={draft} onDraft={setDraft} />}
            </div>
            <div className="flex justify-end py-3">
              <Button type="submit" disabled={saving || !changed}>
                {t("providers.save")}
              </Button>
            </div>
            {!saved.builtin && (
              <div className="border-t pt-4">
                <h4 className="text-sm font-semibold">{t("providers.models")}</h4>
                <p className="text-muted-foreground text-xs">{t("providers.modelsHelp")}</p>
                <Models config={saved} onHidden={(hiddenModels) => void save(create(ProviderConfigSchema, { ...saved, hiddenModels }))} />
              </div>
            )}
          </form>
        )}
      </div>
      <AddProviderDialog
        open={adding}
        onClose={() => setAdding(false)}
        client={client}
        registry={registry}
        taken={configs.map((c) => c.id)}
        onAdded={async (id) => {
          await load();
          setPicked(id);
          void check();
        }}
      />
      <ConfirmDialog
        open={removing}
        onClose={() => setRemoving(false)}
        title={t("accounts.remove", { name: draft?.id ?? "" })}
        description={t("providers.removeHelp")}
        cancel={t("project.keep")}
        confirm={t("accounts.remove", { name: draft?.id ?? "" })}
        destructive
        onConfirm={() =>
          draft &&
          client.deleteProviderConfig({ id: draft.id }).then(async () => {
            const list = await load();
            setPicked(list[0]?.id ?? "");
          }, showError)
        }
      />
    </div>
  );
}
