import { CodeIcon, NetworkIcon } from "lucide-react";
import { useTheme } from "next-themes";
import { useEffect, useState } from "react";
import { CodeBlock } from "@/components/code-block";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";

// mermaid is large, so it loads on the first diagram.
let mermaid: Promise<typeof import("mermaid")> | undefined;
// render() first deletes any element with its id, so each render needs a fresh one or it blanks the diagram on screen.
let renders = 0;

async function draw(source: string, dark: boolean): Promise<string> {
  mermaid ??= import("mermaid");
  const m = (await mermaid).default;
  // strict keeps the agent's diagram from running scripts or click handlers; settings are global, so set them every time.
  // suppressErrorRendering: otherwise a parse error leaves mermaid's error graphic stuck on document.body.
  m.initialize({ startOnLoad: false, securityLevel: "strict", suppressErrorRendering: true, theme: dark ? "dark" : "default" });
  const { svg } = await m.render(`mermaid-${++renders}`, source);
  return svg;
}

/** A ```mermaid block drawn as a diagram, with a toggle to its source; the source shows when it will not draw. */
export function MermaidBlock({ source }: { source: string }) {
  const { t } = useI18n();
  const { resolvedTheme } = useTheme();
  const dark = resolvedTheme === "dark";
  // Tagged with its theme so a theme flip shows "drawing" rather than the old theme's diagram.
  const [out, setOut] = useState<{ svg: string; dark: boolean }>();
  const svg = out?.dark === dark ? out.svg : undefined;
  const [failed, setFailed] = useState(false);
  const [showSource, setShowSource] = useState(false);

  useEffect(() => {
    let live = true;
    // shortcut: a message still streaming fails to draw until its diagram is whole, so it shows as source meanwhile; debounce if that flicker bothers.
    draw(source, dark).then(
      (s) => {
        if (!live) return;
        setOut({ svg: s, dark });
        setFailed(false);
      },
      () => live && setFailed(true),
    );
    return () => {
      live = false;
    };
  }, [source, dark]);

  if (failed) return <CodeBlock code={source} lang="mermaid" />;
  return (
    <div className="not-prose my-3 overflow-hidden rounded-md border">
      <div className="flex items-center justify-between border-b bg-muted px-3 py-1 text-xs text-muted-foreground">
        <span className="font-mono">mermaid</span>
        <Button variant="ghost" size="xs" onClick={() => setShowSource((s) => !s)} aria-pressed={showSource}>
          {showSource ? <NetworkIcon /> : <CodeIcon />}
          {showSource ? t("markdown.showDiagram") : t("markdown.showSource")}
        </Button>
      </div>
      {showSource ? (
        <pre className="overflow-x-auto bg-muted p-3 font-mono text-xs leading-relaxed text-foreground">
          <code>{source}</code>
        </pre>
      ) : svg ? (
        // securityLevel strict has mermaid sanitize the SVG it returns.
        <div className="flex justify-center overflow-x-auto p-3" dangerouslySetInnerHTML={{ __html: svg }} />
      ) : (
        <div className="p-3 text-xs text-muted-foreground">{t("markdown.drawing")}</div>
      )}
    </div>
  );
}
