import { timestampDate } from "@bufbuild/protobuf/wkt";
import { BotIcon, LoaderCircleIcon, SquareTerminalIcon } from "lucide-react";
import { useEffect, useState } from "react";
import type { HostLink } from "@/api";
import { ProjectIcon, threadName, threadState, threadWorking, timeAgo } from "@/components/project-tree";
import { ProjectSchema, type Thread, ThreadMode, ThreadSchema, ThreadStatus } from "@/gen/inaba/v1/host_pb";
import { cached } from "@/lib/host-cache";
import { poll } from "@/lib/poll";
import { useI18n } from "@/lib/i18n";

// Enough to pick up where one left off without making a second sidebar of it.
const SHOWN = 6;

/** The threads last at work on the host, to go back to one from the start page. */
export function RecentThreads({ link, onOpen }: { link: HostLink; onOpen: (threadId: string) => void }) {
  const { t, locale } = useI18n();
  // What the sidebar last read, at once; then the host's own answer.
  const [threads, setThreads] = useState<Thread[]>(() => cached(link.hostId, "threads", ThreadSchema) ?? []);
  const [projects] = useState(() => cached(link.hostId, "projects", ProjectSchema) ?? []);
  // Read again now and then, while it is shown, so a thread at work elsewhere moves up.
  useEffect(() => poll(() => link.threads.listThreads({}).then((r) => setThreads(r.threads)), 15_000), [link]);
  const when = (th: Thread) => (th.updatedAt ? timestampDate(th.updatedAt).getTime() : 0);
  const recent = threads
    .filter((th) => !th.labels.archived)
    .toSorted((a, b) => when(b) - when(a))
    .slice(0, SHOWN);
  if (recent.length === 0) return null;
  return (
    <section className="w-full max-w-md text-left">
      <h2 className="text-muted-foreground mb-1.5 px-2 text-xs font-medium">{t("home.recent")}</h2>
      <ul className="grid gap-0.5">
        {recent.map((th) => {
          const project = projects.find((p) => p.id === th.projectId);
          const Icon = threadWorking(th) ? LoaderCircleIcon : th.mode === ThreadMode.TERMINAL ? SquareTerminalIcon : BotIcon;
          return (
            <li key={th.id}>
              <button type="button" className="hover:bg-muted flex w-full items-center gap-2.5 rounded-lg px-2 py-1.5 text-left" onClick={() => onOpen(th.id)}>
                <Icon className={threadWorking(th) ? "text-muted-foreground size-4 shrink-0 animate-spin" : "text-muted-foreground size-4 shrink-0"} />
                <span className="grid min-w-0 flex-1">
                  <span className="truncate text-sm">{threadName(th)}</span>
                  <span className="text-muted-foreground truncate text-xs">
                    {threadState(th, t)}
                    {/* As the sidebar says it: how long ago, unless it is at work or asking now. */}
                    {!threadWorking(th) && th.status !== ThreadStatus.WAITING && when(th) > 0 && ` · ${timeAgo(new Date(when(th)), locale, t("common.justNow"))}`}
                  </span>
                </span>
                {project && (
                  <span className="text-muted-foreground flex max-w-[40%] shrink-0 items-center gap-1.5 text-xs">
                    <ProjectIcon project={project} className="size-4" />
                    <span className="truncate">{project.name}</span>
                  </span>
                )}
              </button>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
