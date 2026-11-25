import { timestampDate } from "@bufbuild/protobuf/wkt";
import { ImageIcon, KeyRoundIcon, MonitorIcon, Trash2Icon } from "lucide-react";
import { type ChangeEvent, type FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { auth } from "@/api";
import { ConfirmDialog, RenameDialog } from "@/components/dialogs";
import { pictureOf } from "@/components/host-about";
import { UserAvatar } from "@/components/presence";
import { Badge } from "@/components/reui/badge";
import { Card, Row } from "@/components/section-dialog";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import type { BeginTotpResponse, LoginSession, Passkey, User } from "@/gen/inaba/v1/server_pb";
import { errorText, showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

/** Asks the browser for a new passkey and registers it, the WebAuthn way, through JSON both sides read. */
export async function createPasskey(name: string) {
  const begun = await auth.beginPasskeyRegistration({});
  const options = PublicKeyCredential.parseCreationOptionsFromJSON(JSON.parse(begun.optionsJson).publicKey ?? JSON.parse(begun.optionsJson));
  const credential = (await navigator.credentials.create({ publicKey: options })) as PublicKeyCredential | null;
  if (!credential) throw new Error("no passkey was made");
  return auth.finishPasskeyRegistration({ ceremonyId: begun.ceremonyId, credentialJson: JSON.stringify(credential.toJSON()), name });
}

/** Signs in with a passkey the browser offers, without a name typed. */
export async function signInWithPasskey() {
  const begun = await auth.beginPasskeyLogin({});
  const parsed = JSON.parse(begun.optionsJson);
  const options = PublicKeyCredential.parseRequestOptionsFromJSON(parsed.publicKey ?? parsed);
  const credential = (await navigator.credentials.get({ publicKey: options })) as PublicKeyCredential | null;
  if (!credential) throw new Error("no passkey was chosen");
  return auth.finishPasskeyLogin({ ceremonyId: begun.ceremonyId, credentialJson: JSON.stringify(credential.toJSON()) });
}

const when = (t?: { seconds: bigint; nanos: number }) => (t ? timestampDate(t as never).toLocaleString() : "");

/** A browser as people name it, from what it says it is. */
function browserName(agent: string) {
  const browser = /Edg\//.test(agent) ? "Edge" : /Chrome\//.test(agent) ? "Chrome" : /Firefox\//.test(agent) ? "Firefox" : /Safari\//.test(agent) ? "Safari" : "";
  const os = /Mac OS X/.test(agent) ? "macOS" : /Windows/.test(agent) ? "Windows" : /Android/.test(agent) ? "Android" : /iPhone|iPad/.test(agent) ? "iOS" : /Linux/.test(agent) ? "Linux" : "";
  return [browser, os].filter(Boolean).join(" · ") || agent || "—";
}

function PasswordDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useI18n();
  const [error, setError] = useState("");
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    if (form.get("new") !== form.get("again")) return setError(t("profile.passwordsDiffer"));
    try {
      await auth.changePassword({ currentPassword: String(form.get("current")), newPassword: String(form.get("new")) });
      toast.success(t("profile.passwordChanged"));
      onClose();
    } catch (err) {
      setError(errorText(err));
    }
  };
  return (
    <Dialog open={open} onOpenChange={(o) => !o && (setError(""), onClose())}>
      <DialogContent>
        <form onSubmit={(e) => void submit(e)} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>{t("profile.changePassword")}</DialogTitle>
            <DialogDescription>{t("profile.changePasswordHelp")}</DialogDescription>
          </DialogHeader>
          <Field>
            <FieldLabel htmlFor="current">{t("profile.currentPassword")}</FieldLabel>
            <Input id="current" name="current" type="password" autoComplete="current-password" required autoFocus />
          </Field>
          <Field>
            <FieldLabel htmlFor="new">{t("profile.newPassword")}</FieldLabel>
            <Input id="new" name="new" type="password" autoComplete="new-password" minLength={8} required />
          </Field>
          <Field>
            <FieldLabel htmlFor="again">{t("profile.newPasswordAgain")}</FieldLabel>
            <Input id="again" name="again" type="password" autoComplete="new-password" minLength={8} required />
          </Field>
          {error && <p className="text-destructive text-sm">{error}</p>}
          <DialogFooter>
            <Button type="submit">{t("profile.changePassword")}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** Setting up two-step sign-in: scan the code, then prove it with one from the app. Or turning it off with a code. */
function TotpDialog({ user, open, onClose, onUser }: { user: User; open: boolean; onClose: () => void; onUser: (u: User) => void }) {
  const { t } = useI18n();
  const [setup, setSetup] = useState<BeginTotpResponse>();
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    setCode("");
    setError("");
    setSetup(undefined);
    if (open && !user.totp) auth.beginTotp({}).then(setSetup, showError);
  }, [open, user.totp]);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    try {
      onUser(user.totp ? await auth.disableTotp({ code }) : await auth.enableTotp({ code }));
      toast.success(t(user.totp ? "profile.totpOff" : "profile.totpOn"));
      onClose();
    } catch (err) {
      setError(errorText(err));
    }
  };
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <form onSubmit={(e) => void submit(e)} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>{t(user.totp ? "profile.totpDisable" : "profile.totpSetup")}</DialogTitle>
            <DialogDescription>{t(user.totp ? "profile.totpDisableHelp" : "profile.totpSetupHelp")}</DialogDescription>
          </DialogHeader>
          {setup && (
            <div className="grid justify-items-center gap-2">
              <img src={setup.qrCode} alt={t("profile.totpQr")} className="size-48 rounded-md bg-white p-2" />
              <code className="bg-muted rounded px-2 py-1 font-mono text-xs break-all select-all">{setup.secret}</code>
            </div>
          )}
          <Field>
            <FieldLabel htmlFor="totp">{t("profile.totpCode")}</FieldLabel>
            <Input id="totp" value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, "").slice(0, 6))} inputMode="numeric" autoComplete="one-time-code" placeholder="123456" required autoFocus />
          </Field>
          {error && <p className="text-destructive text-sm">{error}</p>}
          <DialogFooter>
            <Button type="submit" variant={user.totp ? "destructive" : "default"} disabled={code.length !== 6}>
              {t(user.totp ? "profile.totpDisable" : "profile.totpEnable")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** The signed-in account: how others see it, and how it signs in. */
export function ProfilePanel({ user, onUser }: { user: User; onUser: (user: User) => void }) {
  const { t } = useI18n();
  const file = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [name, setName] = useState(user.displayName);
  const [changing, setChanging] = useState(false);
  const [totpOpen, setTotpOpen] = useState(false);
  const [passkeys, setPasskeys] = useState<Passkey[]>([]);
  const [sessions, setSessions] = useState<LoginSession[]>([]);
  const [removing, setRemoving] = useState<Passkey>();
  const passkeysSupported = typeof PublicKeyCredential !== "undefined" && "parseCreationOptionsFromJSON" in PublicKeyCredential;

  const load = useCallback(() => {
    auth.listPasskeys({}).then((r) => setPasskeys(r.passkeys), showError);
    auth.listSessions({}).then((r) => setSessions(r.sessions), showError);
  }, []);
  useEffect(load, [load]);

  const update = (change: { avatar?: string; displayName?: string; stealth?: boolean }) => {
    setBusy(true);
    return auth
      .updateProfile(change)
      .then(onUser, showError)
      .finally(() => setBusy(false));
  };
  const pick = async (e: ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0];
    e.target.value = "";
    if (f) await pictureOf(f).then((avatar) => update({ avatar }), showError);
  };
  // A new passkey is named in a dialog of ours first; then the browser's own prompt makes it.
  const [naming, setNaming] = useState(false);
  const addPasskey = async (label: string) => {
    try {
      await createPasskey(label);
      toast.success(t("profile.passkeyAdded"));
      load();
    } catch (err) {
      // Closing the browser's own prompt is a choice, not a failure.
      if (!(err instanceof DOMException && err.name === "NotAllowedError")) showError(err);
    }
  };

  return (
    <div className="grid gap-4">
      <div className="flex items-center gap-4">
        <UserAvatar id={user.id} name={user.displayName || user.name} url={user.avatarUrl} size="lg" className="size-16 text-lg" />
        <div className="grid gap-1">
          <h3 className="text-lg font-semibold">{user.displayName || user.name}</h3>
          <div className="flex items-center gap-1.5">
            <span className="text-muted-foreground text-xs">@{user.name}</span>
            {user.admin && (
              <Badge size="sm" variant="info-light">
                {t("accounts.admin")}
              </Badge>
            )}
          </div>
        </div>
      </div>

      <Card>
        <Row title={t("profile.picture")} description={t("profile.pictureHelp")}>
          <div className="flex gap-2">
            <input ref={file} type="file" accept="image/png,image/jpeg,image/webp" className="hidden" onChange={(e) => void pick(e)} />
            <Button variant="outline" disabled={busy} onClick={() => file.current?.click()}>
              <ImageIcon data-icon="inline-start" />
              {t(user.avatarUrl ? "hostAbout.changePicture" : "profile.upload")}
            </Button>
            {user.avatarUrl && (
              <Button variant="ghost" disabled={busy} onClick={() => void update({ avatar: "" })}>
                <Trash2Icon data-icon="inline-start" />
                {t("hostAbout.removePicture")}
              </Button>
            )}
          </div>
        </Row>
        <Row title={t("profile.displayName")} description={t("profile.displayNameHelp")}>
          <form
            className="flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              void update({ displayName: name.trim() });
            }}
          >
            <Input className="w-48" value={name} onChange={(e) => setName(e.target.value)} maxLength={64} placeholder={user.name} />
            <Button type="submit" variant="outline" disabled={busy || name.trim() === user.displayName}>
              {t("providers.save")}
            </Button>
          </form>
        </Row>
        {user.admin && (
          <Row title={t("profile.stealth")} description={t("profile.stealthHelp")}>
            <Switch checked={user.stealth} disabled={busy} aria-label={t("profile.stealth")} onCheckedChange={(stealth) => void update({ stealth })} />
          </Row>
        )}
      </Card>

      <h3 className="px-1 text-sm font-semibold">{t("profile.security")}</h3>
      <Card>
        {!user.oidc && (
          <Row title={t("profile.password")} description={t("profile.passwordHelp")}>
            <Button variant="outline" onClick={() => setChanging(true)}>
              {t("profile.changePassword")}
            </Button>
          </Row>
        )}
        {!user.oidc && (
          <Row title={t("profile.totp")} description={t(user.totp ? "profile.totpOnHelp" : "profile.totpOffHelp")}>
            <div className="flex items-center gap-2">
              <Badge size="sm" variant={user.totp ? "success-light" : "outline"}>
                {t(user.totp ? "profile.on" : "profile.off")}
              </Badge>
              <Button variant="outline" onClick={() => setTotpOpen(true)}>
                {t(user.totp ? "profile.totpDisable" : "profile.totpSetup")}
              </Button>
            </div>
          </Row>
        )}
        {user.oidc && <p className="text-muted-foreground px-4 py-3 text-sm">{t("profile.oidcHelp")}</p>}
        <div className="grid gap-2 px-4 py-3">
          <div className="flex flex-wrap items-start gap-x-4 gap-y-2">
            <div className="grid min-w-48 flex-1 gap-0.5">
              <span className="text-sm font-medium">{t("profile.passkeys")}</span>
              <span className="text-muted-foreground text-xs">{t(passkeysSupported ? "profile.passkeysHelp" : "profile.passkeysUnsupported")}</span>
            </div>
            <Button variant="outline" disabled={!passkeysSupported} onClick={() => setNaming(true)}>
              <KeyRoundIcon data-icon="inline-start" />
              {t("profile.addPasskey")}
            </Button>
          </div>
          {passkeys.map((k) => (
            <div key={k.id} className="flex items-center gap-3 rounded-md border px-3 py-2">
              <KeyRoundIcon className="text-muted-foreground size-4" />
              <div className="grid min-w-0 flex-1">
                <span className="truncate text-sm">{k.name}</span>
                <span className="text-muted-foreground text-xs">
                  {t("profile.added", { when: when(k.createdAt) })}
                  {k.lastUsedAt && ` · ${t("profile.lastUsed", { when: when(k.lastUsedAt) })}`}
                </span>
              </div>
              <Button size="icon-sm" variant="ghost" aria-label={t("accounts.remove", { name: k.name })} onClick={() => setRemoving(k)}>
                <Trash2Icon />
              </Button>
            </div>
          ))}
        </div>
      </Card>

      <h3 className="px-1 text-sm font-semibold">{t("profile.sessions")}</h3>
      <Card>
        {sessions.map((s) => (
          <div key={s.id} className="flex items-center gap-3 px-4 py-3">
            <MonitorIcon className="text-muted-foreground size-4" />
            <div className="grid min-w-0 flex-1">
              <span className="flex items-center gap-1.5 text-sm">
                {browserName(s.userAgent)}
                {s.current && (
                  <Badge size="sm" variant="success-light">
                    {t("profile.thisBrowser")}
                  </Badge>
                )}
              </span>
              <span className="text-muted-foreground text-xs">{t("profile.signedInAt", { when: when(s.createdAt) })}</span>
            </div>
            {!s.current && (
              <Button size="sm" variant="ghost" onClick={() => auth.revokeSession({ id: s.id }).then(load, showError)}>
                {t("profile.signOutBrowser")}
              </Button>
            )}
          </div>
        ))}
      </Card>

      <PasswordDialog open={changing} onClose={() => (setChanging(false), load())} />
      <TotpDialog user={user} open={totpOpen} onClose={() => setTotpOpen(false)} onUser={onUser} />
      <RenameDialog open={naming} onClose={() => setNaming(false)} title={t("profile.passkeyName")} name={browserName(navigator.userAgent)} onRename={addPasskey} />
      <ConfirmDialog
        open={removing !== undefined}
        onClose={() => setRemoving(undefined)}
        title={t("accounts.remove", { name: removing?.name ?? "" })}
        description={t("profile.removePasskeyHelp")}
        cancel={t("project.keep")}
        confirm={t("accounts.remove", { name: removing?.name ?? "" })}
        destructive
        onConfirm={() => removing && auth.deletePasskey({ id: removing.id }).then(load, showError)}
      />
    </div>
  );
}
