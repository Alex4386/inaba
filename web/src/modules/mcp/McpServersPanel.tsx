import { create } from "@bufbuild/protobuf";
import { GlobeIcon, PencilIcon, PlusIcon, SquareTerminalIcon, Trash2Icon } from "lucide-react";
import { type FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { onHost } from "@/api";
import { ConfirmDialog } from "@/components/dialogs";
import { Picker } from "@/components/picker";
import { Card } from "@/components/section-dialog";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { type Project, ProjectSchema, ProjectService } from "@/gen/inaba/v1/host_pb";
import { type McpServer, McpServerSchema, McpService } from "@/gen/inaba/v1/mcp_pb";
import { showError } from "@/lib/errors";
import { cached, keep } from "@/lib/host-cache";
import { useI18n } from "@/lib/i18n";

/** "KEY=value" lines, the way env files read, to a map; and back. */
export const parsePairs = (text: string, sep: string) =>
  Object.fromEntries(
    text
      .split("\n")
      .map((line) => line.trim())
      .filter(Boolean)
      .map((line) => {
        const at = line.indexOf(sep);
        return at < 0 ? [line, ""] : [line.slice(0, at).trim(), line.slice(at + sep.length).trim()];
      }),
  );
export const showPairs = (pairs: Record<string, string>, sep: string) =>
  Object.entries(pairs)
    .map(([k, v]) => `${k}${sep}${v}`)
    .join("\n");

/** The MCP servers this host gives its threads' agents. */
export function McpServersPanel({ hostId }: { hostId: string }) {
  const { t } = useI18n();
  const link = useMemo(() => ({ mcp: onHost(hostId, McpService), projects: onHost(hostId, ProjectService) }), [hostId]);
  // Undefined until the host answers, so loading does not look like having none.
  const [servers, setServers] = useState<McpServer[]>();
  // The projects last read from this host, at once, for the scopes' names.
  const [projects, setProjects] = useState<Project[] | undefined>(() => cached(hostId, "projects", ProjectSchema));
  const [editing, setEditing] = useState<McpServer>();
  const [removing, setRemoving] = useState<McpServer>();

  const refresh = useCallback(() => {
    link.mcp.listMcpServers({}).then((r) => setServers(r.servers), (e) => (showError(e), setServers((s) => s ?? [])));
    link.projects.listProjects({}).then((r) => (keep(hostId, "projects", ProjectSchema, r.projects), setProjects(r.projects)), showError);
  }, [link, hostId]);
  useEffect(() => {
    refresh();
  }, [refresh]);

  const save = (m: McpServer) => (m.id ? link.mcp.updateMcpServer(m) : link.mcp.createMcpServer(m)).then(refresh);
  // Blank until the projects are known, rather than ids that turn into names.
  const scope = (m: McpServer) =>
    m.projectIds.length === 0 ? t("mcp.everyThread") : projects && m.projectIds.map((id) => projects.find((p) => p.id === id)?.name ?? id).join(", ");

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <p className="text-muted-foreground min-w-48 flex-1 text-sm">{t("mcp.description")}</p>
        <Button onClick={() => setEditing(create(McpServerSchema, { enabled: true }))}>
          <PlusIcon data-icon="inline-start" />
          {t("mcp.add")}
        </Button>
      </div>
      {!servers ? (
        <Spinner />
      ) : servers.length > 0 && (
        <Card>
          {servers.map((m) => (
            <div key={m.id} className="flex items-center gap-3 px-4 py-3">
              {m.url ? <GlobeIcon className="text-muted-foreground size-4 shrink-0" /> : <SquareTerminalIcon className="text-muted-foreground size-4 shrink-0" />}
              <div className="grid min-w-0 flex-1 gap-0.5">
                <span className="text-sm font-medium">{m.name}</span>
                <code className="text-muted-foreground truncate text-xs" title={m.url || [m.command, ...m.args].join(" ")}>
                  {m.url || [m.command, ...m.args].join(" ")}
                </code>
                <span className="text-muted-foreground text-xs">{scope(m)}</span>
              </div>
              <Switch
                checked={m.enabled}
                aria-label={t("mcp.enabled")}
                title={t("mcp.enabled")}
                onCheckedChange={(enabled) => {
                  setServers((now) => now?.map((x) => (x.id === m.id ? { ...x, enabled } : x)));
                  save({ ...m, enabled }).catch((e) => {
                    showError(e);
                    refresh();
                  });
                }}
              />
              <Button size="icon-sm" variant="ghost" aria-label={t("mcp.edit")} title={t("mcp.edit")} onClick={() => setEditing(m)}>
                <PencilIcon />
              </Button>
              <Button size="icon-sm" variant="ghost" aria-label={t("mcp.delete")} title={t("mcp.delete")} onClick={() => setRemoving(m)}>
                <Trash2Icon />
              </Button>
            </div>
          ))}
        </Card>
      )}
      <McpServerDialog server={editing} projects={projects ?? []} onClose={() => setEditing(undefined)} onSave={save} />
      <ConfirmDialog
        open={removing !== undefined}
        onClose={() => setRemoving(undefined)}
        title={t("mcp.deleteTitle", { name: removing?.name ?? "" })}
        description={t("mcp.deleteHelp")}
        cancel={t("project.keep")}
        confirm={t("mcp.delete")}
        destructive
        onConfirm={() => removing && link.mcp.deleteMcpServer({ id: removing.id }).then(refresh, showError)}
      />
    </div>
  );
}

function McpServerDialog({ server, projects, onClose, onSave }: { server?: McpServer; projects: Project[]; onClose: () => void; onSave: (m: McpServer) => Promise<unknown> }) {
  const { t } = useI18n();
  const [name, setName] = useState("");
  const [kind, setKind] = useState<"command" | "url">("command");
  const [command, setCommand] = useState("");
  const [args, setArgs] = useState("");
  const [env, setEnv] = useState("");
  const [url, setUrl] = useState("");
  const [headers, setHeaders] = useState("");
  const [projectIds, setProjectIds] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!server) return;
    setName(server.name);
    setKind(server.url ? "url" : "command");
    setCommand(server.command);
    setArgs(server.args.join("\n"));
    setEnv(showPairs(server.env, "="));
    setUrl(server.url);
    setHeaders(showPairs(server.headers, ": "));
    setProjectIds(server.projectIds);
  }, [server]);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!server || busy) return;
    const lines = (text: string) =>
      text
        .split("\n")
        .map((l) => l.trim())
        .filter(Boolean);
    const next = create(McpServerSchema, {
      id: server.id,
      name,
      enabled: server.enabled,
      projectIds,
      ...(kind === "command" ? { command, args: lines(args), env: parsePairs(env, "=") } : { url, headers: parsePairs(headers, ":") }),
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
    <Dialog open={server !== undefined} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{server?.id ? t("mcp.edit") : t("mcp.add")}</DialogTitle>
          <DialogDescription>{t("mcp.dialogHelp")}</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <Field>
            <FieldLabel htmlFor="mcp-name">{t("common.name")}</FieldLabel>
            <Input id="mcp-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="playwright" autoFocus required />
          </Field>
          <Field>
            <FieldLabel>{t("mcp.kind")}</FieldLabel>
            <Picker
              label={t("mcp.kind")}
              value={kind}
              onChange={setKind}
              options={[
                { value: "command", label: t("mcp.command") },
                { value: "url", label: t("mcp.url") },
              ]}
            />
          </Field>
          {kind === "command" ? (
            <>
              <Field>
                <FieldLabel htmlFor="mcp-command">{t("mcp.command")}</FieldLabel>
                <Input id="mcp-command" value={command} onChange={(e) => setCommand(e.target.value)} placeholder="npx" className="font-mono" required />
              </Field>
              <Field>
                <FieldLabel htmlFor="mcp-args">{t("mcp.args")}</FieldLabel>
                <Textarea id="mcp-args" value={args} onChange={(e) => setArgs(e.target.value)} placeholder={"@playwright/mcp@latest\n--headless"} rows={3} className="font-mono" />
                <FieldDescription>{t("mcp.argsHelp")}</FieldDescription>
              </Field>
              <Field>
                <FieldLabel htmlFor="mcp-env">{t("mcp.env")}</FieldLabel>
                <Textarea id="mcp-env" value={env} onChange={(e) => setEnv(e.target.value)} placeholder="API_KEY=…" rows={2} className="font-mono" />
              </Field>
            </>
          ) : (
            <>
              <Field>
                <FieldLabel htmlFor="mcp-url">{t("mcp.url")}</FieldLabel>
                <Input id="mcp-url" type="url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://" className="font-mono" required />
                <FieldDescription>{t("mcp.urlHelp")}</FieldDescription>
              </Field>
              <Field>
                <FieldLabel htmlFor="mcp-headers">{t("mcp.headers")}</FieldLabel>
                <Textarea id="mcp-headers" value={headers} onChange={(e) => setHeaders(e.target.value)} placeholder="Authorization: Bearer …" rows={2} className="font-mono" />
              </Field>
            </>
          )}
          {projects.length > 0 && (
            <Field>
              <FieldLabel>{t("mcp.projects")}</FieldLabel>
              <div className="grid gap-1.5">
                {projects.map((p) => (
                  <Label key={p.id} className="font-normal">
                    <Checkbox checked={projectIds.includes(p.id)} onCheckedChange={(on) => setProjectIds((now) => (on ? [...now, p.id] : now.filter((x) => x !== p.id)))} />
                    {p.name}
                  </Label>
                ))}
              </div>
              <FieldDescription>{t("mcp.projectsHelp")}</FieldDescription>
            </Field>
          )}
          <DialogFooter>
            <Button type="submit" disabled={busy}>
              {busy && <Spinner data-icon="inline-start" />}
              {server?.id ? t("common.save") : t("common.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
