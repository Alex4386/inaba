import { ArrowLeftIcon, ArrowRightIcon, MonitorIcon, RotateCwIcon, ScanIcon } from "lucide-react";
import { type MouseEvent, type WheelEvent, useCallback, useEffect, useRef, useState } from "react";
import type { HostLink } from "@/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import type { BrowserPage } from "@/gen/inaba/v1/browser_pb";
import { errorText, showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { type Heard, modifierBits, translator } from "./ime";
import { inputLine } from "./input-line";

const buttons = ["left", "middle", "right"];
// What one Input call carries to the page: a mouse event, a key, text or a composition.
type PageInput = NonNullable<Parameters<HostLink["browser"]["input"]>[0]["event"]>;

// The window a desktop page expects, for pages that will not fit a narrow panel.
const desktop = { width: 1280, height: 800 };
const FIT_KEY = "inaba.browser.fit";

const readFit = () => {
  try {
    return localStorage.getItem(FIT_KEY) !== "desktop";
  } catch {
    return true;
  }
};

/** The thread's browser: what its agent sees and does, to watch and to use. */
export function BrowserPanel({
  link,
  threadId,
  page: bound,
  taken,
  onBind,
  onGone,
  onPages,
  onTitle,
  driving = true,
}: {
  link: HostLink;
  threadId: string;
  /** The page of the thread's browser this tab shows; each tab is one page. */
  page?: string;
  /** Pages other tabs show, which this one does not take. */
  taken: string[];
  /** Ties this tab to the page it took or opened. */
  onBind: (pageId: string) => void;
  /** Its page was closed, as by the agent: the tab goes with it. */
  onGone: () => void;
  /** The pages there are, so any the agent opened get tabs of their own. */
  onPages: (pageIds: string[]) => void;
  /** What the page shown is called now, for the tab: its title, else its address. */
  onTitle?: (title: string | undefined) => void;
  /** This browser holds the thread's driver seat: its size is the window's, and its hands are on the page. Others watch. */
  driving?: boolean;
}) {
  const { t } = useI18n();
  const [pages, setPages] = useState<BrowserPage[]>();
  const [pageId, setPageId] = useState("");
  const shownPage = pages?.find((p) => p.id === pageId);
  const title = shownPage && (shownPage.title || shownPage.url);
  useEffect(() => {
    onTitle?.(title);
    // Only when the name changes; the callback is new each render.
  }, [title]);
  const [address, setAddress] = useState("");
  const editing = useRef(false);
  const [image, setImage] = useState<string>();
  const [error, setError] = useState("");
  // The browser's window follows the panel, or stays the size of a desktop one.
  const [fit, setFit] = useState(readFit);
  const [area, setArea] = useState<{ width: number; height: number }>();
  // The page's viewport in CSS pixels, to aim input by.
  const viewport = useRef({ width: 0, height: 0 });
  const screen = useRef<HTMLDivElement>(null);
  const img = useRef<HTMLImageElement>(null);
  // Where keys are caught: put where the page was last clicked, so an input method shows its candidates there.
  const catcher = useRef<HTMLTextAreaElement>(null);
  const [caret, setCaret] = useState({ x: 0, y: 0 });
  const translate = useRef(translator());

  // The latest of what the dock says, read when the browser answers rather than when the call was made.
  const ties = useRef({ bound, taken, onBind, onGone, onPages, driving });
  ties.current = { bound, taken, onBind, onGone, onPages, driving };
  // A tab with no page takes one no tab shows, or opens one, once.
  const claiming = useRef(false);
  const refresh = useCallback(
    () =>
      link.browser.openBrowser({ threadId }).then(
        async (st) => {
          setError("");
          let pages = st.pages;
          const { bound, taken, onBind, onGone, onPages, driving } = ties.current;
          let mine = bound;
          if (mine && !pages.some((p) => p.id === mine)) return onGone();
          // Only the driver finds a tab its page, so watchers do not each open one.
          if (!mine && driving && !claiming.current) {
            claiming.current = true;
            mine = pages.find((p) => !taken.includes(p.id))?.id;
            if (!mine) {
              const opened = await link.browser.navigate({ threadId, url: "about:blank", newPage: true });
              pages = opened.pages;
              mine = opened.opened;
            }
            if (mine) onBind(mine);
          }
          setPages(pages);
          if (mine) setPageId(mine);
          onPages(pages.map((p) => p.id));
        },
        (e) => setError(errorText(e)),
      ),
    [link, threadId],
  );
  useEffect(() => {
    void refresh();
    // The agent opens and closes pages on its own; look again now and then.
    const timer = setInterval(() => void refresh(), 3000);
    return () => clearInterval(timer);
  }, [refresh]);

  // The space the page is shown in, settled: a resize mid-drag would restart the picture each step.
  useEffect(() => {
    const el = screen.current;
    if (!el) return;
    let timer: ReturnType<typeof setTimeout>;
    const observer = new ResizeObserver(() => {
      clearTimeout(timer);
      timer = setTimeout(() => setArea({ width: Math.floor(el.clientWidth), height: Math.floor(el.clientHeight) }), 300);
    });
    observer.observe(el);
    return () => {
      clearTimeout(timer);
      observer.disconnect();
    };
  }, []);

  // Watch the chosen page at the window size wanted, coming back after the stream breaks.
  useEffect(() => {
    if (!pageId || !area) return;
    const size = fit ? area : desktop;
    const abort = new AbortController();
    let shown: string | undefined;
    void (async () => {
      while (!abort.signal.aborted) {
        try {
          const stream = link.browser.watchPage(
            // A watcher leaves the window at the driver's size.
            { threadId, pageId, windowWidth: driving ? size.width : 0, windowHeight: driving ? size.height : 0, maxWidth: Math.round(area.width * devicePixelRatio) },
            { signal: abort.signal },
          );
          for await (const frame of stream) {
            viewport.current = { width: frame.viewportWidth, height: frame.viewportHeight };
            const next = URL.createObjectURL(new Blob([frame.jpeg as BlobPart], { type: "image/jpeg" }));
            setImage(next);
            if (shown) URL.revokeObjectURL(shown);
            shown = next;
            if (!editing.current) setAddress(frame.url);
          }
        } catch {
          // Retried below.
        }
        if (abort.signal.aborted) break;
        await refresh();
        await new Promise((r) => setTimeout(r, 1000));
      }
    })();
    return () => {
      abort.abort();
      if (shown) URL.revokeObjectURL(shown);
    };
  }, [link, threadId, pageId, refresh, fit, area, driving]);

  // Inputs are separate calls; chained, they reach the page in the order they were made.
  const line = useRef<(event: PageInput) => Promise<unknown>>(undefined);
  line.current ??= inputLine<PageInput>(
    (event) => ids.current.link.browser.input({ threadId: ids.current.threadId, pageId: ids.current.pageId, event }),
    // Only the latest position of a move counts; a scroll adds up.
    (a, b) =>
      a.case !== "mouse" || b.case !== "mouse" || a.value.type !== b.value.type
        ? undefined
        : b.value.type === "moved"
          ? b
          : b.value.type === "wheel"
            ? { case: "mouse", value: { ...b.value, deltaX: (a.value.deltaX ?? 0) + (b.value.deltaX ?? 0), deltaY: (a.value.deltaY ?? 0) + (b.value.deltaY ?? 0) } }
            : undefined,
    (e) => e.case === "mouse" && (e.value.type === "moved" || e.value.type === "wheel"),
  );
  // The line outlives renders; it posts to the page shown now.
  const ids = useRef({ link, threadId, pageId });
  ids.current = { link, threadId, pageId };
  const send = (event: PageInput) => (driving ? line.current!(event) : Promise.resolve());
  /** Passes what the catcher heard to the page, and says whether a key went. */
  const heard = (e: Heard) => {
    const out = translate.current(e);
    for (const i of out) void send(i);
    return out.some((i) => i.case === "key");
  };

  /** Where on the page a point of the picture is, in the page's CSS pixels. */
  const at = (e: MouseEvent | WheelEvent) => {
    const box = img.current!.getBoundingClientRect();
    return { x: ((e.clientX - box.left) / box.width) * viewport.current.width, y: ((e.clientY - box.top) / box.height) * viewport.current.height };
  };
  const mouse = (type: string, e: MouseEvent) => {
    const { x, y } = at(e);
    void send({ case: "mouse", value: { type, x, y, button: type === "moved" ? "" : buttons[e.button], clicks: type === "moved" ? 0 : e.detail || 1, modifiers: modifierBits(e), deltaX: 0, deltaY: 0 } });
  };
  // Moves matter while a button is held, for dragging and selecting; otherwise a few are enough for hovering.
  const lastMove = useRef(0);
  const onMove = (e: MouseEvent) => {
    const now = performance.now();
    if (e.buttons === 0 && now - lastMove.current < 100) return;
    lastMove.current = now;
    mouse("moved", e);
  };
  const onWheel = (e: WheelEvent) => {
    const { x, y } = at(e);
    void send({ case: "mouse", value: { type: "wheel", x, y, button: "", clicks: 0, modifiers: modifierBits(e), deltaX: e.deltaX, deltaY: e.deltaY } });
  };
  const onPress = (e: MouseEvent<HTMLImageElement>) => {
    // Keep the keys with the catcher, and move it to where the page was clicked.
    e.preventDefault();
    const box = screen.current!.getBoundingClientRect();
    setCaret({ x: e.clientX - box.left, y: e.clientY - box.top });
    catcher.current?.focus();
    mouse("pressed", e);
  };

  const [navigating, setNavigating] = useState(false);
  const go = (request: { url?: string; action?: string; newPage?: boolean }) => {
    setNavigating(true);
    return link.browser
      .navigate({ threadId, pageId: request.newPage ? "" : pageId, url: request.url ?? "", action: request.action ?? "", newPage: request.newPage ?? false })
      .then((st) => {
        setPages(st.pages);
        if (request.newPage) setPageId(st.pages.at(-1)?.id ?? "");
      }, showError)
      .finally(() => setNavigating(false));
  };
  const submit = () => {
    // A bare address is https, except on this machine or a local network, where dev servers speak plain http.
    const local = /^(localhost|\[[0-9a-f:]+\]|\d{1,3}(\.\d{1,3}){3})(:\d+)?(\/|$)/i.test(address);
    const url = /^[a-z][a-z0-9+.-]*:\/\//i.test(address) || address.startsWith("about:") ? address : `${local ? "http" : "https"}://${address}`;
    editing.current = false;
    void go({ url });
  };
  const toggleFit = () =>
    setFit((now) => {
      try {
        localStorage.setItem(FIT_KEY, now ? "desktop" : "fit");
      } catch {
        // Remembered for this panel only.
      }
      return !now;
    });

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex h-12 shrink-0 items-center gap-1 border-b px-2">
        {/* A watcher sees the controls but cannot use them until they take over. */}
        <fieldset disabled={!driving} className="contents">
        <Button size="icon-sm" variant="ghost" aria-label={t("browser.back")} title={t("browser.back")} onClick={() => void go({ action: "back" })}>
          <ArrowLeftIcon />
        </Button>
        <Button size="icon-sm" variant="ghost" aria-label={t("browser.forward")} title={t("browser.forward")} onClick={() => void go({ action: "forward" })}>
          <ArrowRightIcon />
        </Button>
        <Button size="icon-sm" variant="ghost" aria-label={t("browser.reload")} title={t("browser.reload")} onClick={() => void go({ action: "reload" })}>
          {navigating ? <Spinner /> : <RotateCwIcon />}
        </Button>
        <form
          className="min-w-0 flex-1"
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <Input
            className="h-8 font-mono text-xs"
            value={address}
            aria-label={t("browser.address")}
            placeholder={t("browser.addressHint")}
            onFocus={(e) => ((editing.current = true), e.currentTarget.select())}
            onBlur={() => (editing.current = false)}
            onChange={(e) => setAddress(e.target.value)}
          />
        </form>
        <Button
          size="icon-sm"
          variant={fit ? "ghost" : "secondary"}
          aria-pressed={!fit}
          aria-label={t(fit ? "browser.desktopSize" : "browser.fitSize")}
          title={t(fit ? "browser.desktopSize" : "browser.fitSize")}
          onClick={toggleFit}
        >
          {fit ? <MonitorIcon /> : <ScanIcon />}
        </Button>
        </fieldset>
      </header>
      <div ref={screen} className="bg-muted/40 relative flex min-h-0 flex-1 items-start justify-center overflow-hidden">
        {error ? (
          <p className="text-destructive m-auto max-w-sm p-4 text-center text-sm">{error}</p>
        ) : !image ? (
          <Spinner className="m-auto" />
        ) : (
          // The page itself is not here, only its picture; the mouse acts on it here, and keys go to the catcher.
          <img
            ref={img}
            src={image}
            alt={t("browser.screen")}
            draggable={false}
            className="block max-h-full max-w-full cursor-default object-contain select-none"
            title={driving ? undefined : t("drive.spectating")}
            onMouseDown={(e) => driving && onPress(e)}
            onMouseUp={(e) => mouse("released", e)}
            onMouseMove={onMove}
            onWheel={onWheel}
            onContextMenu={(e) => e.preventDefault()}
          />
        )}
        <textarea
          ref={catcher}
          aria-label={t("browser.keyboard")}
          autoCapitalize="off"
          autoComplete="off"
          autoCorrect="off"
          spellCheck={false}
          className="pointer-events-none absolute h-5 w-px resize-none overflow-hidden border-0 p-0 opacity-0"
          style={{ left: caret.x, top: caret.y }}
          onKeyDown={(e) => {
            if (heard({ type: "keydown", key: e.key, code: e.code, keyCode: e.keyCode, isComposing: e.nativeEvent.isComposing, altKey: e.altKey, ctrlKey: e.ctrlKey, metaKey: e.metaKey, shiftKey: e.shiftKey })) e.preventDefault();
          }}
          onKeyUp={(e) => {
            heard({ type: "keyup", key: e.key, code: e.code, keyCode: e.keyCode, isComposing: e.nativeEvent.isComposing, altKey: e.altKey, ctrlKey: e.ctrlKey, metaKey: e.metaKey, shiftKey: e.shiftKey });
          }}
          onCompositionUpdate={(e) => heard({ type: "compositionupdate", data: e.data })}
          onCompositionEnd={(e) => {
            heard({ type: "compositionend", data: e.data });
            e.currentTarget.value = "";
          }}
          onInput={(e) => {
            const input = e.nativeEvent as InputEvent;
            heard({ type: "input", inputType: input.inputType, data: input.data, isComposing: input.isComposing });
            // What was typed is on the page now; the catcher only holds what is still being composed.
            if (!input.isComposing) e.currentTarget.value = "";
          }}
          onPaste={(e) => {
            e.preventDefault();
            void send({ case: "text", value: e.clipboardData.getData("text") });
          }}
        />
      </div>
    </div>
  );
}
