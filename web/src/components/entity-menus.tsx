import {
  ArchiveIcon,
  ArchiveRestoreIcon,
  BellIcon,
  BellOffIcon,
  CircleDotIcon,
  CopyIcon,
  GitForkIcon,
  ListIcon,
  PencilIcon,
  PinIcon,
  PinOffIcon,
  PlusIcon,
  SettingsIcon,
  Trash2Icon,
} from "lucide-react";
import { type ReactNode, useState } from "react";
import { toast } from "sonner";
import type { HostLink } from "@/api";
import { ConfirmDialog, RenameDialog } from "@/components/dialogs";
import { threadName, threadWorking } from "@/components/project-tree";
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
} from "@/components/ui/dropdown-menu";
import type { Project, Thread } from "@/gen/inaba/v1/host_pb";
import { SnoozeKind } from "@/gen/inaba/v1/server_pb";
import { errorText, showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { useSeen } from "@/lib/seen";
import {
  type Muted,
  type SnoozePreset,
  snoozePresets,
  useSnoozes,
} from "@/lib/snooze";

// What a thread's or a project's menu offers, the same in the sidebar, the thread's header and the listings.
// Each place hands in what is its own: how to go somewhere, what it shows first, what only it offers.

const presetKey = {
  hour: "snooze.hour",
  tomorrow: "snooze.tomorrow",
  week: "snooze.week",
  forever: "snooze.forever",
} as const;
// A weekday alone reads as this week, so a wake time past a day also says the date.
export const wakeTime = (until: Date, locale: string) =>
  until.toLocaleString(locale, {
    ...(until.getTime() - Date.now() > 86_400_000 && {
      month: "short",
      day: "numeric",
    }),
    weekday: "short",
    hour: "2-digit",
    minute: "2-digit",
  });

/** The snooze part of a thread's or project's menu: the presets, or unsnoozing what is snoozed. */
function SnoozeItems({
  own,
  byProject,
  onSet,
  onClear,
}: {
  own?: Muted;
  byProject?: Muted;
  onSet: (preset: SnoozePreset) => void;
  onClear: () => void;
}) {
  const { t, locale } = useI18n();
  if (own)
    return (
      <DropdownMenuItem onClick={onClear}>
        <BellIcon />
        {own.until
          ? t("snooze.off", { time: wakeTime(own.until, locale) })
          : t("snooze.offForever")}
      </DropdownMenuItem>
    );
  if (byProject)
    return (
      <DropdownMenuItem disabled>
        <BellOffIcon />
        {t("snooze.byProject")}
      </DropdownMenuItem>
    );
  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger openOnHover={false}>
        <BellOffIcon />
        {t("snooze.menu")}
      </DropdownMenuSubTrigger>
      <DropdownMenuSubContent>
        {snoozePresets.map((p) => (
          <DropdownMenuItem key={p} onClick={() => onSet(p)}>
            {t(presetKey[p])}
          </DropdownMenuItem>
        ))}
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  );
}

const copy = (text: string, done: string) =>
  navigator.clipboard
    .writeText(text)
    .then(() => toast.success(done), showError);

/** Renaming and ending threads, asked in dialogs: one pair for every thread a place lists. */
export function useThreadDialogs({
  link,
  onChanged,
  onEnded,
}: {
  link: HostLink;
  onChanged: () => unknown;
  onEnded?: (thread: Thread) => void;
}) {
  const { t } = useI18n();
  const [renaming, setRenaming] = useState<Thread>();
  const [ending, setEnding] = useState<Thread>();
  const dialogs = (
    <>
      <RenameDialog
        open={renaming !== undefined}
        onClose={() => setRenaming(undefined)}
        title={t("thread.renameTitle")}
        name={renaming ? threadName(renaming) : ""}
        onRename={(title) =>
          link.threads
            .updateThread({ threadId: renaming!.id, title })
            .then(onChanged)
        }
      />
      <ConfirmDialog
        open={ending !== undefined}
        onClose={() => setEnding(undefined)}
        title={t("thread.endTitle", { name: ending ? threadName(ending) : "" })}
        description={t("thread.endDescription")}
        cancel={t("project.keep")}
        confirm={t("thread.end")}
        destructive
        onConfirm={() => {
          const th = ending;
          return (
            th &&
            link.threads
              .deleteThread({ threadId: th.id })
              .then(() => (onEnded ? onEnded(th) : onChanged()), showError)
          );
        }}
      />
    </>
  );
  return { rename: setRenaming, end: setEnding, dialogs };
}

// One fork at a time, wherever it was asked from.
let forking = false;

/** A thread's menu items. */
export function ThreadMenuItems({
  link,
  thread,
  onChanged,
  onLabel,
  onOpen,
  onRename,
  onEnd,
  onSettings,
  onNewThread,
  shown,
  locked,
  top,
  extra,
}: {
  link: HostLink;
  thread: Thread;
  /** Reads the list or the thread again after a change. */
  onChanged: () => unknown;
  /** Shows a label change at once, before the host answers. */
  onLabel?: (label: "pinned" | "archived", on: boolean) => void;
  /** Goes to a thread, as to a fork once made. */
  onOpen: (threadId: string) => void;
  onRename: () => void;
  onEnd: () => void;
  onSettings: () => void;
  /** Starts another thread in its project, where that is offered. */
  onNewThread?: () => void;
  /** The thread is on screen: it reads as read, and marking it unread would not last. */
  shown?: boolean;
  /** Not this browser's to change: renaming and ending are the driver's. */
  locked?: boolean;
  /** What this place shows first. */
  top?: ReactNode;
  /** What only this place offers, before ending the thread. */
  extra?: ReactNode;
}) {
  const { t } = useI18n();
  const snoozes = useSnoozes(link.hostId);
  const seen = useSeen(link.hostId);
  const pinned = Boolean(thread.labels.pinned);
  const archived = Boolean(thread.labels.archived);
  const label = (name: "pinned" | "archived", on: boolean) => {
    onLabel?.(name, on);
    link.threads
      .updateThread({ threadId: thread.id, [name]: on })
      .catch(showError)
      .finally(onChanged);
  };
  const fork = () => {
    if (forking) return;
    forking = true;
    const made = link.threads
      .forkThread({ threadId: thread.id, seq: thread.lastSeq })
      .then((f) => (void onChanged(), onOpen(f.id)));
    toast.promise(made, { loading: t("thread.forking"), error: errorText });
    made.catch(() => {}).finally(() => (forking = false));
  };
  return (
    <>
      {top}
      {onNewThread && (
        <DropdownMenuItem onClick={onNewThread}>
          <PlusIcon />
          {t("thread.new")}
        </DropdownMenuItem>
      )}
      <DropdownMenuItem onClick={() => label("pinned", !pinned)}>
        {pinned ? <PinOffIcon /> : <PinIcon />}
        {t(pinned ? "thread.unpin" : "thread.pin")}
      </DropdownMenuItem>
      <DropdownMenuItem onClick={() => label("archived", !archived)}>
        {archived ? <ArchiveRestoreIcon /> : <ArchiveIcon />}
        {t(archived ? "archive.unarchiveThread" : "archive.archiveThread")}
      </DropdownMenuItem>
      <SnoozeItems
        own={snoozes.thread(thread.id)}
        byProject={
          thread.projectId ? snoozes.project(thread.projectId) : undefined
        }
        onSet={(preset) => snoozes.set(SnoozeKind.THREAD, thread.id, preset)}
        onClear={() => snoozes.clear(SnoozeKind.THREAD, thread.id)}
      />
      {!shown && thread.activitySeq > 0n && !seen.unread(thread) && (
        <DropdownMenuItem onClick={() => seen.markUnread(thread)}>
          <CircleDotIcon />
          {t("thread.markUnread")}
        </DropdownMenuItem>
      )}
      <DropdownMenuSeparator />
      <DropdownMenuItem disabled={locked} onClick={onRename}>
        <PencilIcon />
        {t("thread.rename")}
      </DropdownMenuItem>
      <DropdownMenuItem onClick={onSettings}>
        <SettingsIcon />
        {t("threadSettings.title")}
      </DropdownMenuItem>
      <DropdownMenuItem disabled={threadWorking(thread)} onClick={fork}>
        <GitForkIcon />
        {t("thread.fork")}
      </DropdownMenuItem>
      <DropdownMenuSub>
        <DropdownMenuSubTrigger openOnHover={false}>
          <CopyIcon />
          {t("common.copy")}
        </DropdownMenuSubTrigger>
        <DropdownMenuSubContent>
          <DropdownMenuItem onClick={() => copy(thread.id, t("common.copied"))}>
            {t("thread.copyId")}
          </DropdownMenuItem>
          <DropdownMenuItem
            onClick={() => copy(thread.cwd, t("common.copied"))}
          >
            {t("thread.copyPath")}
          </DropdownMenuItem>
          {thread.nativeSessionId && (
            <DropdownMenuItem
              onClick={() => copy(thread.nativeSessionId, t("common.copied"))}
            >
              {t("thread.copyNative")}
            </DropdownMenuItem>
          )}
        </DropdownMenuSubContent>
      </DropdownMenuSub>
      {extra}
      <DropdownMenuSeparator />
      <DropdownMenuItem variant="destructive" disabled={locked} onClick={onEnd}>
        <Trash2Icon />
        {t("thread.end")}
      </DropdownMenuItem>
    </>
  );
}

/** Removing projects, asked in a dialog. */
export function useProjectDialogs({
  link,
  onChanged,
  onRemoved,
}: {
  link: HostLink;
  onChanged: () => unknown;
  onRemoved?: (project: Project) => void;
}) {
  const { t } = useI18n();
  const [removing, setRemoving] = useState<Project>();
  const dialogs = (
    <ConfirmDialog
      open={removing !== undefined}
      onClose={() => setRemoving(undefined)}
      title={t("project.removeTitle", { name: removing?.name ?? "" })}
      description={t("project.removeDescription")}
      cancel={t("project.keep")}
      confirm={t("project.remove")}
      destructive
      onConfirm={() => {
        const p = removing;
        return (
          p &&
          link.projects
            .deleteProject({ projectId: p.id })
            .then(() => (onRemoved?.(p), onChanged()), showError)
        );
      }}
    />
  );
  return { remove: setRemoving, dialogs };
}

/** A project's menu items. */
export function ProjectMenuItems({
  link,
  project,
  onChanged,
  onPatch,
  onNewThread,
  onThreads,
  onSettings,
  onRemove,
  canManage,
}: {
  link: HostLink;
  project: Project;
  /** Reads the list again after a change. */
  onChanged: () => unknown;
  /** Shows a change at once, before the host answers. */
  onPatch?: (change: { pinned?: boolean; archived?: boolean }) => void;
  onNewThread: () => void;
  /** Lists all its threads; absent on that list. */
  onThreads?: () => void;
  /** Opens its settings; absent when the account may not change them. */
  onSettings?: () => void;
  onRemove: () => void;
  /** Whether the account may pin, archive and remove projects. */
  canManage: boolean;
}) {
  const { t } = useI18n();
  const snoozes = useSnoozes(link.hostId);
  const patch = (change: { pinned?: boolean; archived?: boolean }) => {
    onPatch?.(change);
    link.projects
      .updateProject({ projectId: project.id, ...change })
      .catch(showError)
      .finally(onChanged);
  };
  return (
    <>
      <DropdownMenuItem onClick={onNewThread}>
        <PlusIcon />
        {t("thread.new")}
      </DropdownMenuItem>
      {onThreads && (
        <DropdownMenuItem onClick={onThreads}>
          <ListIcon />
          {t("archive.allThreads")}
        </DropdownMenuItem>
      )}
      <SnoozeItems
        own={snoozes.project(project.id)}
        onSet={(preset) => snoozes.set(SnoozeKind.PROJECT, project.id, preset)}
        onClear={() => snoozes.clear(SnoozeKind.PROJECT, project.id)}
      />
      {onSettings && (
        <DropdownMenuItem onClick={onSettings}>
          <SettingsIcon />
          {t("projectSettings.open")}
        </DropdownMenuItem>
      )}
      {canManage && (
        <>
          <DropdownMenuItem onClick={() => patch({ pinned: !project.pinned })}>
            {project.pinned ? <PinOffIcon /> : <PinIcon />}
            {t(project.pinned ? "project.unpin" : "project.pin")}
          </DropdownMenuItem>
          <DropdownMenuItem
            onClick={() => patch({ archived: !project.archived })}
          >
            {project.archived ? <ArchiveRestoreIcon /> : <ArchiveIcon />}
            {t(
              project.archived
                ? "archive.unarchiveProject"
                : "archive.archiveProject",
            )}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" onClick={onRemove}>
            <Trash2Icon />
            {t("project.remove")}
          </DropdownMenuItem>
        </>
      )}
    </>
  );
}
