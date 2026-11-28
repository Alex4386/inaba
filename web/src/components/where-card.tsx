import { BotIcon, CheckIcon, ChevronsUpDownIcon, CopyIcon, FolderIcon, GitBranchIcon } from "lucide-react";
import type { ReactNode } from "react";
import { toast } from "sonner";
import { LineCounts } from "@/components/changes-panel";
import { HostAvatar } from "@/components/host-icon";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Spinner } from "@/components/ui/spinner";
import type { GitStatus } from "@/gen/inaba/v1/host_pb";
import type { Host } from "@/gen/inaba/v1/server_pb";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { cn, shortPath } from "@/lib/utils";

/** Where a thread runs, at a glance: the host, the folder, and its git branch and changes. */
export function WhereCard({
  host,
  cwd,
  git,
  onChanges,
  connection,
  provider,
  branches,
  children,
  className,
}: {
  host?: Host;
  cwd: string;
  git?: GitStatus;
  /** Opens the changes, when there is somewhere to show them. */
  onChanges?: () => void;
  /** How this browser reaches the host: straight over WebRTC, or through the server. */
  connection?: "direct" | "relayed";
  /** The agent a thread runs, by its display name. */
  provider?: string;
  /** The branches to switch to, for one who may switch them; without, the branch is only shown. */
  branches?: { list: string[]; switching?: string; onSwitch: (branch: string) => void };
  /** More about the thread, under the rest. */
  children?: ReactNode;
  className?: string;
}) {
  const { t } = useI18n();
  return (
    <aside className={cn("bg-card w-72 divide-y rounded-xl border text-sm shadow-xs", className)}>
      <div className="grid gap-0.5 p-1.5">
        {host && (
          <div className="flex items-center gap-2.5 rounded-md px-2 py-1.5">
            <HostAvatar host={host} className="text-muted-foreground size-4" label={t(host.online ? "host.online" : "host.offline")} />
            <span className="min-w-0 flex-1 truncate">{host.name}</span>
            {connection && (
              <span className="text-muted-foreground shrink-0 text-xs" title={t(connection === "direct" ? "thread.directHelp" : "thread.relayedHelp")}>
                {t(connection === "direct" ? "thread.direct" : "thread.relayed")}
              </span>
            )}
          </div>
        )}
        {provider && (
          <div className="flex items-center gap-2.5 rounded-md px-2 py-1.5">
            <BotIcon className="text-muted-foreground size-4 shrink-0" />
            <span className="truncate">{provider}</span>
          </div>
        )}
        <button
          type="button"
          className="hover:bg-muted group flex items-center gap-2.5 rounded-md px-2 py-1.5 text-left"
          title={cwd}
          onClick={() => navigator.clipboard.writeText(cwd).then(() => toast.success(t("common.copied")), showError)}
        >
          <FolderIcon className="text-muted-foreground size-4 shrink-0" />
          <span className="min-w-0 flex-1 truncate">{shortPath(cwd)}</span>
          <CopyIcon className="text-muted-foreground size-3.5 shrink-0 opacity-0 group-hover:opacity-100" />
        </button>
      </div>
      {git?.repository && (
        <div className="grid gap-0.5 p-1.5">
          {branches && branches.list.length > 0 ? (
            <DropdownMenu>
              <DropdownMenuTrigger className="hover:bg-muted aria-expanded:bg-muted flex items-center gap-2.5 rounded-md px-2 py-1.5 text-left" aria-label={t("git.branch")}>
                <GitBranchIcon className="text-muted-foreground size-4 shrink-0" />
                <span className="min-w-0 flex-1 truncate">{branches.switching ?? (git.branch || t("git.detached"))}</span>
                {branches.switching ? <Spinner className="size-3.5 shrink-0" /> : <ChevronsUpDownIcon className="text-muted-foreground size-3.5 shrink-0" />}
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="max-h-72 w-(--anchor-width) min-w-56">
                {branches.list.map((b) => (
                  <DropdownMenuItem key={b} disabled={Boolean(branches.switching)} onClick={() => b !== git.branch && branches.onSwitch(b)}>
                    <span className="min-w-0 flex-1 truncate">{b}</span>
                    {b === git.branch && <CheckIcon />}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          ) : (
            <div className="flex items-center gap-2.5 px-2 py-1.5">
              <GitBranchIcon className="text-muted-foreground size-4 shrink-0" />
              <span className="truncate">{git.branch || t("git.detached")}</span>
            </div>
          )}
          <button type="button" disabled={!onChanges} onClick={onChanges} className="text-muted-foreground enabled:hover:bg-muted flex items-center gap-2.5 rounded-md px-2 py-1.5 text-left">
            <span className="flex-1">{git.files.length ? t("new.changes", { count: String(git.files.length) }) : t("new.clean")}</span>
            {git.files.length > 0 && <LineCounts additions={git.additions} deletions={git.deletions} />}
          </button>
        </div>
      )}
      {children}
    </aside>
  );
}
