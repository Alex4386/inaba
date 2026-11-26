import { Code, ConnectError } from "@connectrpc/connect";
import { ArrowLeftIcon, PlusIcon, XIcon } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import type { HostLink } from "@/api";
import { ConfirmDialog } from "@/components/dialogs";
import { timeAgo } from "@/components/project-tree";
import { Badge } from "@/components/reui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type { HostTerminal } from "@/gen/inaba/v1/terminal_pb";
import { errorText, showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
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

/** Every terminal on the host, apart from projects and threads, and a way to open another. */
export function TerminalsPage({ link, onOpen }: { link: HostLink; onOpen: (id: string) => void }) {
  const { t, locale } = useI18n();
  const { terminals, error, load } = useTerminals(link);
  const [cwd, setCwd] = useState("");
  const [opening, setOpening] = useState(false);
  const { ask, dialog } = useClose(link, () => void load());
  const open = (e: React.FormEvent) => {
    e.preventDefault();
    setOpening(true);
    link.hostTerminals
      .openHostTerminal({ cwd: cwd.trim() })
      .then((term) => onOpen(term.id), showError)
      .finally(() => setOpening(false));
  };
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <Header>
        <h1 className="text-sm font-medium">{t("terminals.title")}</h1>
      </Header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto grid w-full max-w-3xl grid-cols-[minmax(0,1fr)] gap-4 p-6">
          {error === "off" ? (
            <p className="text-muted-foreground text-sm">{t("terminals.moduleOff")}</p>
          ) : error ? (
            <div className="grid justify-items-start gap-2">
              <p className="text-destructive text-sm">{error}</p>
              <Button size="sm" variant="outline" onClick={() => void load()}>
                {t("common.retry")}
              </Button>
            </div>
          ) : !terminals ? (
            <Spinner />
          ) : (
            <>
              <form className="flex gap-2" onSubmit={open}>
                <Input className="max-w-80 font-mono" value={cwd} onChange={(e) => setCwd(e.target.value)} placeholder={t("terminals.folder")} aria-label={t("terminals.folder")} />
                <Button type="submit" size="sm" disabled={opening}>
                  {opening ? <Spinner data-icon="inline-start" /> : <PlusIcon data-icon="inline-start" />}
                  {t("terminals.new")}
                </Button>
              </form>
              {terminals.length === 0 ? (
                <p className="text-muted-foreground text-sm">{t("terminals.none")}</p>
              ) : (
                <div className="overflow-hidden rounded-lg border">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>{t("listing.folder")}</TableHead>
                        <TableHead>{t("terminals.started")}</TableHead>
                        <TableHead />
                        <TableHead className="w-0" />
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {terminals.map((term) => (
                        <TableRow key={term.id} className="cursor-pointer" onClick={() => onOpen(term.id)}>
                          <TableCell className="max-w-80 truncate font-mono">{term.cwd}</TableCell>
                          <TableCell className="text-muted-foreground">{term.startedAt && timeAgo(new Date(Number(term.startedAt.seconds) * 1000), locale, t("common.justNow"))}</TableCell>
                          <TableCell>
                            <Badge size="sm" variant="outline">
                              {term.exited ? t("terminals.exited") : t("terminals.running")}
                            </Badge>
                          </TableCell>
                          <TableCell>
                            <Button
                              size="icon-sm"
                              variant="ghost"
                              aria-label={t("terminals.close")}
                              title={t("terminals.close")}
                              onClick={(e) => {
                                e.stopPropagation();
                                ask(term);
                              }}
                            >
                              <XIcon />
                            </Button>
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              )}
            </>
          )}
        </div>
      </div>
      {dialog}
    </div>
  );
}

/** One terminal filling the main area. Leaving it leaves the shell running; coming back attaches again and replays recent output. */
export function HostTerminalPage({ link, id, onBack }: { link: HostLink; id: string; onBack: () => void }) {
  const { t } = useI18n();
  const { terminals, error, load } = useTerminals(link);
  const term = terminals?.find((x) => x.id === id);
  const { ask, dialog } = useClose(link, onBack);
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
        <Button size="icon-sm" variant="ghost" onClick={onBack} aria-label={t("terminals.back")} title={t("terminals.back")}>
          <ArrowLeftIcon />
        </Button>
        <h1 className="min-w-0 truncate font-mono text-sm">{term?.cwd}</h1>
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
