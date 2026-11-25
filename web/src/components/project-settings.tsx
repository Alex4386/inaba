import { create } from "@bufbuild/protobuf";
import { ChevronRightIcon, CopyIcon, ImageIcon } from "lucide-react";
import { type ChangeEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { onHost } from "@/api";
import { ConfirmDialog } from "@/components/dialogs";
import { pictureOf } from "@/components/host-about";
import { OptionsBar } from "@/components/options-bar";
import { Picker } from "@/components/picker";
import { ProjectIcon } from "@/components/project-tree";
import { Card, Row } from "@/components/section-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { HousekeepingService, NewThreadDefaultsSchema, type Project, ProjectService, type Provider, ThreadService } from "@/gen/inaba/v1/host_pb";
import { ProjectBrowser } from "@/modules/browser/BrowserSettingsPanel";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

/** One project's own settings: its name, how it is shown, and what its new threads start with. */
function ProjectDetail({ hostId, project, onChanged, onBack }: { hostId: string; project: Project; onChanged: () => void; onBack: () => void }) {
  const { t } = useI18n();
  const projects = useMemo(() => onHost(hostId, ProjectService), [hostId]);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [name, setName] = useState(project.name);
  const [removing, setRemoving] = useState(false);
  const file = useRef<HTMLInputElement>(null);
  useEffect(() => {
    onHost(hostId, ThreadService).listProviders({}).then((r) => setProviders(r.providers), showError);
  }, [hostId]);
  const defaults = project.newThread ?? create(NewThreadDefaultsSchema, {});
  const update = (change: { name?: string; icon?: string; pinned?: boolean; newThread?: typeof defaults }) => projects.updateProject({ projectId: project.id, ...change }).then(onChanged, showError);
  const setDefaults = (change: Partial<typeof defaults>) => update({ newThread: create(NewThreadDefaultsSchema, { ...defaults, ...change }) });
  const provider = providers.find((p) => p.id === defaults.provider);
  const pick = async (e: ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0];
    e.target.value = "";
    if (f) await pictureOf(f).then((icon) => update({ icon }), showError);
  };

  return (
    <div className="grid gap-4">
      <Card>
        <Row title={t("projectSettings.name")} description={t("projectSettings.nameHelp")}>
          <form
            className="flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (name.trim() && name !== project.name) void update({ name: name.trim() });
            }}
          >
            <Input className="w-48" value={name} onChange={(e) => setName(e.target.value)} maxLength={64} required />
            <Button type="submit" variant="outline" disabled={!name.trim() || name === project.name}>
              {t("providers.save")}
            </Button>
          </form>
        </Row>
        <Row title={t("projectSettings.icon")} description={t(project.icon ? "projectSettings.iconPicture" : "projectSettings.iconAuto")}>
          <div className="flex items-center gap-2">
            <ProjectIcon project={project} className="size-8 text-sm" />
            <input ref={file} type="file" accept="image/png,image/jpeg,image/webp" className="hidden" onChange={(e) => void pick(e)} />
            <Button variant="outline" onClick={() => file.current?.click()}>
              <ImageIcon data-icon="inline-start" />
              {t("projectSettings.chooseFile")}
            </Button>
            {project.icon && (
              <Button variant="ghost" onClick={() => void update({ icon: "" })}>
                {t("projectSettings.automatic")}
              </Button>
            )}
          </div>
        </Row>
        <Row title={t("projectSettings.pin")} description={t("projectSettings.pinHelp")}>
          <Switch checked={project.pinned} aria-label={t("projectSettings.pin")} onCheckedChange={(pinned) => void update({ pinned })} />
        </Row>
        <Row title={t("projectSettings.folder")} description={project.cwd}>
          <Button variant="outline" onClick={() => navigator.clipboard.writeText(project.cwd).then(() => toast.success(t("common.copied")), showError)}>
            <CopyIcon data-icon="inline-start" />
            {t("projectSettings.copyPath")}
          </Button>
        </Row>
      </Card>
      <h3 className="px-1 text-sm font-semibold">{t("projectSettings.newThreads")}</h3>
      <Card>
        <Row title={t("projectSettings.provider")} description={t("projectSettings.providerHelp")}>
          <Picker
            label={t("projectSettings.provider")}
            value={defaults.provider || "_"}
            onChange={(id) => void setDefaults({ provider: id === "_" ? "" : id, options: {} })}
            options={[{ value: "_", label: t("projectSettings.anyProvider") }, ...providers.map((p) => ({ value: p.id, label: p.displayName || p.id }))]}
          />
        </Row>
        {provider && provider.options.length > 0 && (
          <Row title={t("projectSettings.model")} description={t("projectSettings.modelHelp")}>
            {/* What the agent offered the last time it ran, starting from the project's choices. */}
            <OptionsBar
              options={provider.options.map((o) => ({ ...o, value: defaults.options[o.id] ?? o.value }))}
              onSet={(id, value) => void setDefaults({ options: { ...defaults.options, [id]: value } })}
              providers={[{ id: provider.id, name: provider.displayName || provider.id, icon: provider.icon }]}
              provider={provider.id}
              field
            />
          </Row>
        )}
        <Row title={t("projectSettings.workspace")} description={t("projectSettings.workspaceHelp")}>
          <Picker
            label={t("projectSettings.workspace")}
            value={defaults.worktree ? "worktree" : "folder"}
            onChange={(v) => void setDefaults({ worktree: v === "worktree" })}
            options={[
              { value: "folder", label: t("projectSettings.inFolder") },
              { value: "worktree", label: t("git.worktree") },
            ]}
          />
        </Row>
      </Card>
      <ProjectBrowser hostId={hostId} projectId={project.id} />
      <Card>
        <Row title={t("project.remove")} description={t("project.removeDescription")}>
          <Button variant="destructive" onClick={() => setRemoving(true)}>
            {t("project.remove")}
          </Button>
        </Row>
      </Card>
      <ConfirmDialog
        open={removing}
        onClose={() => setRemoving(false)}
        title={t("project.removeTitle", { name: project.name })}
        description={t("project.removeDescription")}
        cancel={t("project.keep")}
        confirm={t("project.remove")}
        destructive
        onConfirm={() => projects.deleteProject({ projectId: project.id }).then(() => (onChanged(), onBack()), showError)}
      />
    </div>
  );
}

// How long a thread may sit quiet before the host archives it; 0 never.
const archiveDays = [0, 1, 3, 7, 14, 30, 90];

/** The host's policy for putting quiet threads away by itself. */
function ArchivePolicy({ hostId }: { hostId: string }) {
  const { t } = useI18n();
  const client = useMemo(() => onHost(hostId, HousekeepingService), [hostId]);
  const [days, setDays] = useState<number>();
  useEffect(() => {
    client.getArchivePolicy({}).then((p) => setDays(p.afterDays), showError);
  }, [client]);
  if (days === undefined) return null;
  return (
    <Card>
      <Row title={t("archivePolicy.title")} description={t("archivePolicy.help")}>
        <Picker
          label={t("archivePolicy.title")}
          value={String(days)}
          onChange={(v) => client.setArchivePolicy({ afterDays: Number(v) }).then((p) => setDays(p.afterDays), showError)}
          options={[...new Set([...archiveDays, days])].sort((a, b) => a - b).map((d) => ({ value: String(d), label: d === 0 ? t("archivePolicy.never") : d === 1 ? t("archivePolicy.afterOne") : t("archivePolicy.after", { count: String(d) }) }))}
        />
      </Row>
    </Card>
  );
}

/** A host's projects, each opening its own settings. */
export function ProjectSettings({ hostId, open, onOpen, onName }: { hostId: string; /** The project whose settings show, by id. */ open?: string; onOpen: (id: string | undefined) => void; /** Tells the dialog the open project's name, for its breadcrumb. */ onName: (name: string | undefined) => void }) {
  const { t } = useI18n();
  const client = useMemo(() => onHost(hostId, ProjectService), [hostId]);
  const [projects, setProjects] = useState<Project[]>();
  const setOpen = onOpen;
  const load = useCallback(() => client.listProjects({}).then((r) => setProjects(r.projects), showError), [client]);
  useEffect(() => {
    void load();
  }, [load]);
  const project = projects?.find((p) => p.id === open);
  useEffect(() => onName(project?.name), [project?.name, onName]);
  if (!projects) return <Spinner />;
  if (project) return <ProjectDetail key={project.id} hostId={hostId} project={project} onChanged={() => void load()} onBack={() => setOpen(undefined)} />;
  return (
    <div className="grid gap-3">
      <ArchivePolicy hostId={hostId} />
      <p className="text-muted-foreground text-sm">{t("projectSettings.description")}</p>
      <Card>
        {projects.length === 0 && <p className="text-muted-foreground px-4 py-3 text-sm">{t("projectSettings.none")}</p>}
        {projects.map((p) => (
          <button key={p.id} type="button" className="hover:bg-muted/50 flex w-full items-center gap-3 px-4 py-3 text-left" onClick={() => setOpen(p.id)}>
            <ProjectIcon project={p} className="size-7 text-xs" />
            <span className="grid min-w-0 flex-1 gap-0.5">
              <span className="truncate text-sm font-medium">{p.name}</span>
              <span className="text-muted-foreground truncate text-xs">{p.cwd}</span>
            </span>
            <ChevronRightIcon className="text-muted-foreground size-4" />
          </button>
        ))}
      </Card>
    </div>
  );
}
