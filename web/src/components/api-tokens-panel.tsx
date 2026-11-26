import { Trash2Icon } from "lucide-react";
import { type FormEvent, useCallback, useEffect, useState } from "react";
import { auth } from "@/api";
import { Secret } from "@/components/dialogs";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import type { Token } from "@/gen/inaba/v1/server_pb";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

/** The signed-in user's API tokens: see them, make one, revoke one. */
export function ApiTokensPanel() {
  const { t, locale } = useI18n();
  const [tokens, setTokens] = useState<Token[]>();
  const [secret, setSecret] = useState("");
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState("");

  const refresh = useCallback(() => auth.listTokens({}).then((r) => setTokens(r.tokens), (e) => (showError(e), setTokens((l) => l ?? []))), []);
  useEffect(() => {
    void refresh();
  }, [refresh]);

  const create = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (creating) return;
    const form = event.currentTarget;
    setCreating(true);
    try {
      const created = await auth.createToken({ name: String(new FormData(form).get("name")) });
      setSecret(created.secret);
      form.reset();
      await refresh();
    } catch (e) {
      showError(e);
    } finally {
      setCreating(false);
    }
  };
  const revoke = (id: string) => {
    setRevoking(id);
    return auth
      .revokeToken({ id })
      .then(refresh, showError)
      .finally(() => setRevoking(""));
  };

  return (
    <div className="grid gap-4">
      <p className="text-muted-foreground text-sm">{t("tokens.description")}</p>
        {secret && (
          <div className="grid gap-2">
            <p className="text-sm font-medium">{t("tokens.secret")}</p>
            <Secret value={secret} />
          </div>
        )}
        {!tokens ? (
          <Spinner />
        ) : tokens.length === 0 ? (
          <p className="text-muted-foreground text-sm">{t("tokens.none")}</p>
        ) : (
          <ul className="divide-y rounded-lg border">
            {tokens.map((token) => (
              <li key={token.id} className="flex items-center gap-3 px-3 py-2">
                <div className="grid min-w-0 flex-1">
                  <span className="truncate text-sm font-medium">{token.name}</span>
                  <span className="text-muted-foreground text-xs">
                    {t("tokens.created", { date: token.createdAt ? new Date(Number(token.createdAt.seconds) * 1000).toLocaleDateString(locale) : "" })}
                  </span>
                </div>
                <Button size="sm" variant="destructive" aria-label={t("tokens.revokeNamed", { name: token.name })} disabled={revoking === token.id} onClick={() => void revoke(token.id)}>
                  {revoking === token.id ? <Spinner data-icon="inline-start" /> : <Trash2Icon data-icon="inline-start" />}
                  {t("tokens.revoke")}
                </Button>
              </li>
            ))}
          </ul>
        )}
        <form onSubmit={create} className="flex gap-2">
          <Input name="name" placeholder={t("tokens.newName")} aria-label={t("tokens.newName")} required />
          <Button type="submit" disabled={creating}>
            {creating && <Spinner data-icon="inline-start" />}
            {t("tokens.create")}
          </Button>
        </form>
    </div>
  );
}
