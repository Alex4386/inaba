import { PuzzleIcon } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { onHost } from "@/api";
import { Card } from "@/components/section-dialog";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { type Module, ModuleService } from "@/gen/inaba/v1/host_pb";
import { errorText, showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

/** A host's optional parts, turned on and off while it runs. */
export function ModulesPanel({ hostId, onChanged }: { hostId: string; onChanged: () => void }) {
  const { t } = useI18n();
  const client = useMemo(() => onHost(hostId, ModuleService), [hostId]);
  const [modules, setModules] = useState<Module[]>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");

  const load = useCallback(() => {
    setError("");
    client.listModules({}).then(
      (r) => setModules(r.modules),
      (e) => setError(errorText(e)),
    );
  }, [client]);
  useEffect(() => {
    setModules(undefined);
    load();
  }, [load]);

  const set = (m: Module, enabled: boolean) => {
    setBusy(m.name);
    client
      ?.setModule({ name: m.name, enabled })
      .then((next) => {
        setModules((now) => now?.map((x) => (x.name === next.name ? next : x)));
        if (next.error) showError(new Error(next.error));
        onChanged();
      }, showError)
      .finally(() => setBusy(""));
  };

  return (
    <div className="grid gap-4">
      <p className="text-muted-foreground text-sm">{t("modules.description")}</p>
      {error ? (
        <p className="text-destructive text-sm">{error}</p>
      ) : !modules ? (
        <Spinner />
      ) : modules.length === 0 ? (
        <p className="text-muted-foreground text-sm">{t("modules.none")}</p>
      ) : (
        <Card>
          {modules.map((m) => (
            <div key={m.name} className="flex items-start gap-3 px-4 py-3">
              <PuzzleIcon className="text-muted-foreground mt-0.5 size-4 shrink-0" />
              <div className="grid min-w-0 flex-1 gap-0.5">
                <span className="text-sm font-medium">{m.title || m.name}</span>
                <span className="text-muted-foreground text-xs">{m.description}</span>
                {m.error && <span className="text-destructive text-xs">{t("modules.failed", { error: m.error })}</span>}
              </div>
              {busy === m.name && <Spinner className="mt-0.5" />}
              <Switch checked={m.enabled} disabled={busy === m.name} aria-label={m.title || m.name} onCheckedChange={(on) => set(m, on)} />
            </div>
          ))}
        </Card>
      )}
    </div>
  );
}
