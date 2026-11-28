import { Code, ConnectError } from "@connectrpc/connect";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useEffect, useState } from "react";
import type { HostLink } from "@/api";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Spinner } from "@/components/ui/spinner";
import type { Provider, ProviderSession } from "@/gen/inaba/v1/host_pb";
import { errorText, showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

/**
 * The sessions a provider's agent keeps in the project's folder, as ones started at a terminal outside inaba,
 * to take one up as a thread with its conversation; one a thread already goes on with opens that thread.
 */
export function ImportSessionDialog({
  link,
  provider,
  projectId,
  language,
  onClose,
  onThread,
}: {
  link: HostLink;
  provider: Provider;
  projectId: string;
  language: string;
  onClose: () => void;
  onThread: (threadId: string) => void;
}) {
  const { t, locale } = useI18n();
  const [sessions, setSessions] = useState<ProviderSession[]>();
  const [error, setError] = useState("");
  const [taking, setTaking] = useState<string>();
  useEffect(() => {
    link.threads.listProviderSessions({ provider: provider.id, projectId }).then(
      (r) => setSessions(r.sessions),
      (e) => setError(ConnectError.from(e).code === Code.Unimplemented ? t("import.unsupported", { provider: provider.displayName || provider.id }) : errorText(e)),
    );
  }, [link, provider, projectId, t]);
  const take = (s: ProviderSession) => {
    setTaking(s.id);
    link.threads
      .createThread({ provider: provider.id, projectId, nativeSessionId: s.id, title: s.title, language })
      .then((th) => onThread(th.id), showError)
      .finally(() => setTaking(undefined));
  };
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t("import.title")}</DialogTitle>
          <DialogDescription>{t("import.help", { provider: provider.displayName || provider.id })}</DialogDescription>
        </DialogHeader>
        {error ? (
          <p className="text-muted-foreground text-sm">{error}</p>
        ) : !sessions ? (
          <Spinner className="mx-auto my-6" />
        ) : sessions.length === 0 ? (
          <p className="text-muted-foreground text-sm">{t("import.none")}</p>
        ) : (
          <ul className="-mx-2 grid max-h-[60svh] gap-1 overflow-y-auto">
            {sessions.map((s) => (
              <li key={s.id} className="hover:bg-muted flex items-center gap-3 rounded-md px-2 py-1.5">
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm">{s.title || s.id}</p>
                  <p className="text-muted-foreground truncate font-mono text-xs">
                    {s.updatedAt && timestampDate(s.updatedAt).toLocaleString(locale)} · {s.id}
                  </p>
                </div>
                {s.threadId ? (
                  <Button size="sm" variant="outline" onClick={() => onThread(s.threadId)}>
                    {t("import.open")}
                  </Button>
                ) : (
                  <Button size="sm" disabled={taking !== undefined} onClick={() => take(s)}>
                    {taking === s.id && <Spinner data-icon="inline-start" />}
                    {t("import.take")}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
      </DialogContent>
    </Dialog>
  );
}
