import type { MessageInitShape } from "@bufbuild/protobuf";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { BellOffIcon, BellRingIcon, HashIcon, type LucideIcon, MessageCircleIcon, PlusIcon, SendIcon, Trash2Icon, WebhookIcon } from "lucide-react";
import { type FormEvent, useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { type HostLink, notifications } from "@/api";
import { DataTable } from "@/components/data-table";
import { ConfirmDialog, Secret } from "@/components/dialogs";
import { Picker } from "@/components/picker";
import { timeAgo } from "@/components/project-tree";
import { Badge } from "@/components/reui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { type NotificationChannel, type NotificationChannelSchema, type Snooze, SnoozeKind } from "@/gen/inaba/v1/server_pb";
import { showError } from "@/lib/errors";
import { type MessageKey, useI18n } from "@/lib/i18n";
import { reloadSnoozes } from "@/lib/snooze";

type Kind = "webhook" | "ntfy" | "discord" | "slack" | "telegram";

// Each kind's fields, named as in its config message; secret ones are typed as passwords.
const kinds: Record<Kind, { icon: LucideIcon; fields: { name: string; type: "url" | "text" | "password"; optional?: boolean; placeholder?: string }[] }> = {
  webhook: { icon: WebhookIcon, fields: [{ name: "url", type: "url", placeholder: "https://" }] },
  ntfy: {
    icon: BellRingIcon,
    fields: [
      { name: "serverUrl", type: "url", placeholder: "https://ntfy.sh" },
      { name: "topic", type: "text" },
      { name: "accessToken", type: "password", optional: true },
    ],
  },
  discord: { icon: MessageCircleIcon, fields: [{ name: "webhookUrl", type: "password", placeholder: "https://discord.com/api/webhooks/…" }] },
  slack: { icon: HashIcon, fields: [{ name: "webhookUrl", type: "password", placeholder: "https://hooks.slack.com/…" }] },
  telegram: {
    icon: SendIcon,
    fields: [
      { name: "botToken", type: "password" },
      { name: "chatId", type: "text" },
    ],
  },
};
const events = ["turn.ended", "turn.failed", "input.needed"] as const;

type ChannelInit = MessageInitShape<typeof NotificationChannelSchema>;

const kindOf = (c: NotificationChannel) => (c.config.case ?? c.kind) as Kind;

/** The account's other channels beside push: listed, added, tested, turned on and off. */
export function NotificationChannels() {
  const { t, locale } = useI18n();
  const [channels, setChannels] = useState<NotificationChannel[]>();
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<NotificationChannel>();
  // The channel being tried or switched, by id.
  const [busy, setBusy] = useState<string>();
  const [secret, setSecret] = useState<string>();
  const refresh = useCallback(() => notifications.listChannels({}).then((r) => setChannels(r.channels), showError), []);
  useEffect(() => {
    void refresh();
  }, [refresh]);
  const name = (c: NotificationChannel) => c.label || t(`channel.kind.${kindOf(c)}` as MessageKey);
  const test = (c: NotificationChannel) => {
    if (busy !== undefined) return;
    setBusy(c.id);
    notifications
      .testChannel({ id: c.id })
      .then((d) => (d.error ? toast.error(d.error) : toast.success(t("notify.delivered", { status: String(d.status) }))), showError)
      .finally(() => (setBusy(undefined), void refresh()));
  };
  const toggle = (c: NotificationChannel, enabled: boolean) => {
    setBusy(c.id);
    notifications
      .updateChannel({ id: c.id, label: c.label, events: c.events, automationsOnly: c.automationsOnly, enabled })
      .then(refresh, showError)
      .finally(() => setBusy(undefined));
  };

  return (
    <>
      <DataTable
        rows={channels}
        getRowKey={(c) => c.id}
        empty={{ icon: BellRingIcon, title: t("channel.none"), description: t("channel.help") }}
        actions={
          <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
            <PlusIcon data-icon="inline-start" />
            {t("channel.add")}
          </Button>
        }
        columns={[
          {
            header: t("channel.title"),
            sortBy: name,
            cell: (c) => {
              const Icon = kinds[kindOf(c)]?.icon ?? BellRingIcon;
              return (
                <span className="flex items-center gap-2">
                  <Icon className="text-muted-foreground size-4 shrink-0" aria-label={t(`channel.kind.${kindOf(c)}` as MessageKey)} />
                  <span className="truncate">{name(c)}</span>
                  {c.automationsOnly && (
                    <Badge size="sm" variant="outline">
                      {t("notify.automationsOnly")}
                    </Badge>
                  )}
                </span>
              );
            },
          },
          {
            header: t("channel.events"),
            cell: (c) => <span className="text-muted-foreground text-xs">{c.events.length ? c.events.map((e) => t(`notify.event.${e}` as MessageKey)).join(", ") : t("notify.allEvents")}</span>,
          },
          {
            header: t("channel.last"),
            cell: (c) => {
              if (busy === c.id) return <Spinner />;
              const d = c.lastDelivery;
              if (!d) return <span className="text-muted-foreground">{t("settings.deviceNever")}</span>;
              return (
                <span className={d.error ? "text-destructive" : "text-muted-foreground"} title={d.error || undefined}>
                  {d.error ? t("channel.failed") : t("channel.ok", { status: String(d.status) })} · {d.time ? timeAgo(timestampDate(d.time), locale, t("common.justNow")) : ""}
                </span>
              );
            },
          },
          {
            header: t("channel.enabled"),
            cell: (c) => <Switch checked={c.enabled} disabled={busy !== undefined} onCheckedChange={(on) => toggle(c, on)} aria-label={t("channel.enabled")} />,
          },
        ]}
        rowActions={[
          { label: t("notify.test"), icon: SendIcon, onSelect: test },
          { label: t("notify.delete"), icon: Trash2Icon, destructive: true, separatorBefore: true, onSelect: setRemoving },
        ]}
      />
      <ChannelDialog
        open={adding}
        onClose={() => setAdding(false)}
        onCreate={(c) =>
          notifications.createChannel(c).then((res) => {
            if (res.secret) setSecret(res.secret);
            void refresh();
          })
        }
      />
      <ConfirmDialog
        open={removing !== undefined}
        onClose={() => setRemoving(undefined)}
        title={t("notify.deleteTitle")}
        description={removing ? name(removing) : ""}
        cancel={t("project.keep")}
        confirm={t("notify.delete")}
        destructive
        onConfirm={() => removing && notifications.deleteChannel({ id: removing.id }).then(refresh, showError)}
      />
      <Dialog open={secret !== undefined} onOpenChange={(open) => !open && setSecret(undefined)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("auto.secretTitle")}</DialogTitle>
            <DialogDescription>{t("notify.secretHelp")}</DialogDescription>
          </DialogHeader>
          <Secret value={secret ?? ""} />
          <DialogFooter>
            <Button onClick={() => setSecret(undefined)}>{t("common.done")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

/** Adding a channel: its kind and where it delivers, then which events. */
function ChannelDialog({ open, onClose, onCreate }: { open: boolean; onClose: () => void; onCreate: (c: ChannelInit) => Promise<unknown> }) {
  const { t } = useI18n();
  const [kind, setKind] = useState<Kind>("webhook");
  const [fields, setFields] = useState<Record<string, string>>({});
  const [label, setLabel] = useState("");
  const [chosen, setChosen] = useState<string[]>([...events]);
  const [automationsOnly, setAutomationsOnly] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!open) return;
    setKind("webhook");
    setFields({});
    setLabel("");
    setChosen([...events]);
    setAutomationsOnly(false);
  }, [open]);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    try {
      const value = Object.fromEntries(kinds[kind].fields.map((f) => [f.name, (fields[f.name] ?? "").trim()]));
      // All three is the same as none named, which also takes events added later.
      await onCreate({ label, config: { case: kind, value } as ChannelInit["config"], events: chosen.length === events.length ? [] : chosen, automationsOnly });
      onClose();
    } catch (e) {
      showError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("channel.add")}</DialogTitle>
          <DialogDescription>{t(`channel.help.${kind}` as MessageKey)}</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <Field>
            <FieldLabel>{t("channel.kind")}</FieldLabel>
            <Picker
              label={t("channel.kind")}
              value={kind}
              onChange={(k) => (setKind(k), setFields({}))}
              options={(Object.keys(kinds) as Kind[]).map((k) => ({ value: k, label: t(`channel.kind.${k}` as MessageKey) }))}
            />
          </Field>
          {kinds[kind].fields.map((f) => (
            <Field key={`${kind}.${f.name}`}>
              <FieldLabel htmlFor={`channel-${f.name}`}>{t(`channel.field.${f.name}` as MessageKey)}</FieldLabel>
              <Input
                id={`channel-${f.name}`}
                type={f.type}
                placeholder={f.placeholder}
                autoComplete="off"
                value={fields[f.name] ?? ""}
                onChange={(e) => setFields((now) => ({ ...now, [f.name]: e.target.value }))}
                required={!f.optional}
              />
            </Field>
          ))}
          <Field>
            <FieldLabel htmlFor="channel-label">{t("channel.label")}</FieldLabel>
            <Input id="channel-label" maxLength={64} value={label} placeholder={t(`channel.kind.${kind}` as MessageKey)} onChange={(e) => setLabel(e.target.value)} />
          </Field>
          <div className="grid gap-2">
            {events.map((e) => (
              <Label key={e} className="font-normal">
                <Checkbox checked={chosen.includes(e)} onCheckedChange={(on) => setChosen((now) => (on ? [...now, e] : now.filter((x) => x !== e)))} />
                {t(`notify.event.${e}` as MessageKey)}
              </Label>
            ))}
          </div>
          <Label className="font-normal">
            <Switch checked={automationsOnly} onCheckedChange={setAutomationsOnly} />
            {t("notify.automationsOnly")}
          </Label>
          <DialogFooter>
            <Button type="submit" disabled={chosen.length === 0 || busy}>
              {busy && <Spinner data-icon="inline-start" />}
              {t("common.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** The account's snoozes on the connected host, named from that host, each with Unmute. */
export function MutedList({ link }: { link: HostLink }) {
  const { t, locale } = useI18n();
  const [snoozes, setSnoozes] = useState<Snooze[]>();
  const [names, setNames] = useState<Record<string, string>>({});
  const [clearing, setClearing] = useState<string>();
  const load = useCallback(
    () =>
      notifications.listSnoozes({ hostId: link.hostId }).then((r) => {
        const live = r.snoozes.filter((s) => !s.until || timestampDate(s.until).getTime() > Date.now());
        setSnoozes(live);
        return live;
      }, showError),
    [link],
  );
  useEffect(() => {
    void load().then((live) => {
      if (!live?.length) return;
      // Names are a nicety: anything the host cannot answer stays shown by its id.
      if (live.some((s) => s.kind === SnoozeKind.PROJECT))
        link.projects.listProjects({}).then((r) => setNames((now) => ({ ...now, ...Object.fromEntries(r.projects.map((p) => [p.id, p.name])) })), () => {});
      for (const s of live)
        if (s.kind === SnoozeKind.THREAD) link.threads.getThread({ threadId: s.targetId }).then((th) => th.title && setNames((now) => ({ ...now, [s.targetId]: th.title })), () => {});
    });
  }, [link, load]);
  const clear = (s: Snooze) => {
    setClearing(s.targetId);
    notifications
      .clearSnooze({ hostId: link.hostId, kind: s.kind, targetId: s.targetId })
      // The menus and the sidebar read the shared copy, so it is read again too.
      .then(() => (void reloadSnoozes(link.hostId), load()), showError)
      .finally(() => setClearing(undefined));
  };

  return (
    <DataTable
      rows={snoozes}
      getRowKey={(s) => `${s.kind}:${s.targetId}`}
      empty={{ icon: BellOffIcon, title: t("muted.none") }}
      columns={[
        {
          header: t("muted.what"),
          sortBy: (s) => names[s.targetId] ?? s.targetId,
          cell: (s) => (
            <span className="flex items-center gap-2">
              <Badge size="sm" variant="outline">
                {t(s.kind === SnoozeKind.PROJECT ? "muted.project" : "muted.thread")}
              </Badge>
              <span className="truncate">{names[s.targetId] ?? <code className="text-xs">{s.targetId}</code>}</span>
            </span>
          ),
        },
        {
          header: t("muted.until"),
          cell: (s) => <span className="text-muted-foreground">{s.until ? timestampDate(s.until).toLocaleString(locale) : t("muted.forever")}</span>,
        },
        {
          header: "",
          align: "right",
          cell: (s) => (
            <Button size="xs" variant="ghost" disabled={clearing !== undefined} onClick={() => clear(s)}>
              {clearing === s.targetId && <Spinner data-icon="inline-start" />}
              {t("muted.unmute")}
            </Button>
          ),
        },
      ]}
    />
  );
}
