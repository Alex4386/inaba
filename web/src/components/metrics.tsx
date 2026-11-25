// Small visualizations for dashboards: a usage meter, a split bar with its legend, and a live sparkline.
// Colors come from the theme's tokens, so light and dark each get their own; text stays in text tokens.
import { useState } from "react";
import { cn } from "@/lib/utils";

/** How full something is: calm under 75%, warning to 90%, critical beyond. */
export function Meter({ value, label, className }: { /** 0 to 1; undefined when unknown. */ value?: number; label?: string; className?: string }) {
  if (value === undefined || !Number.isFinite(value)) return <span className="text-muted-foreground text-xs">—</span>;
  const percent = Math.round(Math.min(1, Math.max(0, value)) * 100);
  const tone = percent >= 90 ? "bg-destructive" : percent >= 75 ? "bg-warning" : "bg-primary";
  return (
    <div className={cn("flex min-w-28 flex-col gap-1", className)} role="meter" aria-valuenow={percent} aria-valuemin={0} aria-valuemax={100} aria-label={label}>
      <div className="bg-muted h-1.5 w-full overflow-hidden rounded-full">
        <div className={cn("h-full rounded-full transition-[width]", tone)} style={{ width: `${percent}%` }} />
      </div>
      <span className="text-muted-foreground text-xs tabular-nums">{label ? `${label} · ${percent}%` : `${percent}%`}</span>
    </div>
  );
}

export interface Part {
  label: string;
  value: number;
  /** A background class from the theme, such as bg-success. */
  tone: string;
}

/** Parts of a whole on one bar, gaps between them, and a legend naming each with its count. */
export function SplitBar({ parts, format = String, className }: { parts: Part[]; /** How a value reads, such as a currency. */ format?: (v: number) => string; className?: string }) {
  const total = parts.reduce((n, p) => n + p.value, 0);
  return (
    <div className={cn("flex flex-col gap-2", className)}>
      <div className="bg-muted flex h-2.5 w-full gap-0.5 overflow-hidden rounded-full">
        {total > 0 &&
          parts
            .filter((p) => p.value > 0)
            .map((p) => <div key={p.label} title={`${p.label}: ${format(p.value)}`} className={cn("h-full first:rounded-l-full last:rounded-r-full", p.tone)} style={{ width: `${(p.value / total) * 100}%` }} />)}
      </div>
      <ul className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
        {parts.map((p) => (
          <li key={p.label} className="flex items-center gap-1.5">
            <span className={cn("size-2 rounded-full", p.tone)} aria-hidden />
            <span className="text-muted-foreground">{p.label}</span>
            <span className="font-medium tabular-nums">{format(p.value)}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** A value over time, newest on the right; hovering reads any point. */
export function Sparkline({ points, max = 100, format = (v) => `${Math.round(v)}%`, className }: { points: number[]; max?: number; format?: (v: number) => string; className?: string }) {
  const [at, setAt] = useState<number>();
  const w = 240;
  const h = 48;
  if (points.length < 2) return <div className={cn("h-12", className)} />;
  const x = (i: number) => (i / (points.length - 1)) * w;
  const y = (v: number) => h - 2 - (Math.min(v, max) / max) * (h - 4);
  const line = points.map((v, i) => `${i ? "L" : "M"}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join("");
  const shown = at ?? points.length - 1;
  return (
    <div className={cn("relative", className)}>
      <svg
        viewBox={`0 0 ${w} ${h}`}
        preserveAspectRatio="none"
        className="h-12 w-full overflow-visible"
        onMouseMove={(e) => {
          const r = e.currentTarget.getBoundingClientRect();
          setAt(Math.round(((e.clientX - r.left) / r.width) * (points.length - 1)));
        }}
        onMouseLeave={() => setAt(undefined)}
      >
        <path d={`${line}L${w},${h}L0,${h}Z`} className="fill-primary/10" />
        <path d={line} className="stroke-primary fill-none" strokeWidth={2} vectorEffect="non-scaling-stroke" strokeLinejoin="round" />
        {at !== undefined && <line x1={x(at)} x2={x(at)} y1={0} y2={h} className="stroke-border" vectorEffect="non-scaling-stroke" />}
        <circle cx={x(shown)} cy={y(points[shown])} r={3} className="fill-primary stroke-background" strokeWidth={2} vectorEffect="non-scaling-stroke" />
      </svg>
      <span className="text-foreground absolute top-0 right-0 text-xs font-medium tabular-nums">{format(points[shown])}</span>
    </div>
  );
}

/** Bytes as people read them. */
export function bytes(n: bigint | number) {
  let v = Number(n);
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}
