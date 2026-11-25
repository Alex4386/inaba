import { CheckIcon, CopyIcon } from "lucide-react";
import { useTheme } from "next-themes";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

/** The language a fenced block names, from react-markdown's "language-x" class, or "" when it names none. */
export function codeLanguage(className: unknown): string {
  const classes = Array.isArray(className) ? className : String(className ?? "").split(" ");
  const found = classes.find((c) => typeof c === "string" && c.startsWith("language-"));
  return found ? found.slice("language-".length).toLowerCase() : "";
}

// shiki is large, so it loads on the first code block; its codeToHtml keeps one highlighter for the page
// and loads each language's grammar once, the first time it is asked for.
let shiki: Promise<typeof import("shiki")> | undefined;

async function highlight(code: string, lang: string, dark: boolean): Promise<string | undefined> {
  shiki ??= import("shiki");
  const { bundledLanguages, codeToHtml } = await shiki;
  if (!(lang in bundledLanguages)) return undefined;
  // "inline" gives only the coloured spans, so the block keeps our own pre, background and font.
  return codeToHtml(code, { lang, theme: dark ? "github-dark" : "github-light", structure: "inline" });
}

/** A fenced code block: highlighted when its language is known, with its language and a copy button above it. */
export function CodeBlock({ code, lang }: { code: string; lang: string }) {
  const { t } = useI18n();
  const { resolvedTheme } = useTheme();
  const dark = resolvedTheme === "dark";
  // Tagged with its theme so a theme flip shows plain text rather than the old theme's colours until shiki answers.
  const [out, setOut] = useState<{ html?: string; dark: boolean }>();
  const html = out?.dark === dark ? out.html : undefined;
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    let live = true;
    // Until shiki answers, and for unknown languages, the block shows as plain text.
    highlight(code, lang, dark).then(
      (h) => live && setOut({ html: h, dark }),
      () => live && setOut(undefined),
    );
    return () => {
      live = false;
    };
  }, [code, lang, dark]);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 2000);
    return () => clearTimeout(timer);
  }, [copied]);

  const copy = () => navigator.clipboard.writeText(code).then(() => setCopied(true), showError);

  return (
    <div className="not-prose my-3 overflow-hidden rounded-md border bg-muted">
      <div className="flex items-center justify-between border-b px-3 py-1 text-xs text-muted-foreground">
        <span className="font-mono">{lang || "text"}</span>
        <Button variant="ghost" size="xs" onClick={copy} aria-label={copied ? t("common.copied") : t("common.copy")}>
          {copied ? <CheckIcon /> : <CopyIcon />}
          {copied ? t("common.copied") : t("common.copy")}
        </Button>
      </div>
      <pre className="overflow-x-auto p-3 font-mono text-xs leading-relaxed text-foreground">
        {/* shiki escapes the code it colours, so its output is safe to set as HTML. */}
        {html ? <code dangerouslySetInnerHTML={{ __html: html }} /> : <code>{code}</code>}
      </pre>
    </div>
  );
}
