import { timestampDate } from "@bufbuild/protobuf/wkt";
import { CopyIcon, InfoIcon, SlidersHorizontalIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import type { HostLink } from "@/api";
import { Picker } from "@/components/picker";
import { Card, Row, SectionDialog } from "@/components/section-dialog";
import { Button } from "@/components/ui/button";
import { threadWorking } from "@/components/project-tree";
import { type Thread, ThreadMode } from "@/gen/inaba/v1/host_pb";
import { showError } from "@/lib/errors";
import { type Locale, localeNames, useI18n } from "@/lib/i18n";
import { quoted } from "@/lib/utils";

export type ThreadSection = "general" | "session";

/** A value to read and copy: an id, a folder, a command. */
function Copyable({ value }: { value: string }) {
  const { t } = useI18n();
  return (
    <div className="flex min-w-0 max-w-full items-center gap-1">
      <code className="bg-muted min-w-0 truncate rounded px-1.5 py-0.5 text-xs" title={value}>
        {value}
      </code>
      <Button size="icon-xs" variant="ghost" aria-label={t("common.copy")} title={t("common.copy")} onClick={() => navigator.clipboard.writeText(value).then(() => toast.success(t("common.copied")), showError)}>
        <CopyIcon />
      </Button>
    </div>
  );
}

/**
 * A thread's own settings and what it runs on: the language its agent talks in, and the agent session it goes on
 * with, with the command that takes it up at a terminal on the host.
 */
export function ThreadSettingsDialog({
  link,
  thread,
  section,
  onSection,
  onClose,
  driving,
  onThread,
  providerName,
}: {
  link: HostLink;
  thread: Thread;
  section?: ThreadSection;
  onSection: (s: ThreadSection) => void;
  onClose: () => void;
  /** Only the driver changes the thread. */
  driving: boolean;
  onThread: (thread: Thread) => void;
  providerName: string;
}) {
  const { t, locale } = useI18n();
  // Read again as it opens: only GetThread says how to resume the session.
  const [read, setRead] = useState<Thread>();
  const open = section !== undefined;
  useEffect(() => {
    if (open) link.threads.getThread({ threadId: thread.id }).then(setRead, showError);
  }, [link, thread.id, open]);
  // Agents still working in the background end with the session a language change closes.
  const busy = threadWorking(thread);
  const native = read?.nativeSessionId || thread.nativeSessionId;
  const resume = read?.resumeCommand ?? [];
  const when = (ts?: Thread["createdAt"]) => (ts ? timestampDate(ts).toLocaleString(locale) : "—");
  return (
    <SectionDialog
      title={t("threadSettings.title")}
      sections={[
        ["general", t("threadSettings.general"), <SlidersHorizontalIcon />],
        ["session", t("threadSettings.session"), <InfoIcon />],
      ]}
      section={section}
      onSection={onSection}
      onClose={onClose}
    >
      {(current) =>
        current === "general" ? (
          <Card>
            <Row title={t("thread.language")} description={t(busy ? "thread.busyHelp" : "threadSettings.languageHelp")}>
              <Picker
                label={t("thread.language")}
                // The picker takes no empty value: "auto" stands for leaving it to the agent.
                value={thread.language || "auto"}
                disabled={!driving || busy}
                onChange={(l: string) => void link.threads.updateThread({ threadId: thread.id, language: l === "auto" ? "" : l }).then(onThread, showError)}
                options={[
                  { value: "auto", label: t("thread.languageAgent") },
                  ...(Object.keys(localeNames) as Locale[]).map((l) => ({ value: l, label: localeNames[l] })),
                  // One set elsewhere, as by an API call, is shown as itself.
                  ...(thread.language && !(thread.language in localeNames) ? [{ value: thread.language, label: thread.language }] : []),
                ]}
              />
            </Row>
          </Card>
        ) : (
          <Card>
            <Row title={t("threadSettings.provider")} description={t(thread.mode === ThreadMode.TERMINAL ? "threadSettings.inTerminal" : "threadSettings.inChat")}>
              <span className="text-sm">{providerName}</span>
            </Row>
            <Row title={t("threadSettings.nativeSession")} description={t("threadSettings.nativeSessionHelp")}>
              {native ? <Copyable value={native} /> : <span className="text-muted-foreground text-sm">{t("threadSettings.noSession")}</span>}
            </Row>
            {resume.length > 0 && (
              <Row title={t("threadSettings.resume")} description={t("threadSettings.resumeHelp")}>
                <Copyable value={resume.map(quoted).join(" ")} />
              </Row>
            )}
            <Row title={t("threadSettings.folder")} description={t(thread.worktree ? "threadSettings.worktree" : "threadSettings.projectFolder")}>
              <Copyable value={thread.cwd} />
            </Row>
            <Row title={t("threadSettings.id")} description={t("threadSettings.created", { when: when(thread.createdAt) })}>
              <Copyable value={thread.id} />
            </Row>
          </Card>
        )
      }
    </SectionDialog>
  );
}
