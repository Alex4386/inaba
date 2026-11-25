// Series colors for usage charts. The theme's --chart-1..5 are one CVD-checked palette, so a
// sixth series is never a made-up hue: everything past the fifth folds into "other".

// Written out in full so Tailwind finds the class names.
const fills = ["fill-chart-1", "fill-chart-2", "fill-chart-3", "fill-chart-4", "fill-chart-5"];
const backgrounds = ["bg-chart-1", "bg-chart-2", "bg-chart-3", "bg-chart-4", "bg-chart-5"];
export const otherTone = { fill: "fill-muted-foreground/40", bg: "bg-muted-foreground/40" };

export interface Tone {
  fill: string;
  bg: string;
  /** Folded into "other": past the palette's five colors. */
  other: boolean;
}

/**
 * A color per id, by the ids' sorted order rather than by how much each used, so a series
 * keeps its color when the ranking changes.
 * shortcut: colors follow the ids present in one summary, so a provider that only appears in a
 * longer range can shift the others; key colors to the host's provider list if that bothers people.
 */
export function seriesTones(ids: string[]): Map<string, Tone> {
  const sorted = [...new Set(ids)].sort();
  return new Map(sorted.map((id, i) => [id, i < fills.length ? { fill: fills[i], bg: backgrounds[i], other: false } : { ...otherTone, other: true }]));
}
