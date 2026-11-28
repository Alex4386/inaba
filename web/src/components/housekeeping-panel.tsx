import { FolderGitIcon, RefreshCwIcon } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { admin, housekeepingOf } from "@/api";
import { ConfirmDialog } from "@/components/dialogs";
import { Picker } from "@/components/picker";
import { Badge } from "@/components/reui/badge";
import { Card, Row } from "@/components/section-dialog";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { Spinner } from "@/components/ui/spinner";
import type { Housekeeping } from "@/gen/inaba/v1/host_pb";
import type { Host, ServerHousekeeping } from "@/gen/inaba/v1/server_pb";
import { errorText, showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { shortPath } from "@/lib/utils";

/** A size the way people read one. */
function bytes(n: bigint) {
  const units = ["B", "KiB", "MiB", "GiB"];
  let size = Number(n);
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit++;
  }
  return `${size.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

/** For administrators: what the server and each host keep on disk, and clearing what is no longer needed. */
export function HousekeepingPanel({ hosts }: { hosts: Host[] }) {
  const { t } = useI18n();
  return (
    <div className="grid gap-6">
      <p className="text-muted-foreground text-sm">{t("housekeeping.description")}</p>
      <ServerSection />
      <HostSection hosts={hosts} />
    </div>
  );
}

function ServerSection() {
  const { t } = useI18n();
  const [state, setState] = useState<ServerHousekeeping>();
  const [busy, setBusy] = useState<"compact" | "tokens">();
  useEffect(() => {
    admin.getServerHousekeeping({}).then(setState, showError);
  }, []);
  const clean = (expiredTokens: boolean, compactDatabase: boolean) => {
    setBusy(compactDatabase ? "compact" : "tokens");
    admin
      .cleanUpServer({ expiredTokens, compactDatabase })
      .then((next) => (setState(next), toast.success(t("housekeeping.done"))), showError)
      .finally(() => setBusy(undefined));
  };
  return (
    <section className="grid gap-2">
      <h3 className="text-sm font-medium">{t("housekeeping.server")}</h3>
      <Card>
        <Row title={t("housekeeping.database")} description={state ? bytes(state.databaseBytes) : "…"}>
          <Button size="sm" variant="outline" disabled={!!busy || !state} onClick={() => clean(false, true)}>
            {busy === "compact" && <Spinner data-icon="inline-start" />}
            {t("housekeeping.compact")}
          </Button>
        </Row>
        <Row
          title={t("housekeeping.expired")}
          description={state ? t("housekeeping.expiredHelp", { expired: String(state.expiredTokens), active: String(state.activeSignIns) }) : "…"}
        >
          <Button size="sm" variant="outline" disabled={!!busy || !state?.expiredTokens} onClick={() => clean(true, false)}>
            {busy === "tokens" && <Spinner data-icon="inline-start" />}
            {t("housekeeping.deleteExpired")}
          </Button>
        </Row>
      </Card>
    </section>
  );
}

function HostSection({ hosts }: { hosts: Host[] }) {
  const { t } = useI18n();
  const online = hosts.filter((h) => h.online);
  // The one picked, or the first online once the list is in: it can arrive after this first draws.
  const [picked, setHostId] = useState("");
  const hostId = picked || online[0]?.id || "";
  const client = useMemo(() => (hostId ? housekeepingOf(hostId) : undefined), [hostId]);
  const [state, setState] = useState<Housekeeping>();
  const [error, setError] = useState("");
  // What is being cleaned; the confirm dialogs show their own spinner.
  const [busy, setBusy] = useState<"compact" | "other">();
  const [asking, setAsking] = useState<"worktrees" | "snapshots">();
  const [discard, setDiscard] = useState(false);

  const load = useCallback(() => {
    setError("");
    client?.getHousekeeping({}).then(setState, (e) => (setState(undefined), setError(errorText(e))));
  }, [client]);
  useEffect(() => {
    setState(undefined);
    load();
  }, [load]);

  const clean = (req: { leftoverWorktrees?: boolean; discardChanges?: boolean; leftoverSnapshots?: boolean; compactDatabase?: boolean }) => {
    if (!client) return;
    setBusy(req.compactDatabase ? "compact" : "other");
    return client
      .cleanUp(req)
      .then((res) => {
        setState(res.housekeeping);
        // What was asked to go but stayed is worth reading, one line each.
        if (res.kept.length > 0) toast.warning(t("housekeeping.kept"), { description: res.kept.join("\n"), duration: 15000 });
        else toast.success(t("housekeeping.done"));
      }, showError)
      .finally(() => setBusy(undefined));
  };

  const leftover = state?.worktrees.filter((w) => !w.threadId) ?? [];
  const dirtyLeftover = leftover.filter((w) => w.dirty).length;
  const leftoverSnapshots = state?.snapshots.reduce((n, s) => n + s.leftoverRefs, 0) ?? 0;

  return (
    <section className="grid gap-2">
      <div className="flex items-center gap-2">
        <h3 className="text-sm font-medium">{t("housekeeping.host")}</h3>
        {online.length > 0 && <Picker size="sm" label={t("housekeeping.host")} value={hostId} onChange={setHostId} options={online.map((h) => ({ value: h.id, label: h.name }))} />}
        <Button className="ml-auto" size="icon-sm" variant="ghost" aria-label={t("git.refresh")} title={t("git.refresh")} disabled={!client} onClick={load}>
          <RefreshCwIcon />
        </Button>
      </div>
      {online.length === 0 ? (
        <p className="text-muted-foreground text-sm">{t("housekeeping.noHosts")}</p>
      ) : error ? (
        <p className="text-destructive text-sm">{error}</p>
      ) : !state ? (
        <Spinner />
      ) : (
        <>
          <Card>
            <Row title={t("housekeeping.database")} description={bytes(state.databaseBytes)}>
              <Button size="sm" variant="outline" disabled={!!busy} onClick={() => void clean({ compactDatabase: true })}>
                {busy === "compact" && <Spinner data-icon="inline-start" />}
                {t("housekeeping.compact")}
              </Button>
            </Row>
            <Row title={t("housekeeping.worktrees")} description={t("housekeeping.worktreesHelp", { count: String(state.worktrees.length), leftover: String(leftover.length) })}>
              <Button size="sm" variant="outline" disabled={!!busy || leftover.length === 0} onClick={() => (setDiscard(false), setAsking("worktrees"))}>
                {t("housekeeping.removeWorktrees")}
              </Button>
            </Row>
            {state.worktrees.map((w) => (
              <div key={w.path} className="flex flex-wrap items-center gap-2 px-4 py-2 text-sm">
                <FolderGitIcon className="text-muted-foreground size-4 shrink-0" />
                <span className="min-w-0 truncate" title={w.path}>
                  {w.threadTitle || shortPath(w.path)}
                </span>
                <code className="text-muted-foreground text-xs">{w.branch}</code>
                <span className="ml-auto flex gap-1">
                  {!w.threadId && (
                    <Badge size="sm" variant="warning-light">
                      {t("housekeeping.leftover")}
                    </Badge>
                  )}
                  {w.dirty && (
                    <Badge size="sm" variant="outline">
                      {t("housekeeping.uncommitted")}
                    </Badge>
                  )}
                </span>
              </div>
            ))}
            <Row title={t("housekeeping.snapshots")} description={t("housekeeping.snapshotsHelp", { count: String(state.snapshots.reduce((n, s) => n + s.refs, 0)), leftover: String(leftoverSnapshots) })}>
              <Button size="sm" variant="outline" disabled={!!busy || leftoverSnapshots === 0} onClick={() => setAsking("snapshots")}>
                {t("housekeeping.removeSnapshots")}
              </Button>
            </Row>
          </Card>
          <ConfirmDialog
            open={asking === "worktrees"}
            onClose={() => setAsking(undefined)}
            title={t("housekeeping.removeWorktreesTitle", { count: String(leftover.length) })}
            description={
              <span className="grid gap-3">
                {t("housekeeping.removeWorktreesHelp")}
                {dirtyLeftover > 0 && (
                  <Label className="text-foreground font-normal">
                    <Checkbox checked={discard} onCheckedChange={setDiscard} />
                    {t("housekeeping.discard", { count: String(dirtyLeftover) })}
                  </Label>
                )}
              </span>
            }
            cancel={t("project.keep")}
            confirm={t("housekeeping.removeWorktrees")}
            destructive
            onConfirm={() => clean({ leftoverWorktrees: true, discardChanges: discard })}
          />
          <ConfirmDialog
            open={asking === "snapshots"}
            onClose={() => setAsking(undefined)}
            title={t("housekeeping.removeSnapshotsTitle", { count: String(leftoverSnapshots) })}
            description={t("housekeeping.removeSnapshotsHelp")}
            cancel={t("project.keep")}
            confirm={t("housekeeping.removeSnapshots")}
            destructive
            onConfirm={() => clean({ leftoverSnapshots: true })}
          />
        </>
      )}
    </section>
  );
}
