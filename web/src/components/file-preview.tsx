import { useMemo } from "react";
import { Markdown } from "@/components/markdown";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { parseDelimited } from "@/lib/csv";
import { useI18n } from "@/lib/i18n";

/** Which rendered view a file has, from its name; undefined when it is only shown as source. */
export function previewKind(path: string): "markdown" | "csv" | "tsv" | undefined {
  const ext = path.split(".").at(-1)?.toLowerCase();
  if (ext === "md" || ext === "markdown") return "markdown";
  if (ext === "csv" || ext === "tsv") return ext;
}

// shortcut: renders at most this many rows without virtualizing, upgrade to a virtual list if bigger tables matter.
const MAX_ROWS = 1000;

/**
 * The folder path a link in the file at `from` points to, or undefined when it is not a file of the folder
 * (a web or mail link, a #fragment, or one climbing above the root). "/x" is taken from the root, as on a forge.
 */
export function linkTarget(from: string, href: string): string | undefined {
  if (/^[a-z][a-z\d+.-]*:/i.test(href) || href.startsWith("//") || href.startsWith("#")) return;
  const bare = href.replace(/[?#].*$/, "");
  if (!bare) return;
  let name = bare;
  try {
    name = decodeURIComponent(bare);
  } catch {
    // A stray % stays as written.
  }
  const out = name.startsWith("/") ? [] : from.split("/").slice(0, -1);
  for (const part of name.split("/")) {
    if (part === "..") {
      if (!out.pop()) return;
    } else if (part && part !== ".") out.push(part);
  }
  return out.length ? out.join("/") : undefined;
}

/** A file rendered as a reader sees it: Markdown as a page, CSV and TSV as a table. */
export function FilePreview({ kind, text, path, onOpen }: { kind: "markdown" | "csv" | "tsv"; text: string; path: string; onOpen: (path: string) => void }) {
  if (kind === "markdown")
    return (
      <div
        className="min-h-0 flex-1 overflow-auto p-4"
        // A relative link would navigate the whole app away and drop unsaved edits: open it here instead.
        // Web links pass through, since Markdown already sends them to a new tab.
        onClickCapture={(e) => {
          const href = (e.target as Element).closest("a")?.getAttribute("href");
          if (href == null || /^(https?|mailto):/i.test(href)) return;
          e.preventDefault();
          const target = linkTarget(path, href);
          if (target) onOpen(target);
        }}
      >
        <Markdown>{text}</Markdown>
      </div>
    );
  return <TablePreview text={text} separator={kind === "csv" ? "," : "\t"} />;
}

function TablePreview({ text, separator }: { text: string; separator: "," | "\t" }) {
  const { t } = useI18n();
  // Blank lines are spacing in the file, not empty records.
  const rows = useMemo(() => parseDelimited(text, separator).filter((r) => r.length > 1 || r[0] !== ""), [text, separator]);
  if (rows.length === 0) return <p className="text-muted-foreground p-4 text-sm">{t("files.emptyTable")}</p>;
  const [head, ...body] = rows;
  return (
    <div className="min-h-0 flex-1 overflow-auto">
      {body.length > MAX_ROWS && <p className="text-muted-foreground border-b px-3 py-1.5 text-xs">{t("files.rowsCapped", { shown: String(MAX_ROWS), total: String(body.length) })}</p>}
      <Table className="text-xs">
        <TableHeader className="bg-muted/50">
          <TableRow>
            {head.map((cell, i) => (
              <TableHead key={i}>{cell}</TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {body.slice(0, MAX_ROWS).map((row, r) => (
            <TableRow key={r}>
              {row.map((cell, i) => (
                <TableCell key={i} className="whitespace-pre">
                  {cell}
                </TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
