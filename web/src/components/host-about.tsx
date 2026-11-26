import { timestampDate } from "@bufbuild/protobuf/wkt";
import { DownloadIcon, ImageIcon, RotateCwIcon, Trash2Icon } from "lucide-react";
import { type ChangeEvent, useEffect, useMemo, useRef, useState } from "react";
import { hosts as hostsApi, onHost } from "@/api";
import { ConfirmDialog } from "@/components/dialogs";
import { Picker } from "@/components/picker";
import { HostIcon, SystemIcon, hostIcons, platformName } from "@/components/host-icon";
import { timeAgo } from "@/components/project-tree";
import { Badge } from "@/components/reui/badge";
import { Card, Row } from "@/components/section-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { type SystemInfo, SystemService } from "@/gen/inaba/v1/host_pb";
import type { Host } from "@/gen/inaba/v1/server_pb";
import { showError } from "@/lib/errors";
import { type Translate, useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { toast } from "sonner";

const maxImage = 256 << 10;

/** Reads a picked file as a data: URL, shrunk to a 128px square so it fits what the server keeps. */
export function pictureOf(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => {
      const side = 128;
      const canvas = document.createElement("canvas");
      canvas.width = canvas.height = side;
      const scale = Math.max(side / img.width, side / img.height);
      const w = img.width * scale;
      const h = img.height * scale;
      canvas.getContext("2d")!.drawImage(img, (side - w) / 2, (side - h) / 2, w, h);
      URL.revokeObjectURL(img.src);
      resolve(canvas.toDataURL("image/png"));
    };
    img.onerror = () => reject(new Error("not an image"));
    img.src = URL.createObjectURL(file);
  });
}

const units = ["B", "KB", "MB", "GB", "TB"];
/** Bytes as people read them, such as 12.4 GB. */
export function bytes(n: bigint | number) {
  let v = Number(n);
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 || i === 0 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}

/** An uptime as people say it, such as 3d 4h. */
function uptime(seconds: bigint) {
  const s = Number(seconds);
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  return d ? `${d}d ${h}h` : h ? `${h}h ${m}m` : `${m}m`;
}

/** One figure about the machine: a label, its value, and when it is a share of something, a meter of how full. */
function Tile({ label, value, detail, share }: { label: string; value: string; detail?: string; share?: number }) {
  return (
    <div className="bg-card grid content-start gap-1.5 rounded-xl border p-3">
      <span className="text-muted-foreground text-xs">{label}</span>
      <span className="text-xl font-semibold tabular-nums">{value}</span>
      {share !== undefined && (
        <div className="bg-muted h-1 overflow-hidden rounded-full" role="meter" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(share)}>
          <div className="bg-primary h-full rounded-full transition-[width] duration-500" style={{ width: `${Math.min(100, Math.max(0, share))}%` }} />
        </div>
      )}
      {detail && <span className="text-muted-foreground truncate text-xs" title={detail}>{detail}</span>}
    </div>
  );
}

/** What the machine is and how busy it is, read again every few seconds while shown. */
function SystemTiles({ hostId }: { hostId: string }) {
  const { t } = useI18n();
  const client = useMemo(() => onHost(hostId, SystemService), [hostId]);
  const [info, setInfo] = useState<SystemInfo>();
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let live = true;
    const read = () =>
      client.getSystemInfo({}).then(
        (r) => live && (setInfo(r), setFailed(false)),
        () => live && setFailed(true),
      );
    void read();
    const timer = setInterval(read, 3000);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [client]);
  if (!info) return failed ? <p className="text-muted-foreground text-sm">{t("hostAbout.noSystem")}</p> : null;
  const share = (used: bigint, total: bigint) => (total ? (Number(used) / Number(total)) * 100 : 0);
  return (
    <div className="grid gap-2">
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-sm font-medium">{t("hostAbout.systemTitle")}</span>
        <span className="text-muted-foreground truncate text-xs">{[info.hostname, info.platform, info.kernel].filter(Boolean).join(" · ")}</span>
      </div>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
        <Tile label={t("hostAbout.cpu")} value={`${info.cpuPercent.toFixed(0)}%`} share={info.cpuPercent} detail={`${info.cpuCores} ${t("hostAbout.cores")}${info.cpuModel ? ` · ${info.cpuModel}` : ""}`} />
        <Tile label={t("hostAbout.memory")} value={`${share(info.memoryUsed, info.memoryTotal).toFixed(0)}%`} share={share(info.memoryUsed, info.memoryTotal)} detail={`${bytes(info.memoryUsed)} / ${bytes(info.memoryTotal)}`} />
        <Tile label={t("hostAbout.disk")} value={`${share(info.diskUsed, info.diskTotal).toFixed(0)}%`} share={share(info.diskUsed, info.diskTotal)} detail={`${bytes(info.diskUsed)} / ${bytes(info.diskTotal)}`} />
        <Tile label={t("hostAbout.load")} value={info.load1.toFixed(2)} detail={`${info.load5.toFixed(2)} · ${info.load15.toFixed(2)} ${t("hostAbout.loadHelp")}`} />
        <Tile label={t("hostAbout.uptime")} value={uptime(info.uptimeSeconds)} />
        <Tile label={t("hostAbout.threads")} value={`${info.threadsRunning} / ${info.threads}`} detail={t("hostAbout.threadsHelp")} />
      </div>
    </div>
  );
}

/** When a host was last here, said plainly; "never" for one that has not connected. */
export function lastSeen(host: Pick<Host, "lastSeen">, t: Translate, locale: string) {
  if (!host.lastSeen) return t("hostAbout.neverConnected");
  return t("hostAbout.lastSeen", { when: timeAgo(timestampDate(host.lastSeen), locale, t("common.justNow")) });
}

/** What an offline host said of itself when it was last connected: its machine, not how busy it was. */
function LastKnownSystem({ host }: { host: Host }) {
  const { t, locale } = useI18n();
  const info = host.system;
  return (
    <div className="grid gap-2">
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-sm font-medium">{t("hostAbout.lastKnown")}</span>
        <span className="text-muted-foreground truncate text-xs">{lastSeen(host, t, locale)}</span>
      </div>
      {info && (
        <>
          <span className="text-muted-foreground truncate text-xs">{[info.hostname, info.platform, info.kernel].filter(Boolean).join(" · ")}</span>
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
            <Tile label={t("hostAbout.cpu")} value={`${info.cpuCores} ${t("hostAbout.cores")}`} detail={info.cpuModel} />
            <Tile label={t("hostAbout.memory")} value={bytes(info.memoryTotal)} />
            <Tile label={t("hostAbout.disk")} value={bytes(info.diskTotal)} detail={`${bytes(info.diskUsed)} ${t("hostAbout.usedThen")}`} />
          </div>
        </>
      )}
    </div>
  );
}

type Channel = "release" | "pre-release" | "nightly";

/** The channel a build came from, as inaba update reads its version: nightly-…, a tag with a dash, or a release. */
export function channelOf(version: string | undefined): Channel {
  if (version?.startsWith("nightly-")) return "nightly";
  if (version?.startsWith("v") && version.includes("-")) return "pre-release";
  return "release";
}

/** A host's name, how it is shown, what it runs on, and how it connects. */
export function HostAbout({
  host,
  connection,
  canRename,
  canRestart,
  onChanged,
}: {
  host: Host;
  connection?: "direct" | "relayed";
  canRename: boolean;
  /** Whether the caller may restart it: its owner or an administrator (the server's grantsOn). */
  canRestart: boolean;
  onChanged: () => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState(host.name);
  const [restarting, setRestarting] = useState(false);
  const [channel, setChannel] = useState<Channel>(() => channelOf(host.system?.inabaVersion));
  const [upgrading, setUpgrading] = useState(false);
  const [busy, setBusy] = useState(false);
  const upgrade = () => {
    setBusy(true);
    onHost(host.id, SystemService)
      .upgradeHost({ channel, useGh: channel === "nightly" })
      .then(
        (r) =>
          void (r.updated
            ? toast.success(t("hostAbout.upgraded", { name: host.name, version: r.to }))
            : toast(t("hostAbout.upToDate", { name: host.name, channel: t(`hostAbout.channel.${channel}`) }))),
        showError,
      )
      .finally(() => setBusy(false));
  };
  const restart = () =>
    onHost(host.id, SystemService)
      .restartHost({})
      .then(() => void toast.success(t("hostAbout.restarted", { name: host.name })), showError);
  const file = useRef<HTMLInputElement>(null);
  const [saving, setSaving] = useState<"name" | "look">();
  const update = (change: { name?: string; icon?: string; image?: string }) => {
    setSaving(change.name ? "name" : "look");
    return hostsApi
      .updateHost({ id: host.id, ...change })
      .then(onChanged, showError)
      .finally(() => setSaving(undefined));
  };
  const pick = async (e: ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0];
    e.target.value = "";
    if (!f) return;
    try {
      const image = await pictureOf(f);
      if (image.length > maxImage) throw new Error(t("hostAbout.tooBig"));
      await update({ image });
    } catch (err) {
      showError(err);
    }
  };
  const choices: [string, string][] = [["", t("hostAbout.system")], ...Object.keys(hostIcons).map((k) => [k, t(`hostAbout.icon.${k}` as never)] as [string, string])];

  return (
    <div className="grid gap-4">
      <div className="flex items-center gap-4">
        <div className="bg-muted flex size-16 shrink-0 items-center justify-center rounded-xl">
          <HostIcon host={host} className="size-8" />
        </div>
        <div className="grid min-w-0 gap-1">
          <h3 className="truncate text-lg font-semibold">{host.name}</h3>
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge size="sm" variant={host.online ? "success-light" : "outline"}>
              {t(host.online ? "host.online" : "host.offline")}
            </Badge>
            <Badge size="sm" variant="outline">
              {t(host.shared ? "hostAbout.shared" : "host.personal")}
            </Badge>
            {host.os && <span className="text-muted-foreground text-xs">{platformName(host)}</span>}
          </div>
        </div>
      </div>
      {host.online ? <SystemTiles hostId={host.id} /> : <LastKnownSystem host={host} />}
      <Card>
        <Row title={t("hostAbout.name")} description={t("hostAbout.nameHelp")}>
          <form
            className="flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (name.trim() && name !== host.name) void update({ name: name.trim() });
            }}
          >
            <Input className="w-48" value={name} onChange={(e) => setName(e.target.value)} maxLength={64} required disabled={!canRename} />
            <Button type="submit" variant="outline" disabled={!canRename || !name.trim() || name === host.name || saving === "name"}>
              {saving === "name" && <Spinner data-icon="inline-start" />}
              {t("providers.save")}
            </Button>
          </form>
        </Row>
        <div className="grid gap-3 px-4 py-3">
          <div className="grid gap-0.5">
            <span className="text-sm font-medium">{t("hostAbout.look")}</span>
            <span className="text-muted-foreground text-xs">{t("hostAbout.lookHelp")}</span>
          </div>
          <div className="flex flex-wrap gap-2">
            {choices.map(([icon, label]) => {
              const Icon = hostIcons[icon];
              const on = !host.image && host.icon === icon;
              return (
                <button
                  key={icon || "system"}
                  type="button"
                  title={label}
                  aria-label={label}
                  aria-pressed={on}
                  disabled={!!saving}
                  onClick={() => void update({ icon, image: "" })}
                  className={cn("hover:bg-muted flex size-10 items-center justify-center rounded-lg border", on && "border-primary bg-primary/5 ring-primary/20 ring-2")}
                >
                  {Icon ? <Icon className="size-5" /> : <SystemIcon host={host} className="size-5" />}
                </button>
              );
            })}
            <input ref={file} type="file" accept="image/png,image/jpeg,image/webp" className="hidden" onChange={(e) => void pick(e)} />
            <Button type="button" variant="outline" className="h-10" disabled={!!saving} onClick={() => file.current?.click()}>
              {saving === "look" ? <Spinner data-icon="inline-start" /> : <ImageIcon data-icon="inline-start" />}
              {t(host.image ? "hostAbout.changePicture" : "hostAbout.picture")}
            </Button>
            {host.image && (
              <Button type="button" variant="ghost" className="h-10" disabled={!!saving} onClick={() => void update({ image: "" })}>
                <Trash2Icon data-icon="inline-start" />
                {t("hostAbout.removePicture")}
              </Button>
            )}
          </div>
        </div>
      </Card>
      <Card>
        <Row title={t("hostAbout.connection")} description={t(connection === "direct" ? "thread.directHelp" : connection === "relayed" ? "thread.relayedHelp" : host.online ? "hostAbout.onlineHelp" : "empty.offlineHelp")}>
          <span className="text-sm">{connection ? t(connection === "direct" ? "thread.direct" : "thread.relayed") : t(host.online ? "host.online" : "host.offline")}</span>
        </Row>
        <Row title={t("hostAbout.version")} description={t("hostAbout.versionHelp")}>
          <code className="text-muted-foreground text-xs break-all">{host.system?.inabaVersion || "—"}</code>
        </Row>
        <Row title={t("hostAbout.id")} description={t("hostAbout.idHelp")}>
          <code className="text-muted-foreground text-xs">{host.id}</code>
        </Row>
        {canRestart && (
          <Row title={t("hostAbout.upgrade")} description={t("hostAbout.upgradeHelp")}>
            <div className="flex gap-2">
              <Picker
                value={channel}
                onChange={setChannel}
                label={t("hostAbout.channel")}
                className="w-36"
                options={(["release", "pre-release", "nightly"] as const).map((c) => ({ value: c, label: t(`hostAbout.channel.${c}`) }))}
              />
              <Button type="button" variant="outline" disabled={!host.online || busy} onClick={() => setUpgrading(true)}>
                {busy ? <Spinner data-icon="inline-start" /> : <DownloadIcon data-icon="inline-start" />}
                {t(busy ? "hostAbout.upgrading" : "hostAbout.upgrade")}
              </Button>
            </div>
          </Row>
        )}
        {canRestart && (
          <Row title={t("hostAbout.restart")} description={t("hostAbout.restartHelp")}>
            <Button type="button" variant="outline" disabled={!host.online} onClick={() => setRestarting(true)}>
              <RotateCwIcon data-icon="inline-start" />
              {t("hostAbout.restart")}
            </Button>
          </Row>
        )}
      </Card>
      <ConfirmDialog
        open={upgrading}
        onClose={() => setUpgrading(false)}
        title={t("hostAbout.upgradeTitle", { name: host.name })}
        description={
          <>
            {t("hostAbout.upgradeConfirm", { channel: t(`hostAbout.channel.${channel}`) })}
            {channel === "nightly" && (
              <>
                <br />
                <br />
                {t("hostAbout.upgradeNightly")}
              </>
            )}
          </>
        }
        cancel={t("hostAbout.restartCancel")}
        confirm={t(channel === "nightly" ? "hostAbout.upgradeWithGh" : "hostAbout.upgrade")}
        onConfirm={upgrade}
      />
      <ConfirmDialog
        open={restarting}
        onClose={() => setRestarting(false)}
        title={t("hostAbout.restartTitle", { name: host.name })}
        description={t("hostAbout.restartConfirm")}
        cancel={t("hostAbout.restartCancel")}
        confirm={t("hostAbout.restart")}
        destructive
        onConfirm={restart}
      />
    </div>
  );
}
