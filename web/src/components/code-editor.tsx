// Monaco, bundled with the app rather than fetched from a CDN, and loaded only when a
// file is opened: this module is its own chunk.
import Editor, { loader, type OnMount } from "@monaco-editor/react";
import * as monaco from "monaco-editor";
import editorWorker from "monaco-editor/editor/editor.worker?worker";
import cssWorker from "monaco-editor/language/css/css.worker?worker";
import htmlWorker from "monaco-editor/language/html/html.worker?worker";
import jsonWorker from "monaco-editor/language/json/json.worker?worker";
import tsWorker from "monaco-editor/language/typescript/ts.worker?worker";
import { useTheme } from "next-themes";
import { WrapTextIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";
import { setWordWrap, useCodeFont, useWordWrap } from "@/lib/settings";
import { cn } from "@/lib/utils";

self.MonacoEnvironment = {
  getWorker(_, label) {
    if (label === "json") return new jsonWorker();
    if (label === "css" || label === "scss" || label === "less") return new cssWorker();
    if (label === "html" || label === "handlebars" || label === "razor") return new htmlWorker();
    if (label === "typescript" || label === "javascript") return new tsWorker();
    return new editorWorker();
  },
};
loader.config({ monaco });

/** Where to put the cursor and scroll to: a new object each time, so the same place can be shown again. */
export type Reveal = { line: number; column: number; length: number };

/** A file in Monaco: highlighted by its name's language, editable unless readOnly, ⌘S saving; a status bar under it. */
export default function CodeEditor({ path, value, readOnly, onChange, onSave, reveal }: { path: string; value: string; readOnly?: boolean; onChange: (value: string) => void; onSave: () => void; reveal?: Reveal }) {
  const { t } = useI18n();
  const { resolvedTheme } = useTheme();
  const wrap = useWordWrap();
  const [pos, setPos] = useState({ line: 1, column: 1 });
  const [language, setLanguage] = useState("");
  const editor = useRef<monaco.editor.IStandaloneCodeEditor>(null);
  // The reveal asked for before the editor mounted is shown once it does.
  const pending = useRef(reveal);
  pending.current = reveal;
  useEffect(() => {
    if (editor.current && reveal) show(editor.current, reveal);
  }, [reveal]);
  const fontFamily = useCodeFont();
  // The latest save, for the key binding Monaco keeps from the first render.
  const save = useRef(onSave);
  save.current = onSave;
  const mount: OnMount = (ed, m) => {
    // An action, not addCommand: Monaco's commands are global, so with several editors open only the last one's would run.
    ed.addAction({ id: "inaba.save", label: "Save", keybindings: [m.KeyMod.CtrlCmd | m.KeyCode.KeyS], run: () => save.current() });
    editor.current = ed;
    setLanguage(ed.getModel()?.getLanguageId() ?? "");
    ed.onDidChangeCursorPosition((e) => setPos({ line: e.position.lineNumber, column: e.position.column }));
    if (pending.current) show(ed, pending.current);
  };
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="min-h-0 flex-1">
        <Editor
          // The path picks the language from its extension, and keeps each file's own undo history.
          path={path}
          value={value}
          theme={resolvedTheme === "dark" ? "vs-dark" : "vs"}
          onChange={(v) => onChange(v ?? "")}
          onMount={mount}
          options={{
            readOnly,
            fontSize: 13,
            fontFamily,
            minimap: { enabled: false },
            scrollBeyondLastLine: false,
            automaticLayout: true,
            renderWhitespace: "selection",
            tabSize: 2,
            wordWrap: wrap ? "on" : "off",
          }}
        />
      </div>
      <div className="text-muted-foreground flex h-6 shrink-0 items-center gap-3 border-t px-3 text-xs tabular-nums">
        <span>{t("files.position", { line: String(pos.line), column: String(pos.column) })}</span>
        <span>{language}</span>
        <Button size="icon-xs" variant="ghost" className={cn("ml-auto", wrap && "bg-muted text-foreground")} aria-pressed={wrap} aria-label={t("files.wordWrap")} title={t("files.wordWrap")} onClick={() => setWordWrap(!wrap)}>
          <WrapTextIcon />
        </Button>
      </div>
    </div>
  );
}

// Each reveal is shown once, so an editor mounted again (a reload) keeps the user's place.
const shown = new WeakSet<Reveal>();

function show(editor: monaco.editor.IStandaloneCodeEditor, at: Reveal) {
  if (shown.has(at)) return;
  shown.add(at);
  const range = new monaco.Range(at.line, at.column, at.line, at.column + at.length);
  editor.setSelection(range);
  editor.revealRangeInCenterIfOutsideViewport(range);
}
