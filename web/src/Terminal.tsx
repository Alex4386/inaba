import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { Terminal } from "@xterm/xterm";
import { useEffect, useRef, useState } from "react";
import type { HostLink } from "./api";
import { Spinner } from "@/components/ui/spinner";
import { inputLine } from "@/modules/browser/input-line";
import { useI18n } from "@/lib/i18n";
import { codeFontStack, terminalFontSize, useCodeFont, useTerminalFontSize } from "@/lib/settings";
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

const joined = (a: Uint8Array, b: Uint8Array) => {
  const out = new Uint8Array(a.length + b.length);
  out.set(a);
  out.set(b, a.length);
  return out;
};

/** How a terminal reaches its process on the host; keep it stable across renders, as a new one attaches again. */
export type TerminalIO = {
  attach: (size: { cols: number; rows: number }, signal: AbortSignal) => AsyncIterable<{ data: Uint8Array; cols: number; rows: number }>;
  write: (data: Uint8Array) => Promise<unknown>;
  resize: (size: { cols: number; rows: number }) => Promise<unknown>;
};

/**
 * A thread's terminal, or with shell set, the shell of that id beside its conversation; or with io set, any
 * other terminal on the host. The thread's driver sizes it to their screen and types into it; anyone else
 * watches it at the driver's size. While pending, the seat may yet be this tab's: keys typed meanwhile wait, and
 * go once it drives, or are dropped if it only watches.
 */
export function TerminalView({ link, threadId = "", shell, io, driving = true, pending = false, onExit, onTitle }: { link: HostLink; threadId?: string; shell?: string; io?: TerminalIO; driving?: boolean; pending?: boolean; onExit: () => void; onTitle?: (title: string) => void }) {
  const el = useRef<HTMLDivElement>(null);
  const { t } = useI18n();
  // The latest callback and wording, read when the process exits rather than when the terminal was made.
  const exit = useRef(onExit);
  exit.current = onExit;
  const title = useRef(onTitle);
  title.current = onTitle;
  const exitedText = useRef("");
  exitedText.current = t("thread.exited");
  const notSentText = useRef("");
  notSentText.current = t("thread.inputNotSent");
  // Output is flowing; until then, or after a break, a spinner says it is still connecting.
  const [attached, setAttached] = useState(false);
  // Read live by the attached terminal: taking or losing the seat changes it in place rather than attaching again.
  const drivingRef = useRef(driving);
  drivingRef.current = driving;
  const pendingRef = useRef(pending);
  pendingRef.current = pending;
  // The code font, changed in settings while the terminal runs: it redraws in place, without attaching again.
  const font = useCodeFont();
  const fontSize = useTerminalFontSize();
  const live = useRef<{ term: Terminal; fit: FitAddon; resize: (size: { cols: number; rows: number }) => void; settle: (ours: boolean) => void }>(undefined);
  useEffect(() => {
    if (!live.current) return;
    live.current.term.options.fontFamily = font;
    live.current.term.options.fontSize = fontSize;
    if (driving) live.current.fit.fit();
  }, [font, fontSize, driving]);
  useEffect(() => {
    if (!live.current) return;
    const { term, fit, resize, settle } = live.current;
    term.options.disableStdin = !driving && !pending;
    term.options.cursorBlink = driving;
    if (!driving) return void (pending || settle(false));
    // The seat is ours: the process takes this screen's size, though fitting may leave it unchanged and fire no resize.
    fit.fit();
    resize({ cols: term.cols, rows: term.rows });
    settle(true);
    term.focus();
  }, [driving, pending]);

  useEffect(() => {
    const term = new Terminal({ fontFamily: codeFontStack(), fontSize: terminalFontSize(), cursorBlink: drivingRef.current, disableStdin: !drivingRef.current && !pendingRef.current, theme: { background: "#0b0b0d" } });
    const fit = new FitAddon();
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

    // When input last failed, so a burst of lost keystrokes leaves one marker rather than one each.
    let failed = 0;
    const to: TerminalIO = io ?? {
      attach: (size, signal) => (shell !== undefined ? link.shell.attachShell({ threadId, shellId: shell, ...size }, { signal }) : link.terminal.attach({ threadId, ...size }, { signal })),
      write: (data) => (shell !== undefined ? link.shell.writeShell({ threadId, shellId: shell, data }) : link.terminal.write({ threadId, data })),
      resize: (size) => (shell !== undefined ? link.shell.resizeShell({ threadId, shellId: shell, ...size }) : link.terminal.resize({ threadId, ...size })),
    };
    // The shell sets its window title as it goes; the host hears it too, but only reports it when asked.
    term.onTitleChange((t) => title.current?.(t));
    // Calls go one at a time, in the order made; keys typed while one is on its way go together as the next, so a
    // slow link costs a round trip per burst rather than per key.
    const send = inputLine<{ data: Uint8Array } | { size: { cols: number; rows: number } }>(
      (call) =>
        ("data" in call ? to.write(call.data) : to.resize(call.size)).catch(() => {
          if (Date.now() - failed < 2_000) return;
          failed = Date.now();
          term.write(`\r\n\x1b[2m[${notSentText.current}]\x1b[0m\r\n`);
        }),
      (a, b) => ("data" in a && "data" in b ? { data: joined(a.data, b.data) } : "size" in a && "size" in b ? b : undefined),
      () => true,
    );
    const resize = (size: { cols: number; rows: number }) => void send({ size });
    // Keys typed before the seat is settled; sent after the size once it is ours, so they land in order.
    let held: Uint8Array | undefined;
    const settle = (ours: boolean) => {
      if (ours && held) void send({ data: held });
      held = undefined;
    };
    live.current = { term, fit, resize, settle };
    term.onData((data) => {
      const bytes = encoder.encode(data);
      if (drivingRef.current) void send({ data: bytes });
      else if (pendingRef.current) held = held ? joined(held, bytes) : bytes;
    });
    // Only the driver's size goes to the process: every viewer sending its own would fight over it.
    term.onResize((size) => drivingRef.current && resize(size));
    const observer = new ResizeObserver(() => drivingRef.current && fit.fit());
    observer.observe(el.current!);
    if (drivingRef.current) fit.fit();
    if (drivingRef.current || pendingRef.current) term.focus();

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
          if (drivingRef.current) {
            await sized();
            fit.fit();
          }
          // Each attach replays the recent output, so start from a clean screen.
          term.reset();
          setAttached(false);
          // A watcher asks for no size, leaving the driver's.
          const size = drivingRef.current ? { cols: term.cols, rows: term.rows } : { cols: 0, rows: 0 };
          for await (const chunk of to.attach(size, abort.signal)) {
            if (!drivingRef.current && chunk.cols > 0 && chunk.rows > 0) term.resize(chunk.cols, chunk.rows);
            term.write(chunk.data);
            setAttached(true);
          }
          if (!abort.signal.aborted) {
            setAttached(true);
            term.write(`\r\n\x1b[2m[${exitedText.current}]\x1b[0m\r\n`);
            exit.current();
          }
          return;
        } catch {
          setAttached(false);
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
    // Not on link.direct: a stream open on the relay keeps flowing, and attaching again would replay it all over the slow link.
  }, [link, threadId, shell, io]);

  // A watcher's terminal is the driver's size, which may not fit here; it scrolls instead.
  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      <div className={cn("min-h-0 flex-1 bg-[#0b0b0d] p-2", !driving && "overflow-auto")} ref={el} title={driving ? undefined : t("drive.spectating")} />
      {!attached && <Spinner className="absolute inset-0 m-auto text-white/60" />}
    </div>
  );
}
