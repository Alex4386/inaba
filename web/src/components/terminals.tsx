import { Code, ConnectError } from "@connectrpc/connect";
import { ChevronRightIcon, PlusIcon, SquareTerminalIcon, XIcon } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import type { HostLink } from "@/api";
import { ConfirmDialog } from "@/components/dialogs";
import { Badge } from "@/components/reui/badge";
import { Button } from "@/components/ui/button";
import { SidebarGroup, SidebarGroupAction, SidebarGroupLabel, SidebarMenu, SidebarMenuButton, SidebarMenuItem, SidebarTrigger, useSidebar } from "@/components/ui/sidebar";
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
  useEffect(() => {
    void load();
  }, [load]);
  return { terminals, error, load };
}

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
  // Titles change as people work in them (cd, ssh), so the list follows, as the projects' does.
  useEffect(() => {
    const timer = setInterval(() => void load(), 3_000);
    return () => clearInterval(timer);
  }, [load]);
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
                tooltip={term.title || term.cwd}
                isActive={term.id === selected}
                onClick={() => {
                  setOpenMobile(false);
                  onOpen(term.id);
                }}
              >
                <SquareTerminalIcon />
                <span className={cn("truncate", term.exited && "text-muted-foreground")}>{term.title || term.cwd}</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          ))}
        </SidebarMenu>
      )}
    </SidebarGroup>
  );
}

/** One terminal filling the main area. Leaving it leaves the shell running; coming back attaches again and replays recent output. */
export function HostTerminalPage({ link, id, onClosed }: { link: HostLink; id: string; onClosed: () => void }) {
  const { t } = useI18n();
  const { terminals, error, load } = useTerminals(link);
  const term = terminals?.find((x) => x.id === id);
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
    <div className="flex min-h-0 flex-1 flex-col">
      <Header>
        <SquareTerminalIcon className="text-muted-foreground size-4 shrink-0" />
        <h1 className="min-w-0 truncate text-sm font-medium" title={term?.cwd}>
          {term?.title || term?.cwd}
        </h1>
        {term?.exited && (
          <Badge size="sm" variant="outline">
            {t("terminals.exited")}
          </Badge>
        )}
        {term && (
          <Button size="sm" variant="outline" className="ml-auto" onClick={() => ask(term)}>
            <XIcon data-icon="inline-start" />
            {t("terminals.close")}
          </Button>
        )}
      </Header>
      {error || (terminals && !term) ? (
        <p className="text-muted-foreground p-6 text-sm">{error === "off" ? t("terminals.moduleOff") : error || t("terminals.notFound")}</p>
      ) : !term ? (
        <Spinner className="m-auto" />
      ) : (
        <TerminalView link={link} io={io} onExit={() => void load()} />
      )}
      {dialog}
    </div>
  );
}
