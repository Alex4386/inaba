import { type ChangeTypes, type FileDiffMetadata, parsePatchFiles } from "@pierre/diffs";
import { FileDiff } from "@pierre/diffs/react";
import { ChevronDownIcon, ChevronRightIcon, CopyIcon } from "lucide-react";
import { useTheme } from "next-themes";
import { useCallback, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { LineCounts } from "@/components/changes-panel";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { isTestPath } from "@/lib/diff-files";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** One path whole before and after the change, as GetDiff gives it for a single path. */
export type DiffSides = { sides: boolean; before: string; after: string };

// The letter git status uses for each kind of change, in the color diffs use for it.
const STATUS: Record<ChangeTypes, [string, string]> = {
  change: ["M", "text-muted-foreground"],
  new: ["A", "text-success"],
  deleted: ["D", "text-destructive"],
  "rename-pure": ["R", "text-muted-foreground"],
  "rename-changed": ["R", "text-muted-foreground"],
};

const count = (file: FileDiffMetadata, key: "additionLines" | "deletionLines") => file.hunks.reduce((sum, h) => sum + h[key], 0);

/**
 * Every file of a unified diff, highlighted, one under another, each with a header that folds it,
 * under a list of the files to jump to. Loaded on its own, since the highlighter is large.
 */
export default function DiffView({
  patch,
  split,
  wrap,
  folded,
  onFold,
  hideTests,
  loadSides,
}: {
  patch: string;
  split: boolean;
  wrap: boolean;
  folded: Set<string>;
  onFold: (name: string) => void;
  hideTests: boolean;
  /** Reads one path whole, so the context around its hunks can be expanded. */
  loadSides: (path: string) => Promise<DiffSides>;
}) {
  const { t } = useI18n();
  const { resolvedTheme } = useTheme();
  const all = useMemo(() => parsePatchFiles(patch).flatMap((p) => p.files), [patch]);
  const files = hideTests ? all.filter((f) => !isTestPath(f.name)) : all;
  const [listed, setListed] = useState(true);
  const items = useRef(new Map<string, HTMLDivElement>());
  // The whole file is read only when someone expands context, since most diffs are read as they are.
  const loadDiffFiles = useCallback(
    async (file: FileDiffMetadata) => {
      const r = await loadSides(file.name);
      if (!r.sides) {
        toast.error(t("git.noContext"));
        throw new Error("no sides for " + file.name);
      }
      return { oldFile: { name: file.name, contents: r.before }, newFile: { name: file.name, contents: r.after } };
    },
    [loadSides, t],
  );
  const jump = (name: string) => {
    if (folded.has(name)) onFold(name);
    items.current.get(name)?.scrollIntoView({ block: "start" });
  };
  return (
    <div className="grid gap-2 p-2">
      <Collapsible open={listed} onOpenChange={setListed} className="rounded-md border">
        <CollapsibleTrigger className="text-muted-foreground hover:text-foreground flex w-full items-center gap-1 px-2 py-1.5 text-xs font-medium">
          {listed ? <ChevronDownIcon className="size-4" /> : <ChevronRightIcon className="size-4" />}
          {t("git.files", { count: String(files.length) })}
          {all.length > files.length && <span className="font-normal">· {t("git.testsHidden", { count: String(all.length - files.length) })}</span>}
        </CollapsibleTrigger>
        <CollapsibleContent>
          <ul className="max-h-60 overflow-auto border-t py-1">
            {files.map((file) => {
              const [letter, color] = STATUS[file.type];
              return (
                <li key={file.name}>
                  <button type="button" className="hover:bg-muted flex w-full items-center gap-2 px-2 py-0.5 text-left text-xs" title={file.prevName ? `${file.prevName} → ${file.name}` : file.name} onClick={() => jump(file.name)}>
                    <span className={cn("w-3 shrink-0 font-mono", color)}>{letter}</span>
                    <span className="min-w-0 flex-1 truncate font-mono">{file.name}</span>
                    <LineCounts additions={count(file, "additionLines")} deletions={count(file, "deletionLines")} />
                  </button>
                </li>
              );
            })}
          </ul>
        </CollapsibleContent>
      </Collapsible>
      {files.map((file) => {
        const closed = folded.has(file.name);
        return (
          <div
            key={file.name}
            className="scroll-mt-2"
            ref={(el) => {
              if (el) items.current.set(file.name, el);
              else items.current.delete(file.name);
            }}
          >
            <FileDiff
              fileDiff={file}
              className="overflow-hidden rounded-md border"
              disableWorkerPool
              options={{
                diffStyle: split ? "split" : "unified",
                overflow: wrap ? "wrap" : "scroll",
                themeType: resolvedTheme === "dark" ? "dark" : "light",
                theme: { dark: "pierre-dark", light: "pierre-light" },
                collapsed: closed,
                stickyHeader: true,
                lineDiffType: "word",
                // shortcut: a renamed file's before is read at its new path and so comes back empty; renames keep the patch's context only, until GetDiff takes the old path too.
                loadDiffFiles: file.type === "change" ? loadDiffFiles : undefined,
              }}
              renderHeaderPrefix={() => (
                <button type="button" className="text-muted-foreground hover:text-foreground flex items-center" aria-expanded={!closed} aria-label={file.name} onClick={() => onFold(file.name)}>
                  {closed ? <ChevronRightIcon className="size-4" /> : <ChevronDownIcon className="size-4" />}
                </button>
              )}
              renderHeaderMetadata={() => (
                <Button size="icon-xs" variant="ghost" aria-label={t("git.copyPath")} title={t("git.copyPath")} onClick={() => navigator.clipboard.writeText(file.name).then(() => toast.success(t("common.copied")), showError)}>
                  <CopyIcon />
                </Button>
              )}
            />
          </div>
        );
      })}
    </div>
  );
}
