import { Code, ConnectError } from "@connectrpc/connect";
import { ChevronRightIcon, MoreHorizontalIcon, PencilIcon, PlusIcon, SquareTerminalIcon, Trash2Icon, UndoIcon, XIcon } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import type { HostLink } from "@/api";
import { ConfirmDialog, RenameDialog } from "@/components/dialogs";
import { CLIENT, usePresent, WatchingBanner } from "@/components/presence";
import { Badge } from "@/components/reui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { SidebarGroup, SidebarGroupAction, SidebarGroupLabel, SidebarMenu, SidebarMenuAction, SidebarMenuButton, SidebarMenuItem, SidebarTrigger, useSidebar } from "@/components/ui/sidebar";
import { Spinner } from "@/components/ui/spinner";
import type { HostTerminal } from "@/gen/inaba/v1/terminal_pb";
import { errorText, showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { type TerminalIO, TerminalView } from "@/Terminal";

/** The host's terminals; a host without the terminal module answers Unimplemented, which reads as "off". */
function useTerminals(link: HostLink) {
  const [terminals, setTerminals] = useState<HostTerminal[]>();
  const [error, setError] = useState<"off" | string>("");
  const load = useCallback(
    () =>
      link.hostTerminals.listHostTerminals({}).then(
        (r) => (setError(""), setTerminals(r.terminals)),
        (e) => setError(ConnectError.from(e).code === Code.Unimplemented ? "off" : errorText(e)),
      ),
    [link],
  );
  // Titles change as people work in them (cd, ssh), and names as they are set, so the list follows.
  useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), 3_000);
    return () => clearInterval(timer);
  }, [load]);
  return { terminals, error, load };
}

/** What a terminal is called: the name someone gave it, else the title its shell set, else its folder. */
const terminalName = (term: HostTerminal, title = term.title) => term.name || title || term.cwd;

function Header({ children }: { children: React.ReactNode }) {
  return (
    <header className="flex h-12 shrink-0 items-center gap-2 border-b px-3">
      <SidebarTrigger />
      {children}
    </header>
  );
}

/** Asks before ending a terminal whose shell still runs; an exited one closes at once. */
function useClose(link: HostLink, onClosed: () => void) {
  const { t } = useI18n();
  const [asking, setAsking] = useState<HostTerminal>();
  const close = (id: string) => link.hostTerminals.closeHostTerminal({ id }).then(onClosed, showError);
  const dialog = (
    <ConfirmDialog
      open={asking !== undefined}
      onClose={() => setAsking(undefined)}
      title={t("terminals.closeTitle")}
      description={t("terminals.closeHelp")}
      cancel={t("terminals.keep")}
      confirm={t("terminals.close")}
      destructive
      onConfirm={() => asking && close(asking.id).finally(() => setAsking(undefined))}
    />
  );
  return { ask: (term: HostTerminal) => (term.exited ? void close(term.id) : setAsking(term)), dialog };
}

/** The host's terminals in the sidebar, under a heading that folds them, each by the title its shell set. */
export function TerminalTree({ link, selected, onOpen }: { link: HostLink; selected?: string; onOpen: (id: string) => void }) {
  const { t } = useI18n();
  const { setOpenMobile } = useSidebar();
  const { terminals, error, load } = useTerminals(link);
  const [open, setOpen] = useState(true);
  const [opening, setOpening] = useState(false);
  const [renaming, setRenaming] = useState<HostTerminal>();
  const { ask, dialog } = useClose(link, () => void load());
  const rename = (id: string, name: string) => link.hostTerminals.renameHostTerminal({ id, name }).then(() => load());
  if (error === "off") return null;
  const start = () => {
    if (opening) return;
    setOpening(true);
    link.hostTerminals
      .openHostTerminal({})
      .then((term) => {
        setOpenMobile(false);
        setOpen(true);
        void load();
        onOpen(term.id);
      }, showError)
      .finally(() => setOpening(false));
  };
  return (
    <SidebarGroup>
      <SidebarGroupLabel render={<button type="button" onClick={() => setOpen((o) => !o)} />} className="gap-1">
        <ChevronRightIcon className={cn("size-3.5 transition-transform", open && "rotate-90")} />
        {t("terminals.title")}
      </SidebarGroupLabel>
      <SidebarGroupAction title={t("terminals.new")} onClick={start} disabled={opening}>
        {opening ? <Spinner /> : <PlusIcon />}
        <span className="sr-only">{t("terminals.new")}</span>
      </SidebarGroupAction>
      {open && (
        <SidebarMenu>
          {error && <li className="text-destructive px-2 py-1 text-xs">{error}</li>}
          {terminals?.map((term) => (
            <SidebarMenuItem key={term.id}>
              <SidebarMenuButton
                tooltip={terminalName(term)}
                isActive={term.id === selected}
                onClick={() => {
                  setOpenMobile(false);
                  onOpen(term.id);
                }}
              >
                <SquareTerminalIcon />
                <span className={cn("truncate", term.exited && "text-muted-foreground")}>{terminalName(term)}</span>
              </SidebarMenuButton>
              <DropdownMenu>
                <DropdownMenuTrigger render={<SidebarMenuAction showOnHover className="aria-expanded:bg-muted" />}>
                  <MoreHorizontalIcon />
                  <span className="sr-only">{t("terminals.more", { name: terminalName(term) })}</span>
                </DropdownMenuTrigger>
                <DropdownMenuContent className="w-auto whitespace-nowrap" side="right" align="start">
                  <DropdownMenuItem onClick={() => setRenaming(term)}>
                    <PencilIcon />
                    {t("terminals.rename")}
                  </DropdownMenuItem>
                  {term.name && (
                    <DropdownMenuItem onClick={() => rename(term.id, "").catch(showError)}>
                      <UndoIcon />
                      {t("terminals.useShellTitle")}
                    </DropdownMenuItem>
                  )}
                  <DropdownMenuSeparator />
                  <DropdownMenuItem variant="destructive" onClick={() => ask(term)}>
                    <Trash2Icon />
                    {t("terminals.close")}
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </SidebarMenuItem>
          ))}
        </SidebarMenu>
      )}
      <RenameDialog
        open={renaming !== undefined}
        onClose={() => setRenaming(undefined)}
        title={t("terminals.renameTitle")}
        name={renaming ? terminalName(renaming) : ""}
        onRename={(name) => rename(renaming!.id, name)}
      />
      {dialog}
    </SidebarGroup>
  );
}

/** One terminal filling the main area. Leaving it leaves the shell running; coming back attaches again and replays recent output. */
export function HostTerminalPage({ link, id, onClosed, stealth }: { link: HostLink; id: string; onClosed: () => void; /** Never takes a free seat by itself. */ stealth?: boolean }) {
  const { t } = useI18n();
  // One browser tab types and sizes the terminal, as a thread's driver does; every other tab watches.
  const { clients, seat, take, refresh } = usePresent();
  // Answers asked before this terminal opened are the last place's, which the page above has not reset yet.
  const openedAt = useMemo(() => performance.now(), [id]);
  // A tab taking the seat tells this browser's other tabs at once, so the one that lost it stops typing now, not at its next beat.
  // shortcut: a tab in another browser still learns at its next beat, up to 10 s; the host does not check the seat.
  useEffect(() => {
    if (typeof BroadcastChannel === "undefined") return;
    const channel = new BroadcastChannel("inaba.seat");
    channel.onmessage = (e: MessageEvent<string>) => e.data === id && refresh();
    return () => channel.close();
  }, [id, refresh]);
  // Not before the server has answered for this terminal: a duplicated tab learns its own id only then.
  const driver = seat?.driver || undefined;
  const driving = seat !== undefined && driver === CLIENT;
  // A free seat goes by itself only to a tab alone on the terminal; with others watching, the first to ask gets it.
  useEffect(() => {
    if (!stealth && seat && seat.sentAt >= openedAt && clients && !seat.driver && clients.length === 0) void take("");
  }, [seat, clients, stealth, take, openedAt]);
  const askToTake = () =>
    void take(driver ?? "").then((s) => {
      if (s && s.driver !== CLIENT) toast.error(t("drive.denied"));
      else if (s && typeof BroadcastChannel !== "undefined") {
        const channel = new BroadcastChannel("inaba.seat");
        channel.postMessage(id);
        channel.close();
      }
    });
  const { terminals, error, load } = useTerminals(link);
  const term = terminals?.find((x) => x.id === id);
  // Live from the terminal itself, ahead of the listing; reset when another terminal opens.
  const [live, setLive] = useState<{ id: string; title: string }>();
  const title = term && terminalName(term, (live?.id === id && live.title) || term.title);
  const { ask, dialog } = useClose(link, onClosed);
  const io = useMemo<TerminalIO>(
    () => ({
      attach: (size, signal) => link.hostTerminals.attachHostTerminal({ id, ...size }, { signal }),
      write: (data) => link.hostTerminals.writeHostTerminal({ id, data }),
      resize: (size) => link.hostTerminals.resizeHostTerminal({ id, ...size }),
    }),
    [link, id],
  );
  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      {/* Only the owner's own tabs share a terminal's seat, so it is always theirs to take. */}
      {seat && !driving && term && <WatchingBanner driver={driver} onTake={askToTake} />}
      <Header>
        <SquareTerminalIcon className="text-muted-foreground size-4 shrink-0" />
        <h1 className="min-w-0 truncate text-sm font-medium" title={term?.cwd}>
          {title}
        </h1>
        {term?.exited && (
          <Badge size="sm" variant="outline">
            {t("terminals.exited")}
          </Badge>
        )}
        <div className="ml-auto flex shrink-0 items-center gap-2">
          {/* Who else has this terminal open. */}
          {term && (
            <Button size="icon-sm" variant="ghost" aria-label={t("terminals.close")} title={t("terminals.close")} disabled={!driving} onClick={() => ask(term)}>
              <XIcon />
            </Button>
          )}
        </div>
      </Header>
      {error || (terminals && !term) ? (
        <p className="text-muted-foreground p-6 text-sm">{error === "off" ? t("terminals.moduleOff") : error || t("terminals.notFound")}</p>
      ) : !term ? (
        <Spinner className="m-auto" />
      ) : (
        <TerminalView key={id} link={link} io={io} driving={driving} onExit={() => void load()} onTitle={(t) => setLive({ id, title: t })} />
      )}
      {dialog}
    </div>
  );
}
