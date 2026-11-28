import { type PointerEvent, type RefObject, useEffect, useRef, useState } from "react";
import { useIsMobile } from "@/hooks/use-mobile";
import { useI18n } from "@/lib/i18n";
import { useChatMap } from "@/lib/settings";
import { cn } from "@/lib/utils";

/** A message of the user's and the start of the answer to it. seq matches the row's data-seq. */
export type Exchange = { seq: string; text: string; answer: string };

// Ticks closer than this are too thin to tell apart or point at.
const TICK = 10;

/** Which tick of count an exchange falls on, when there are more exchanges than ticks. */
export const tickOf = (i: number, exchanges: number, count: number) => Math.floor((i * count) / exchanges);

/**
 * Ticks at the chat's left edge to jump or scrub through a long conversation: one per exchange, or as many as
 * the chat's height has room for, each then standing for a stretch of them. reach reads in the turns above what
 * is read so far, back to the given message, for a jump to one not shown yet.
 */
export function ChatMap({ exchanges, scroller, reach }: { exchanges: Exchange[]; scroller: RefObject<HTMLElement | null>; reach?: (seq: string) => Promise<void> }) {
  const { t } = useI18n();
  const on = useChatMap();
  const isMobile = useIsMobile();
  const [active, setActive] = useState(0);
  const [hover, setHover] = useState<number>();
  const dragging = useRef(false);
  const ticks = useRef<HTMLElement>(null);
  // The tick last jumped to and where that left the scroll, so the bottom rule below does not override a click.
  const pinned = useRef<{ i: number; top: number }>(undefined);
  // The exchange last asked for, for a jump that waits on reading it in to give way to a later one.
  const wanted = useRef<number>(undefined);
  const [room, setRoom] = useState(0);
  const shown = on && !isMobile && exchanges.length >= 2;
  const index = useRef(new Map<string, number>());
  index.current = new Map(exchanges.map((x, i) => [x.seq, i]));
  // The strip is at most 70% of the chat's height.
  const count = Math.min(exchanges.length, Math.max(2, Math.floor((room * 0.7 - 8) / TICK)));

  useEffect(() => {
    const box = scroller.current?.parentElement;
    if (!box || !shown) return;
    const sized = new ResizeObserver(() => setRoom(box.clientHeight));
    sized.observe(box);
    return () => sized.disconnect();
  }, [scroller, shown]);

  // The exchange in view: the last one whose message starts above a third of the way down. Measured once a frame at most.
  useEffect(() => {
    const el = scroller.current;
    if (!el || !shown) return;
    let frame = 0;
    const measure = () => {
      frame = 0;
      const line = el.getBoundingClientRect().top + el.clientHeight / 3;
      const rows = el.querySelectorAll<HTMLElement>("[data-seq]");
      // Rows are only what is read in: each is found among all exchanges by its seq.
      const at$ = (row: Element | undefined) => index.current.get((row as HTMLElement | undefined)?.dataset.seq ?? "") ?? 0;
      let at = at$(rows[0]);
      rows.forEach((row) => {
        if (row.getBoundingClientRect().top <= line) at = at$(row);
      });
      // Scrolled to the end, a short last exchange never reaches the line.
      if (el.scrollHeight - el.scrollTop - el.clientHeight < 4) at = at$(rows[rows.length - 1]);
      if (pinned.current && Math.abs(pinned.current.top - el.scrollTop) < 2) at = pinned.current.i;
      else pinned.current = undefined;
      setActive(Math.max(at, 0));
    };
    const onScroll = () => {
      frame ||= requestAnimationFrame(measure);
    };
    measure();
    el.addEventListener("scroll", onScroll, { passive: true });
    // Rows also move without a scroll: a work log opened, an image loaded, the chat resized.
    const moved = new ResizeObserver(onScroll);
    moved.observe(el);
    if (el.firstElementChild) moved.observe(el.firstElementChild);
    return () => {
      moved.disconnect();
      el.removeEventListener("scroll", onScroll);
      cancelAnimationFrame(frame);
    };
  }, [scroller, exchanges, shown]);

  if (!shown) return null;

  // Straight there, no smooth scrolling, so scrubbing keeps up with the pointer.
  const jump = (i: number) => {
    wanted.current = i;
    const seq = exchanges[i].seq;
    const el = scroller.current;
    const row = el?.querySelector<HTMLElement>(`[data-seq="${seq}"]`);
    if (!el) return;
    if (!row) {
      // Above what is read in: read back to it, then go there once it is drawn, unless another was asked for meanwhile.
      setActive(i);
      void reach?.(seq).then(() => {
        // The turns read in are drawn in a render of their own: go once the row is there and the chat has kept its place.
        const go = () => {
          if (!el.querySelector(`[data-seq="${seq}"]`)) return false;
          found.disconnect();
          requestAnimationFrame(() => wanted.current === i && jump(i));
          return true;
        };
        const found = new MutationObserver(go);
        if (!go()) found.observe(el, { childList: true, subtree: true });
        setTimeout(() => found.disconnect(), 10_000);
      });
      return;
    }
    el.scrollTop += row.getBoundingClientRect().top - el.getBoundingClientRect().top - 12;
    // At once, not when the browser gets to it: the chat would take the reader still at the bottom and, as what
    // was just read in above settles, put them back there.
    el.dispatchEvent(new Event("scroll"));
    pinned.current = { i, top: el.scrollTop };
    setActive(i);
  };
  // Ticks are spaced evenly, so the one under the pointer follows from its height on the strip.
  const under = (e: PointerEvent<HTMLElement>) => {
    const box = e.currentTarget.getBoundingClientRect();
    return Math.min(exchanges.length - 1, Math.max(0, Math.floor(((e.clientY - box.top) / box.height) * exchanges.length)));
  };

  return (
    <nav
      ref={ticks}
      aria-label={t("chatMap.label")}
      // Only where the gutter beside the 48rem column has room for it, so it never covers text.
      className="absolute top-1/2 left-1 z-10 hidden max-h-[70%] -translate-y-1/2 touch-none flex-col py-1 select-none @min-[54rem]/chat:flex"
      onPointerDown={(e) => {
        if (e.button !== 0) return;
        dragging.current = true;
        e.currentTarget.setPointerCapture(e.pointerId);
        const i = under(e);
        setHover(i);
        jump(i);
      }}
      onPointerMove={(e) => {
        const i = under(e);
        setHover(i);
        if (dragging.current) jump(i);
      }}
      onPointerUp={() => {
        // A drag that ended above what is read in goes there once it is.
        if (dragging.current && wanted.current !== undefined) jump(wanted.current);
        dragging.current = false;
      }}
      onPointerCancel={() => (dragging.current = false)}
      onLostPointerCapture={() => (dragging.current = false)}
      onPointerLeave={() => !dragging.current && setHover(undefined)}
      // The strip sits beside the scroller, not in it, so a wheel over it is passed on.
      onWheel={(e) => scroller.current?.scrollBy({ top: e.deltaY })}
      // One tab stop for the whole map; arrows move between ticks and jump with them.
      onKeyDown={(e) => {
        // Arrows step through exchanges, not ticks, so none is out of reach when a tick stands for several.
        const from = active;
        const to = { ArrowUp: from - 1, ArrowDown: from + 1, Home: 0, End: exchanges.length - 1 }[e.key];
        if (to === undefined) return;
        e.preventDefault();
        const i = Math.min(exchanges.length - 1, Math.max(0, to));
        ticks.current?.querySelector<HTMLElement>(`[data-tick="${tickOf(i, exchanges.length, count)}"]`)?.focus();
        jump(i);
      }}
    >
      {Array.from({ length: count }, (_, k) => {
        // The tick's first exchange, which it jumps to and is named by.
        const i = Math.ceil((k * exchanges.length) / count);
        const on = tickOf(active, exchanges.length, count) === k;
        return (
          <button
            key={k}
            type="button"
            data-tick={k}
            tabIndex={on ? 0 : -1}
            aria-label={exchanges[i].text.split("\n")[0]}
            aria-current={on || undefined}
            className="group flex min-h-0 shrink basis-2.5 cursor-pointer items-center px-1.5 outline-none"
            onClick={(e) => e.detail === 0 && jump(i)}
            onFocus={() => setHover((h) => (h !== undefined && tickOf(h, exchanges.length, count) === k ? h : i))}
            onBlur={() => setHover(undefined)}
          >
            <span
              className={cn(
                "h-0.5 rounded-full transition-[width,background-color] group-focus-visible:ring-ring group-focus-visible:ring-2",
                on ? "bg-foreground w-5" : hover !== undefined && tickOf(hover, exchanges.length, count) === k ? "bg-foreground/70 w-4" : "bg-muted-foreground/40 w-3",
              )}
            />
          </button>
        );
      })}
      {hover !== undefined && exchanges[hover] && (
        <div
          className="bg-popover text-popover-foreground pointer-events-none absolute left-full ml-2 w-72 -translate-y-1/2 rounded-lg border p-3 text-xs shadow-md"
          style={{ top: `${((hover + 0.5) / exchanges.length) * 100}%` }}
        >
          <p className="truncate font-semibold">{exchanges[hover].text}</p>
          {exchanges[hover].answer && <p className="text-muted-foreground mt-1 line-clamp-3 wrap-anywhere">{exchanges[hover].answer}</p>}
        </div>
      )}
    </nav>
  );
}
