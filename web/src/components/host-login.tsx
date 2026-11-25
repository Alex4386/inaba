import { CheckIcon, LaptopIcon, ShieldAlertIcon } from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";
import { hosts as hostsApi } from "@/api";
import { Secret } from "@/components/dialogs";
import { Logo } from "@/components/logo";
import { Alert, AlertDescription } from "@/components/reui/alert";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import type { Host, HostLogin } from "@/gen/inaba/v1/server_pb";
import { errorText } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** Where a code waits while its user signs in, which may leave the page for an identity provider. */
export const DEVICE_CODE_KEY = "inaba.deviceCode";

/** The page `inaba login` links to, /ui/device?code=...: a signed-in user checks the machine's code and approves it as a host. */
export function DeviceApproval({ code: initial, canShare, onDone }: { code: string; canShare: boolean; onDone: (host?: Host) => void }) {
  const { t } = useI18n();
  const [code, setCode] = useState(initial);
  const [login, setLogin] = useState<HostLogin>();
  const [name, setName] = useState("");
  const [shared, setShared] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ host?: Host }>();

  const look = (c: string) => {
    setError("");
    setBusy(true);
    hostsApi
      .getHostLogin({ userCode: c })
      .then((l) => {
        setLogin(l);
        setName(l.name);
      }, (e) => setError(errorText(e)))
      .finally(() => setBusy(false));
  };
  useEffect(() => {
    if (initial) look(initial);
  }, [initial]);

  const act = (approve: boolean) => {
    if (!login) return;
    setBusy(true);
    setError("");
    (approve ? hostsApi.approveHostLogin({ userCode: login.userCode, name, shared }).then((host) => setResult({ host })) : hostsApi.denyHostLogin({ userCode: login.userCode }).then(() => setResult({})))
      .catch((e) => setError(errorText(e)))
      .finally(() => setBusy(false));
  };

  return (
    <main className="bg-muted grid min-h-svh place-items-center p-4">
      <div className="grid w-full max-w-md gap-6">
        <div className="flex items-center justify-center gap-2 text-lg font-semibold">
          <div className="bg-primary text-primary-foreground flex size-8 items-center justify-center rounded-lg">
            <Logo className="size-5" />
          </div>
          inaba
        </div>
        <div className="bg-card grid gap-5 rounded-xl border p-6 shadow-xs">
          {result ? (
            <div className="grid justify-items-center gap-3 py-2 text-center">
              <div className={cn("flex size-12 items-center justify-center rounded-full", result.host ? "bg-success/10 text-success" : "bg-muted text-muted-foreground")}>
                {result.host ? <CheckIcon className="size-6" /> : <ShieldAlertIcon className="size-6" />}
              </div>
              <h1 className="font-medium">{result.host ? t("device.approved", { name: result.host.name }) : t("device.denied")}</h1>
              <p className="text-muted-foreground text-sm">{t(result.host ? "device.approvedHelp" : "device.deniedHelp")}</p>
              <Button className="mt-2" onClick={() => onDone(result.host)}>
                {t("device.open")}
              </Button>
            </div>
          ) : !login ? (
            <form
              className="grid gap-4"
              onSubmit={(e: FormEvent) => {
                e.preventDefault();
                look(code);
              }}
            >
              <div className="grid gap-1">
                <h1 className="font-medium">{t("device.title")}</h1>
                <p className="text-muted-foreground text-sm">{t("device.enterCode")}</p>
              </div>
              <Input
                aria-label={t("device.code")}
                className="h-14 text-center font-mono text-2xl tracking-[0.3em] uppercase"
                value={code}
                onChange={(e) => setCode(e.target.value.toUpperCase())}
                placeholder="BCDF-GHJK"
                autoFocus
                autoComplete="off"
                spellCheck={false}
                maxLength={9}
              />
              {error && (
                <Alert variant="destructive">
                  <AlertDescription>{error}</AlertDescription>
                </Alert>
              )}
              <Button type="submit" disabled={busy || code.replace(/[^A-Z]/g, "").length !== 8}>
                {busy && <Spinner data-icon="inline-start" />}
                {t("device.continue")}
              </Button>
              <Button type="button" variant="ghost" onClick={() => onDone()}>
                {t("device.cancel")}
              </Button>
            </form>
          ) : (
            <form
              className="grid gap-5"
              onSubmit={(e: FormEvent) => {
                e.preventDefault();
                act(true);
              }}
            >
              <div className="grid gap-1">
                <h1 className="font-medium">{t("device.confirmTitle")}</h1>
                <p className="text-muted-foreground text-sm">{t("device.confirmHelp")}</p>
              </div>
              <div className="bg-muted/50 flex items-center gap-3 rounded-lg border p-3">
                <LaptopIcon className="text-muted-foreground size-5 shrink-0" />
                <div className="grid min-w-0 flex-1 gap-0.5">
                  <span className="truncate text-sm font-medium">{login.name || t("device.unnamed")}</span>
                  {login.address && <span className="text-muted-foreground truncate text-xs">{t("device.from", { address: login.address })}</span>}
                </div>
                <code className="bg-background rounded-md border px-2 py-1 font-mono text-sm tracking-widest">{login.userCode}</code>
              </div>
              <Field>
                <FieldLabel htmlFor="host-name">{t("device.name")}</FieldLabel>
                <Input id="host-name" value={name} onChange={(e) => setName(e.target.value)} required />
              </Field>
              {canShare && (
                <Field>
                  <FieldLabel>{t("device.who")}</FieldLabel>
                  <div className="grid grid-cols-2 gap-2">
                    {[false, true].map((s) => (
                      <button
                        key={String(s)}
                        type="button"
                        aria-pressed={shared === s}
                        onClick={() => setShared(s)}
                        className={cn("hover:bg-muted/50 grid gap-0.5 rounded-lg border p-3 text-left", shared === s && "border-primary ring-primary/20 ring-2")}
                      >
                        <span className="text-sm font-medium">{t(s ? "device.shared" : "device.personal")}</span>
                        <span className="text-muted-foreground text-xs">{t(s ? "device.sharedHelp" : "device.personalHelp")}</span>
                      </button>
                    ))}
                  </div>
                  <FieldDescription>{t("device.whoHelp")}</FieldDescription>
                </Field>
              )}
              {error && (
                <Alert variant="destructive">
                  <AlertDescription>{error}</AlertDescription>
                </Alert>
              )}
              <div className="grid grid-cols-2 gap-2">
                <Button type="button" variant="outline" disabled={busy} onClick={() => act(false)}>
                  {t("device.deny")}
                </Button>
                <Button type="submit" disabled={busy}>
                  {busy && <Spinner data-icon="inline-start" />}
                  {t("device.approve")}
                </Button>
              </div>
            </form>
          )}
        </div>
      </div>
    </main>
  );
}

/** How to add a host: sign the machine in from its own terminal, then approve it here. */
export function AddHostDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useI18n();
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("host.addTitle")}</DialogTitle>
          <DialogDescription>{t("device.addHelp")}</DialogDescription>
        </DialogHeader>
        <Secret value={`inaba login --server ${location.origin}`} />
        <p className="text-muted-foreground text-sm">{t("device.addAfter")}</p>
        <DialogFooter>
          <Button onClick={onClose}>{t("device.done")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
