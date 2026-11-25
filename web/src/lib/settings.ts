import { useSyncExternalStore } from "react";

// Preferences of this browser, not of the account: they stay on the device.
const NOTIFY_KEY = "inaba.notify";
const listeners = new Set<() => void>();

export function setNotify(on: boolean) {
  localStorage.setItem(NOTIFY_KEY, on ? "on" : "off");
  for (const listener of listeners) listener();
}

/** Whether to show a toast when a thread other than the open one finishes or asks. On unless turned off. */
export const notifyEnabled = () => localStorage.getItem(NOTIFY_KEY) !== "off";

export function useNotify(): boolean {
  return useSyncExternalStore((listener) => {
    listeners.add(listener);
    return () => listeners.delete(listener);
  }, notifyEnabled);
}

// The font code is shown in: the editor, terminals, diffs and code in messages. Empty is the system's own.
const CODE_FONT_KEY = "inaba.codeFont";
export const systemCodeFont = "ui-monospace, SFMono-Regular, Menlo, monospace";
const fontListeners = new Set<() => void>();

/** The chosen code font's family, "" for the system's monospace. */
export function codeFont(): string {
  try {
    return localStorage.getItem(CODE_FONT_KEY) ?? "";
  } catch {
    return "";
  }
}

/** The CSS font stack for code: the chosen family first, the system's after it for anything it lacks. */
export function codeFontStack(family = codeFont()): string {
  return family ? `${JSON.stringify(family)}, ${systemCodeFont}` : systemCodeFont;
}

/** Puts the code font where styles read it: Tailwind's font-mono and the diff view's own variable. */
export function applyCodeFont() {
  const stack = codeFontStack();
  document.documentElement.style.setProperty("--font-mono", stack);
  document.documentElement.style.setProperty("--diffs-font-family", stack);
}

export function setCodeFont(family: string) {
  try {
    localStorage.setItem(CODE_FONT_KEY, family);
  } catch {
    // Applies until the page reloads.
  }
  applyCodeFont();
  for (const listener of fontListeners) listener();
}

/** The code font stack, following changes, for what sets its font in code: Monaco and the terminal. */
export function useCodeFont(): string {
  return useSyncExternalStore((listener) => {
    fontListeners.add(listener);
    return () => fontListeners.delete(listener);
  }, codeFontStack);
}

// The look of this browser's UI: a preset of neutral tones, an accent color and how round corners are.
const THEME_KEY = "inaba.theme";
export const themePresets = ["neutral", "cool", "warm", "contrast"] as const;
export type ThemePreset = (typeof themePresets)[number];
/** accent is "" for the preset's own primary; radius is in rem, as --radius is. */
export type ThemePrefs = { preset: ThemePreset; accent: string; radius: number };
export const defaultTheme: ThemePrefs = { preset: "neutral", accent: "", radius: 0.625 };
export const radiusRange = { min: 0, max: 1.25, step: 0.125 };
const themeListeners = new Set<() => void>();

/** A "#rrggbb" color in lower case, or undefined for anything else. The leading # is optional. */
export function parseHex(text: string): string | undefined {
  const m = /^#?([0-9a-f]{6})$/i.exec(text.trim());
  return m ? `#${m[1].toLowerCase()}` : undefined;
}

/** WCAG relative luminance of a "#rrggbb" color, 0 for black to 1 for white. */
export function luminance(hex: string): number {
  const n = Number.parseInt(hex.slice(1), 16);
  const [r, g, b] = [n >> 16, (n >> 8) & 255, n & 255].map((c) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

/** Near-white or near-black, whichever contrasts more with the color, for text drawn on it. */
export function readableForeground(hex: string): string {
  const l = luminance(hex);
  return 1.05 / (l + 0.05) >= (l + 0.05) / 0.05 ? "#fafafa" : "#0a0a0a";
}

/** WCAG contrast ratio of two "#rrggbb" colors, 1 to 21. */
export function contrastRatio(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

/** Whether the accent fades into the page in light or dark mode: under 3:1, WCAG's floor for buttons and focus rings. */
export function accentLowContrast(accent: string): boolean {
  // shortcut: white and #0a0a0a stand in for every preset's light and dark --background (all within a few % of them, oklch tokens aren't parsed); read the computed token if a preset strays.
  return contrastRatio(accent, "#ffffff") < 3 || contrastRatio(accent, "#0a0a0a") < 3;
}

let themeCache: { raw: string | null; prefs: ThemePrefs } | undefined;
// The look while storage is blocked, so changes still apply until reload as the code font's do.
let blockedTheme: string | null = null;

/** The saved look, each field checked since storage can hold anything; a bad field falls back to its default. */
export function themePrefs(): ThemePrefs {
  let raw: string | null;
  try {
    raw = localStorage.getItem(THEME_KEY);
  } catch {
    raw = blockedTheme;
  }
  // The same object while nothing changed, as useSyncExternalStore needs.
  if (themeCache?.raw === raw) return themeCache.prefs;
  let saved: Partial<Record<keyof ThemePrefs, unknown>> = {};
  try {
    saved = JSON.parse(raw ?? "{}") ?? {};
  } catch {
    // Not JSON: the defaults.
  }
  const r = saved.radius;
  const prefs: ThemePrefs = {
    preset: themePresets.includes(saved.preset as ThemePreset) ? (saved.preset as ThemePreset) : defaultTheme.preset,
    accent: typeof saved.accent === "string" ? (parseHex(saved.accent) ?? "") : "",
    radius: typeof r === "number" && r >= radiusRange.min && r <= radiusRange.max ? r : defaultTheme.radius,
  };
  themeCache = { raw, prefs };
  return prefs;
}

/** Puts the look on the root element: the preset as data-preset (index.css has its tokens) and the rest as inline tokens, which win in light and dark alike. */
export function applyTheme() {
  const { preset, accent, radius } = themePrefs();
  const root = document.documentElement;
  if (preset === "neutral") delete root.dataset.preset;
  else root.dataset.preset = preset;
  const fg = accent && readableForeground(accent);
  for (const name of ["--primary", "--ring", "--sidebar-primary", "--sidebar-ring"]) {
    if (accent) root.style.setProperty(name, accent);
    else root.style.removeProperty(name);
  }
  for (const name of ["--primary-foreground", "--sidebar-primary-foreground"]) {
    if (fg) root.style.setProperty(name, fg);
    else root.style.removeProperty(name);
  }
  root.style.setProperty("--radius", `${radius}rem`);
}

/** Saves part of the look, or all of it back to the defaults with no argument. */
export function setThemePrefs(change?: Partial<ThemePrefs>) {
  const raw = change ? JSON.stringify({ ...themePrefs(), ...change }) : null;
  try {
    if (raw) localStorage.setItem(THEME_KEY, raw);
    else localStorage.removeItem(THEME_KEY);
  } catch {
    blockedTheme = raw;
  }
  applyTheme();
  for (const listener of themeListeners) listener();
}

// shortcut: no "storage" event listener, so other open tabs keep their look until reload, like notify and the code font; listen if that bites.
export function useThemePrefs(): ThemePrefs {
  return useSyncExternalStore((listener) => {
    themeListeners.add(listener);
    return () => themeListeners.delete(listener);
  }, themePrefs);
}

// main.tsx imports this module before it renders, so the saved look is on before anything draws.
if (typeof document !== "undefined") applyTheme();
