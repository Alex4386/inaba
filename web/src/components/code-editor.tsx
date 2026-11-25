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
import { useRef } from "react";
import { useCodeFont } from "@/lib/settings";

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

/** A file in Monaco: highlighted by its name's language, editable unless readOnly, ⌘S saving. */
export default function CodeEditor({ path, value, readOnly, onChange, onSave }: { path: string; value: string; readOnly?: boolean; onChange: (value: string) => void; onSave: () => void }) {
  const { resolvedTheme } = useTheme();
  const fontFamily = useCodeFont();
  // The latest save, for the key binding Monaco keeps from the first render.
  const save = useRef(onSave);
  save.current = onSave;
  const mount: OnMount = (editor, m) => {
    editor.addCommand(m.KeyMod.CtrlCmd | m.KeyCode.KeyS, () => save.current());
  };
  return (
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
      }}
    />
  );
}
