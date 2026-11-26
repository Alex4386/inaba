import { useLayoutEffect, useRef } from "react";
import { SidebarRail, useSidebar } from "@/components/ui/sidebar";

// The sidebar's width, kept in this browser. Its own default is 16rem, 256px.
const WIDTH_KEY = "inaba.sidebarWidth";
const MIN = 200;
const MAX = 480;
const clamp = (w: number) => Math.min(MAX, Math.max(MIN, Math.round(w)));

function storedWidth(): number | undefined {
  try {
    const w = Number(localStorage.getItem(WIDTH_KEY));
    return w ? clamp(w) : undefined;
  } catch {
    return undefined;
  }
}

/** The sidebar's edge: dragged, it sets the sidebar's width; clicked, it folds the sidebar as before. */
export function SidebarResizeRail() {
  const { open, toggleSidebar } = useSidebar();
  const rail = useRef<HTMLButtonElement>(null);
  const drag = useRef<{ x: number; from: number; moved: boolean }>(undefined);
  const dragged = useRef(false);
  const wrapper = () => rail.current?.closest<HTMLElement>("[data-slot=sidebar-wrapper]");
  const setWidth = (w: number) => wrapper()?.style.setProperty("--sidebar-width", `${w}px`);

  // Before the first paint, so a stored width shows without a jump from the default.
  useLayoutEffect(() => {
    const w = storedWidth();
    if (w) setWidth(w);
  }, []);

  const end = () => {
    const d = drag.current;
    drag.current = undefined;
    dragged.current = Boolean(d?.moved);
    if (!d?.moved) return;
    delete wrapper()?.dataset.resizing;
    const w = rail.current?.closest("[data-slot=sidebar]")?.querySelector("[data-slot=sidebar-gap]")?.getBoundingClientRect().width;
    try {
      if (w) localStorage.setItem(WIDTH_KEY, String(clamp(w)));
    } catch {
      // Kept until the page reloads.
    }
  };

  return (
    <SidebarRail
      ref={rail}
      onPointerDown={(e) => {
        // A folded sidebar only opens; its width is set once it is open.
        if (!open || e.button !== 0) return;
        const gap = rail.current?.closest("[data-slot=sidebar]")?.querySelector("[data-slot=sidebar-gap]");
        drag.current = { x: e.clientX, from: gap?.getBoundingClientRect().width ?? 256, moved: false };
        e.currentTarget.setPointerCapture(e.pointerId);
      }}
      onPointerMove={(e) => {
        const d = drag.current;
        if (!d) return;
        const dx = e.clientX - d.x;
        // A few pixels of wobble is still a click.
        if (!d.moved && Math.abs(dx) < 4) return;
        if (!d.moved) {
          d.moved = true;
          // The sidebar's width animates for folding; while dragged it follows the pointer at once.
          const w = wrapper();
          if (w) w.dataset.resizing = "";
        }
        setWidth(clamp(d.from + dx));
      }}
      onPointerUp={end}
      onPointerCancel={end}
      onClick={() => {
        if (dragged.current) dragged.current = false;
        else toggleSidebar();
      }}
    />
  );
}
