import { type PointerEvent, type RefObject, useEffect, useRef, useState } from "react";
import { useIsMobile } from "@/hooks/use-mobile";
import { useI18n } from "@/lib/i18n";
import { useChatMap } from "@/lib/settings";
import { cn } from "@/lib/utils";

/** A message of the user's and the start of the answer to it. seq matches the row's data-seq. */
export type Exchange = { seq: string; text: string; answer: string };

/** Ticks at the chat's left edge, one per exchange, to jump or scrub through a long conversation. */
export function ChatMap({ exchanges, scroller }: { exchanges: Exchange[]; scroller: RefObject<HTMLElement | null> }) {
  const { t } = useI18n();
  const on = useChatMap();
  const isMobile = useIsMobile();
  const [active, setActive] = useState(0);
  const [hover, setHover] = useState<number>();
  const dragging = useRef(false);
  const ticks = useRef<HTMLElement>(null);
  // The tick last jumped to and where that left the scroll, so the bottom rule below does not override a click.
  const pinned = useRef<{ i: number; top: number }>(undefined);
  const shown = on && !isMobile && exchanges.length >= 2;

  // The exchange in view: the last one whose message starts above a third of the way down. Measured once a frame at most.
  useEffect(() => {
    const el = scroller.current;
    if (!el || !shown) return;
    let frame = 0;
    const measure = () => {
      frame = 0;
      const line = el.getBoundingClientRect().top + el.clientHeight / 3;
      const rows = el.querySelectorAll<HTMLElement>("[data-seq]");
      let at = 0;
      rows.forEach((row, i) => {
        if (row.getBoundingClientRect().top <= line) at = i;
      });
      // Scrolled to the end, a short last exchange never reaches the line.
      if (el.scrollHeight - el.scrollTop - el.clientHeight < 4) at = rows.length - 1;
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
    const el = scroller.current;
    const row = el?.querySelector<HTMLElement>(`[data-seq="${exchanges[i].seq}"]`);
    if (!el || !row) return;
    el.scrollTop += row.getBoundingClientRect().top - el.getBoundingClientRect().top - 12;
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
      onPointerUp={() => (dragging.current = false)}
      onPointerCancel={() => (dragging.current = false)}
      onLostPointerCapture={() => (dragging.current = false)}
      onPointerLeave={() => !dragging.current && setHover(undefined)}
      // The strip sits beside the scroller, not in it, so a wheel over it is passed on.
      onWheel={(e) => scroller.current?.scrollBy({ top: e.deltaY })}
      // One tab stop for the whole map; arrows move between ticks and jump with them.
      onKeyDown={(e) => {
        const from = Number((e.target as HTMLElement).dataset.tick ?? active);
        const to = { ArrowUp: from - 1, ArrowDown: from + 1, Home: 0, End: exchanges.length - 1 }[e.key];
        if (to === undefined) return;
        e.preventDefault();
        const i = Math.min(exchanges.length - 1, Math.max(0, to));
        ticks.current?.querySelector<HTMLElement>(`[data-tick="${i}"]`)?.focus();
        jump(i);
      }}
    >
      {exchanges.map((x, i) => (
        <button
          key={x.seq}
          type="button"
          data-tick={i}
          tabIndex={i === active ? 0 : -1}
          aria-label={x.text.split("\n")[0]}
          aria-current={i === active || undefined}
          className="group flex min-h-0 shrink basis-2.5 cursor-pointer items-center px-1.5 outline-none"
          onClick={() => jump(i)}
          onFocus={() => setHover(i)}
          onBlur={() => setHover(undefined)}
        >
          <span
            className={cn(
              "h-0.5 rounded-full transition-[width,background-color] group-focus-visible:ring-ring group-focus-visible:ring-2",
              i === active ? "bg-foreground w-5" : i === hover ? "bg-foreground/70 w-4" : "bg-muted-foreground/40 w-3",
            )}
          />
        </button>
      ))}
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
