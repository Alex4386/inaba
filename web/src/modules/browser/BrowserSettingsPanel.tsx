import { create } from "@bufbuild/protobuf";
import { type FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { onHost } from "@/api";
import { Picker } from "@/components/picker";
import { Card, Row } from "@/components/section-dialog";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { type BrowserProfile, BrowserProfileSchema, BrowserService, type BrowserSettings, BrowserSharing } from "@/gen/inaba/v1/browser_pb";
import { showError } from "@/lib/errors";
import { type MessageKey, useI18n } from "@/lib/i18n";

const sharingLabel: Record<BrowserSharing, MessageKey> = {
  [BrowserSharing.UNSPECIFIED]: "browserSettings.perThread",
  [BrowserSharing.THREAD]: "browserSettings.perThread",
  [BrowserSharing.PROJECT]: "browserSettings.perProject",
};

// Switches as one per line, with or without their dashes; the host writes them back without.
const readFlags = (text: string) =>
  text
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
const showFlags = (flags: string[]) => flags.map((f) => `--${f}`).join("\n");

/** The host's browser settings, read once, and a way to save a change to them. */
function useBrowserSettings(hostId: string) {
  const browser = useMemo(() => onHost(hostId, BrowserService), [hostId]);
  // Undefined while loading; null where the host has no browser module on.
  const [settings, setSettings] = useState<BrowserSettings | null>();
  useEffect(() => {
    browser.getBrowserSettings({}).then(setSettings, () => setSettings(null));
  }, [browser]);
  const save = useCallback((next: BrowserSettings) => browser.saveBrowserSettings(next).then(setSettings), [browser]);
  return { settings, save };
}

/** How the host's browsers start by default: whether a project's threads share one, and Chrome's switches. */
export function BrowserSettingsPanel({ hostId }: { hostId: string }) {
  const { t } = useI18n();
  const { settings, save } = useBrowserSettings(hostId);
  const [sharing, setSharing] = useState(BrowserSharing.UNSPECIFIED);
  const [flags, setFlags] = useState("");
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (!settings) return;
    setSharing(settings.defaults?.sharing ?? BrowserSharing.UNSPECIFIED);
    setFlags(showFlags(settings.defaults?.flags ?? []));
  }, [settings]);
  if (settings === undefined) return <Spinner />;
  if (settings === null) return null;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setSaving(true);
    save({ ...settings, defaults: create(BrowserProfileSchema, { sharing, flags: readFlags(flags) }) })
      .catch(showError)
      .finally(() => setSaving(false));
  };
  return (
    <form onSubmit={submit} className="grid gap-4">
      <p className="text-muted-foreground text-sm">{t("browserSettings.description")}</p>
      <Field>
        <FieldLabel>{t("browserSettings.sharing")}</FieldLabel>
        <Picker
          label={t("browserSettings.sharing")}
          value={String(sharing === BrowserSharing.UNSPECIFIED ? BrowserSharing.THREAD : sharing)}
          onChange={(v) => setSharing(Number(v) as BrowserSharing)}
          options={[BrowserSharing.THREAD, BrowserSharing.PROJECT].map((s) => ({ value: String(s), label: t(sharingLabel[s]) }))}
        />
        <FieldDescription>{t("browserSettings.sharingHelp")}</FieldDescription>
      </Field>
      <Field>
        <FieldLabel htmlFor="browser-flags">{t("browserSettings.flags")}</FieldLabel>
        <Textarea id="browser-flags" className="font-mono text-xs" rows={4} value={flags} onChange={(e) => setFlags(e.target.value)} />
        <FieldDescription>{t("browserSettings.flagsHelp")}</FieldDescription>
      </Field>
      <div>
        <Button type="submit" disabled={saving}>
          {saving && <Spinner data-icon="inline-start" />}
          {t("browserSettings.save")}
        </Button>
      </div>
    </form>
  );
}

/**
 * One project's browser, in its own settings: isolated, its threads share a browser of the project's
 * own, started with switches of its own; otherwise it follows the host's.
 */
export function ProjectBrowser({ hostId, projectId }: { hostId: string; projectId: string }) {
  const { t } = useI18n();
  const { settings, save } = useBrowserSettings(hostId);
  const [flags, setFlags] = useState("");
  const [saving, setSaving] = useState(false);
  const own = settings?.projects[projectId];
  useEffect(() => setFlags(showFlags(own?.flags ?? [])), [own]);
  // No browser module on this host: nothing to set.
  if (!settings) return null;
  const isolated = own?.sharing === BrowserSharing.PROJECT;
  const store = (profile: BrowserProfile | undefined) => {
    const projects = { ...settings.projects };
    if (profile) projects[projectId] = profile;
    else delete projects[projectId];
    setSaving(true);
    return save({ ...settings, projects })
      .catch(showError)
      .finally(() => setSaving(false));
  };
  return (
    <>
      <h3 className="px-1 text-sm font-semibold">{t("browserSettings.title")}</h3>
      <Card>
        <Row title={t("projectBrowser.isolate")} description={t("projectBrowser.isolateHelp")}>
          <Switch
            checked={isolated}
            disabled={saving}
            aria-label={t("projectBrowser.isolate")}
            onCheckedChange={(on) => void store(on ? create(BrowserProfileSchema, { sharing: BrowserSharing.PROJECT, flags: [] }) : undefined)}
          />
        </Row>
        {isolated && (
          <form
            className="grid gap-2 px-4 py-3"
            onSubmit={(e) => {
              e.preventDefault();
              void store(create(BrowserProfileSchema, { sharing: BrowserSharing.PROJECT, flags: readFlags(flags) }));
            }}
          >
            <Field>
              <FieldLabel htmlFor={`project-browser-flags-${projectId}`}>{t("browserSettings.flags")}</FieldLabel>
              <Textarea id={`project-browser-flags-${projectId}`} className="font-mono text-xs" rows={3} value={flags} onChange={(e) => setFlags(e.target.value)} />
              <FieldDescription>{t("projectBrowser.flagsHelp")}</FieldDescription>
            </Field>
            <div>
              <Button type="submit" variant="outline" disabled={saving}>
                {saving && <Spinner data-icon="inline-start" />}
                {t("browserSettings.save")}
              </Button>
            </div>
          </form>
        )}
      </Card>
    </>
  );
}
