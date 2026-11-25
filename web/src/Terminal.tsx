import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { Terminal } from "@xterm/xterm";
import { useEffect, useRef } from "react";
import type { HostLink } from "./api";
import { useI18n } from "@/lib/i18n";
import { codeFontStack, useCodeFont } from "@/lib/settings";
import { cn } from "@/lib/utils";

// shortcut: navigator.platform is deprecated but still set in every browser; switch to userAgentData once Safari and Firefox ship it.
const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);

/**
 * Whether a key press is a clipboard shortcut the browser should handle instead of the terminal. On a Mac only
 * Cmd copies and pastes, so Ctrl+C always interrupts and Ctrl+V stays the literal-next key. Elsewhere Ctrl+C
 * copies only while text is selected, so with nothing selected it still interrupts; Ctrl+V pastes.
 * Letting the browser have them fires its copy and paste events, which xterm already turns into a copy of the
 * selection and a paste into the program, so nothing is handled twice.
 */
export function isClipboardKey(e: Pick<KeyboardEvent, "key" | "ctrlKey" | "metaKey" | "altKey" | "shiftKey">, hasSelection: boolean, mac = isMac) {
  if (e.altKey || e.shiftKey || (mac ? !e.metaKey || e.ctrlKey : !e.ctrlKey || e.metaKey)) return false;
  const key = e.key.toLowerCase();
  return key === "v" || (key === "c" && (mac || hasSelection));
}

/**
 * A thread's terminal, or with shell set, the shell of that id beside its conversation. The thread's
 * driver sizes it to their screen and types into it; anyone else watches it at the driver's size.
 */
export function TerminalView({ link, threadId, shell, driving = true, onExit }: { link: HostLink; threadId: string; shell?: string; driving?: boolean; onExit: () => void }) {
  const el = useRef<HTMLDivElement>(null);
  const { t } = useI18n();
  // The latest callback and wording, read when the process exits rather than when the terminal was made.
  const exit = useRef(onExit);
  exit.current = onExit;
  const exitedText = useRef("");
  exitedText.current = t("thread.exited");
  const direct = link.direct;
  // The code font, changed in settings while the terminal runs: it redraws in place, without attaching again.
  const font = useCodeFont();
  const live = useRef<{ term: Terminal; fit: FitAddon }>(undefined);
  useEffect(() => {
    if (!live.current) return;
    live.current.term.options.fontFamily = font;
    if (driving) live.current.fit.fit();
  }, [font, driving]);

  useEffect(() => {
    const term = new Terminal({ fontFamily: codeFontStack(), fontSize: 13, cursorBlink: driving, disableStdin: !driving, theme: { background: "#0b0b0d" } });
    const fit = new FitAddon();
    live.current = { term, fit };
    term.loadAddon(fit);
    // Links open on Cmd/Ctrl+click, as in VS Code, so a plain click can still select text; xterm underlines them on hover.
    term.loadAddon(
      new WebLinksAddon((event, uri) => {
        if (event.metaKey || event.ctrlKey) window.open(uri, "_blank", "noopener,noreferrer");
      }),
    );
    // A watcher's paste goes nowhere: xterm drops input while stdin is disabled.
    term.attachCustomKeyEventHandler((e) => {
      if (!isClipboardKey(e, term.hasSelection())) return true;
      // Clear after a copy so the next Ctrl+C interrupts; deferred, since the browser's copy event reads the selection after this.
      if (e.type === "keydown" && e.key.toLowerCase() === "c") setTimeout(() => term.clearSelection());
      return false;
    });
    term.open(el.current!);
    const abort = new AbortController();
    const encoder = new TextEncoder();

    // Keystrokes are separate calls; chaining them keeps them in the order typed.
    let queue = Promise.resolve();
    const enqueue = (call: () => Promise<unknown>) => {
      queue = queue.then(call).then(
        () => {},
        () => {},
      );
    };
    term.onData((data) => enqueue(() => (shell !== undefined ? link.shell.writeShell({ threadId, shellId: shell, data: encoder.encode(data) }) : link.terminal.write({ threadId, data: encoder.encode(data) }))));
    // Only the driver's size goes to the process: every viewer sending its own would fight over it.
    if (driving) term.onResize(({ cols, rows }) => enqueue(() => (shell !== undefined ? link.shell.resizeShell({ threadId, shellId: shell, cols, rows }) : link.terminal.resize({ threadId, cols, rows }))));
    const observer = new ResizeObserver(() => driving && fit.fit());
    observer.observe(el.current!);
    if (driving) {
      fit.fit();
      term.focus();
    }

    // A hidden tab has no size to fit; wait until it shows, so the program starts at the width it is seen at.
    const sized = () =>
      new Promise<void>((resolve) => {
        if (fit.proposeDimensions()) return resolve();
        const watch = new ResizeObserver(() => {
          if (fit.proposeDimensions()) {
            watch.disconnect();
            resolve();
          }
        });
        watch.observe(el.current!);
        abort.signal.addEventListener("abort", () => (watch.disconnect(), resolve()));
      });

    void (async () => {
      while (!abort.signal.aborted) {
        try {
          if (driving) {
            await sized();
            fit.fit();
          }
          // Each attach replays the recent output, so start from a clean screen.
          term.reset();
          // A watcher asks for no size, leaving the driver's.
          const size = driving ? { cols: term.cols, rows: term.rows } : { cols: 0, rows: 0 };
          for await (const chunk of shell !== undefined
            ? link.shell.attachShell({ threadId, shellId: shell, ...size }, { signal: abort.signal })
            : link.terminal.attach({ threadId, ...size }, { signal: abort.signal }))
          {
            if (!driving && chunk.cols > 0 && chunk.rows > 0) term.resize(chunk.cols, chunk.rows);
            term.write(chunk.data);
          }
          if (!abort.signal.aborted) {
            term.write(`\r\n\x1b[2m[${exitedText.current}]\x1b[0m\r\n`);
            exit.current();
          }
          return;
        } catch {
          await new Promise((r) => setTimeout(r, 1_000));
        }
      }
    })();

    return () => {
      abort.abort();
      observer.disconnect();
      live.current = undefined;
      term.dispose();
    };
  }, [link, threadId, shell, direct, driving]);

  // A watcher's terminal is the driver's size, which may not fit here; it scrolls instead.
  return <div className={cn("min-h-0 flex-1 bg-[#0b0b0d] p-2", !driving && "overflow-auto")} ref={el} title={driving ? undefined : t("drive.spectating")} />;
}
