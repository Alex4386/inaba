import { create } from "@bufbuild/protobuf";
import type { Client } from "@connectrpc/connect";
import { BotIcon, CheckIcon, ChevronDownIcon, ChevronUpIcon, ExternalLinkIcon, SearchIcon } from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";
import { RuntimeFields, Setting, words } from "@/components/providers-panel";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { type AcpAgent, ProviderConfigSchema, type ProviderConfigService } from "@/gen/inaba/v1/host_pb";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** A well-known agent: how it starts, falling back to command when the registry cannot be reached, and its own CLI. */
type Preset = { id: string; registry: string; name: string; command: string; terminal?: string; resume?: string };

// The agents most people want, first. Their commands come from the ACP registry when it answers, pinned to its version.
const featured: Preset[] = [
  { id: "claude", registry: "claude-acp", name: "Claude", command: "npx -y @agentclientprotocol/claude-agent-acp", terminal: "claude", resume: "claude --resume {session}" },
  { id: "codex", registry: "codex-acp", name: "Codex", command: "npx -y @agentclientprotocol/codex-acp", terminal: "codex", resume: "codex resume {session}" },
  { id: "gemini", registry: "gemini", name: "Gemini CLI", command: "npx -y @google/gemini-cli --acp", terminal: "gemini" },
  { id: "copilot", registry: "github-copilot-cli", name: "GitHub Copilot", command: "npx -y @github/copilot --acp", terminal: "copilot" },
  // Published in the registry only as a download; this runs the CLI already installed.
  { id: "opencode", registry: "opencode", name: "OpenCode", command: "opencode acp", terminal: "opencode" },
  { id: "grok", registry: "grok-build", name: "Grok", command: "npx -y @xai-official/grok agent stdio", terminal: "grok" },
  { id: "qwen", registry: "qwen-code", name: "Qwen Code", command: "npx -y @qwen-code/qwen-code --acp", terminal: "qwen" },
  { id: "kilo", registry: "kilo", name: "Kilo", command: "npx -y @kilocode/cli acp" },
];

/** A free id near the one wanted, so a second Claude becomes claude-2. */
export function freeId(want: string, taken: string[]): string {
  if (!taken.includes(want)) return want;
  let n = 2;
  while (taken.includes(`${want}-${n}`)) n++;
  return `${want}-${n}`;
}

/** The registry's icon for a provider: the agent its command runs, or the one its id names. */
export function registryIcon(c: { id: string; command: string[]; icon?: string }, registry: AcpAgent[]): string | undefined {
  if (c.icon) return c.icon;
  const pkg = (a: AcpAgent) => a.command[a.command[0] === "npx" ? 2 : 1]?.replace(/(.)@[^@]*$/, "$1").replace(/==.*$/, "");
  const ran = registry.find((a) => {
    const name = pkg(a);
    return name && c.command.some((w) => w === name || w.startsWith(name + "@") || w.startsWith(name + "=="));
  });
  const preset = featured.find((p) => p.id === c.id.replace(/-\d+$/, ""));
  return (ran ?? registry.find((a) => a.id === c.id || a.id === preset?.registry || a.id === `${c.id}-acp`))?.icon;
}

export function AgentIcon({ src }: { src?: string }) {
  const [broken, setBroken] = useState(false);
  // A picture of one's own keeps its colors; the registry's marks are drawn in black and invert in the dark.
  return src && !broken ? <img src={src} alt="" className={cn("size-5 shrink-0 rounded-sm object-cover", !src.startsWith("data:") && "dark:invert")} onError={() => setBroken(true)} /> : <BotIcon className="text-muted-foreground size-5 shrink-0" />;
}

const steps = ["providers.stepProvider", "providers.stepIdentity", "providers.stepConfig"] as const;

/** Adds a provider in three steps: which agent, from the well-known ones or the ACP registry; what to call it; how it runs. */
export function AddProviderDialog({
  open,
  onClose,
  client,
  registry,
  taken,
  onAdded,
}: {
  open: boolean;
  onClose: () => void;
  client: Client<typeof ProviderConfigService>;
  /** The ACP registry as the host gave it; empty while it loads or when it cannot be reached. */
  registry: AcpAgent[];
  taken: string[];
  onAdded: (id: string) => void;
}) {
  const { t } = useI18n();
  const [step, setStep] = useState(0);
  const [more, setMore] = useState(false);
  const [query, setQuery] = useState("");
  // What step one chose: a featured agent or a registry one, by key.
  const [chosen, setChosen] = useState<string>();
  const [draft, setDraft] = useState(() => create(ProviderConfigSchema, { enabled: true }));
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) return;
    setStep(0);
    setChosen(undefined);
    setMore(false);
    setQuery("");
  }, [open]);

  const inRegistry = (id: string) => registry.find((a) => a.id === id);
  // Starts the later steps from an agent: its id, name, commands and environment.
  const begin = (from?: { id: string; name: string; command: string[]; terminal?: string; resume?: string; env?: Record<string, string> }) => {
    setDraft(
      create(ProviderConfigSchema, {
        enabled: true,
        id: from ? freeId(from.id, taken) : "",
        displayName: from?.name ?? "",
        command: from?.command ?? [],
        terminal: words(from?.terminal ?? ""),
        terminalResume: words(from?.resume ?? ""),
        env: from?.env ?? {},
      }),
    );
    setStep(1);
  };
  const fromPreset = (p: Preset) => {
    const r = inRegistry(p.registry);
    begin({ id: p.id, name: p.name, command: r?.command.length ? r.command : words(p.command), terminal: p.terminal, resume: p.resume, env: r?.env });
  };
  const fromRegistry = (a: AcpAgent) => begin({ id: a.id.replace(/-acp$/, ""), name: a.name, command: a.command, env: a.env });
  const proceed = () => {
    const p = featured.find((x) => x.id === chosen);
    const a = registry.find((x) => x.id === chosen);
    if (p) fromPreset(p);
    else if (a) fromRegistry(a);
  };

  const save = (e: FormEvent) => {
    e.preventDefault();
    if (step === 1) return setStep(2);
    setSaving(true);
    client
      .saveProviderConfig({ config: draft })
      .then((c) => {
        onAdded(c.id);
        onClose();
      }, showError)
      .finally(() => setSaving(false));
  };

  const q = query.trim().toLowerCase();
  const listed = registry.filter((a) => !q || a.name.toLowerCase().includes(q) || a.description.toLowerCase().includes(q));

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[calc(100svh-2rem)] grid-rows-[auto_minmax(0,1fr)_auto] gap-0 p-0 sm:max-w-2xl">
        <DialogHeader className="gap-3 border-b p-5">
          <DialogTitle className="text-lg">{t("providers.add")}</DialogTitle>
          <DialogDescription>{t("providers.addHelp")}</DialogDescription>
          <ol className="bg-muted/50 grid grid-cols-3 gap-1 rounded-lg border p-1">
            {steps.map((label, i) => (
              <li key={label} className={cn("flex items-center gap-2 rounded-md px-2 py-1.5 text-sm", i === step ? "bg-background font-medium shadow-xs" : "text-muted-foreground")}>
                <span className={cn("flex size-5 items-center justify-center rounded-full border text-xs", i === step && "border-primary text-primary", i < step && "bg-primary text-primary-foreground border-primary")}>
                  {i < step ? <CheckIcon className="size-3" /> : i + 1}
                </span>
                {t(label)}
              </li>
            ))}
          </ol>
        </DialogHeader>

        {step === 0 ? (
          <>
            <div className="grid content-start gap-3 overflow-y-auto p-5">
              <span className="text-sm font-medium">{t("providers.stepProvider")}</span>
              <div className="grid gap-2 sm:grid-cols-2">
                {featured.map((p) => (
                  <button
                    key={p.id}
                    type="button"
                    aria-pressed={chosen === p.id}
                    onClick={() => setChosen(p.id)}
                    onDoubleClick={() => fromPreset(p)}
                    className={cn("hover:bg-muted/50 flex h-11 items-center gap-3 rounded-lg border px-3 text-left text-sm font-medium", chosen === p.id && "border-primary bg-primary/5 ring-primary/20 ring-2")}
                  >
                    <AgentIcon src={inRegistry(p.registry)?.icon} />
                    <span className="flex-1 truncate">{p.name}</span>
                    {chosen === p.id && <CheckIcon className="text-primary size-4" />}
                  </button>
                ))}
              </div>
              <button type="button" className="text-muted-foreground hover:text-foreground flex w-fit items-center gap-1 px-1 text-xs" onClick={() => setMore((m) => !m)}>
                {t(more ? "providers.showLess" : "providers.showMore")}
                {more ? <ChevronUpIcon className="size-3" /> : <ChevronDownIcon className="size-3" />}
              </button>
              {more && (
                <>
                  <div className="text-muted-foreground flex items-center gap-3 text-xs before:h-px before:flex-1 before:bg-border after:h-px after:flex-1 after:bg-border">{t("providers.fromRegistry")}</div>
                  <div className="relative">
                    <SearchIcon className="text-muted-foreground absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
                    <Input className="pl-8" value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t("providers.searchAgents")} />
                  </div>
                  {registry.length === 0 ? (
                    <p className="text-muted-foreground text-sm">{t("providers.registryUnavailable")}</p>
                  ) : (
                    <div className="divide-y">
                      {listed.map((a) => (
                        <div key={a.id} className="flex items-center gap-3 py-2.5">
                          <AgentIcon src={a.icon} />
                          <div className="grid min-w-0 flex-1 gap-0.5">
                            <span className="truncate text-sm font-medium">{a.name}</span>
                            <span className="text-muted-foreground truncate text-xs">{a.description}</span>
                          </div>
                          {a.website && (
                            <a className="text-muted-foreground hover:text-foreground" href={a.website} target="_blank" rel="noreferrer" aria-label={a.website}>
                              <ExternalLinkIcon className="size-4" />
                            </a>
                          )}
                          <Button size="sm" variant="outline" disabled={a.command.length === 0} title={a.command.length === 0 ? t("providers.downloadOnly") : undefined} onClick={() => fromRegistry(a)}>
                            {t("providers.addOne")}
                          </Button>
                        </div>
                      ))}
                    </div>
                  )}
                </>
              )}
            </div>
            <DialogFooter className="m-0 rounded-b-xl">
              <Button variant="ghost" onClick={onClose}>
                {t("providers.cancel")}
              </Button>
              <Button variant="outline" onClick={() => begin()}>
                {t("providers.manual")}
              </Button>
              <Button disabled={!chosen} onClick={proceed}>
                {t("device.continue")}
              </Button>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={save} className="contents">
            <div className="divide-y overflow-y-auto px-5 py-2">
              {step === 1 ? (
                <>
                  <Setting title={t("providers.id")} help={t("providers.idHelp")}>
                    <Input autoFocus required pattern="[a-z0-9][a-z0-9_\-]{0,39}" value={draft.id} onChange={(e) => setDraft({ ...draft, id: e.target.value })} placeholder="claude" />
                  </Setting>
                  <Setting title={t("providers.displayName")}>
                    <Input value={draft.displayName} onChange={(e) => setDraft({ ...draft, displayName: e.target.value })} placeholder={draft.id} />
                  </Setting>
                  {taken.includes(draft.id) && <p className="text-destructive py-2 text-sm">{t("providers.idTaken")}</p>}
                </>
              ) : (
                <RuntimeFields draft={draft} onDraft={setDraft} />
              )}
            </div>
            <DialogFooter className="m-0 rounded-b-xl">
              <Button type="button" variant="ghost" onClick={() => setStep(step - 1)}>
                {t("providers.back")}
              </Button>
              <Button type="submit" disabled={saving || (step === 1 && taken.includes(draft.id))}>
                {saving && <Spinner data-icon="inline-start" />}
                {t(step === 1 ? "device.continue" : "providers.add")}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
