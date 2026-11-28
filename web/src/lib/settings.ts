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

// The column of ticks beside a thread's chat, one per message of the user's. On unless turned off.
const CHAT_MAP_KEY = "inaba.chatMap";
const chatMapListeners = new Set<() => void>();

export function setChatMap(on: boolean) {
  localStorage.setItem(CHAT_MAP_KEY, on ? "on" : "off");
  for (const listener of chatMapListeners) listener();
}

export const chatMapEnabled = () => localStorage.getItem(CHAT_MAP_KEY) !== "off";

export function useChatMap(): boolean {
  return useSyncExternalStore((listener) => {
    chatMapListeners.add(listener);
    return () => chatMapListeners.delete(listener);
  }, chatMapEnabled);
}

// The language new threads' agents talk in: the app's own by default, whatever one writes in with "auto",
// or one picked. A BCP 47 tag, as the host takes it.
const AGENT_LANGUAGE_KEY = "inaba.agentLanguage";
const agentLanguageListeners = new Set<() => void>();

export function setAgentLanguage(choice: string) {
  localStorage.setItem(AGENT_LANGUAGE_KEY, choice);
  for (const listener of agentLanguageListeners) listener();
}

/** The choice as saved: "interface" (the default), "auto", or a language's tag. */
export const agentLanguageChoice = () => localStorage.getItem(AGENT_LANGUAGE_KEY) || "interface";

export function useAgentLanguage(): string {
  return useSyncExternalStore((listener) => {
    agentLanguageListeners.add(listener);
    return () => agentLanguageListeners.delete(listener);
  }, agentLanguageChoice);
}

/** The tag a new thread is made with for a choice, given the app's language; empty leaves it to the agent. */
export const agentLanguageTag = (choice: string, locale: string) => (choice === "interface" ? locale : choice === "auto" ? "" : choice);

// The terminals' text size, in pixels.
const TERMINAL_SIZE_KEY = "inaba.terminalFontSize";
export const terminalFontSizes = [10, 11, 12, 13, 14, 15, 16, 18, 20, 22, 24];
const terminalSizeListeners = new Set<() => void>();

export function terminalFontSize(): number {
  const size = Number(localStorage.getItem(TERMINAL_SIZE_KEY));
  return terminalFontSizes.includes(size) ? size : 13;
}

export function setTerminalFontSize(size: number) {
  localStorage.setItem(TERMINAL_SIZE_KEY, String(size));
  for (const listener of terminalSizeListeners) listener();
}

export function useTerminalFontSize(): number {
  return useSyncExternalStore((listener) => {
    terminalSizeListeners.add(listener);
    return () => terminalSizeListeners.delete(listener);
  }, terminalFontSize);
}

// Whether the file editor wraps long lines. Off unless turned on.
const WRAP_KEY = "inaba.files.wrap";
const wrapListeners = new Set<() => void>();

export function setWordWrap(on: boolean) {
  localStorage.setItem(WRAP_KEY, on ? "on" : "off");
  for (const listener of wrapListeners) listener();
}

export function useWordWrap(): boolean {
  return useSyncExternalStore((listener) => {
    wrapListeners.add(listener);
    return () => wrapListeners.delete(listener);
  }, () => localStorage.getItem(WRAP_KEY) === "on");
}

// The font code is shown in: the editor, terminals, diffs and code in messages. Empty is JetBrains Mono, which inaba carries.
const CODE_FONT_KEY = "inaba.codeFont";
// Hangul is in no code font here: it falls to Pretendard rather than the generic monospace, GulimChe in Korean Windows.
const codeFallback = '"JetBrains Mono Variable", "Pretendard Variable", monospace';
export const builtInCodeFonts = [
  { family: "", label: "JetBrains Mono" },
  { family: "monospace", label: "" },
] as const;
const fontListeners = new Set<() => void>();
// While the code font is still being fetched its stack ends in a spare monospace, so the stack
// changes once it is in: Monaco and the terminal measure their cells again only when their font
// changes, and the page draws without waiting for the font.
let fontLoads = 0;

/** The chosen code font's family: "" for JetBrains Mono, "monospace" for the system's own. */
export function codeFont(): string {
  try {
    return localStorage.getItem(CODE_FONT_KEY) ?? "";
  } catch {
    return "";
  }
}

/** The CSS font stack for code: the chosen family first, the bundled ones after it for anything it lacks. */
export function codeFontStack(family = codeFont()): string {
  const stack = family === "monospace" ? `ui-monospace, SFMono-Regular, Menlo, "Cascadia Mono", Consolas, ${codeFallback}` : family ? `${JSON.stringify(family)}, ${codeFallback}` : codeFallback;
  return fontLoads > 0 ? `${stack}, monospace` : stack;
}

/** Fetches the web fonts the code font needs, then tells what set it in code (Monaco, the terminal) to measure again. */
export function loadCodeFont(): Promise<void> {
  fontLoads++;
  const done = () => {
    fontLoads--;
    applyCodeFont();
    for (const listener of fontListeners) listener();
  };
  return document.fonts.load(`13px ${codeFontStack()}`).then(done, done);
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
  void loadCodeFont();
  applyCodeFont();
}

/** The code font stack, following changes, for what sets its font in code: Monaco and the terminal. */
export function useCodeFont(): string {
  return useSyncExternalStore((listener) => {
    fontListeners.add(listener);
    return () => fontListeners.delete(listener);
  }, codeFontStack);
}

// The font of the rest of the UI. Empty is Pretendard, which inaba carries itself.
const UI_FONT_KEY = "inaba.uiFont";
export const builtInUiFonts = [
  { family: "", label: "Pretendard" },
  { family: "Geist Variable", label: "Geist" },
  { family: "system-ui", label: "" },
] as const;
// --cjk-font is a font for Han characters in the page's language; see index.css.
const uiFallback = "var(--cjk-font), system-ui, -apple-system, sans-serif";

/** The chosen UI font's family: "" for Pretendard, "system-ui" for the system's own. */
export function uiFont(): string {
  try {
    return localStorage.getItem(UI_FONT_KEY) ?? "";
  } catch {
    return "";
  }
}

/** The CSS font stack for the UI: the chosen family first, the system's after it for anything it lacks. */
export function uiFontStack(family = uiFont()): string {
  if (family === "system-ui") return uiFallback;
  return `${JSON.stringify(family || "Pretendard Variable")}, ${uiFallback}`;
}

/** Puts the UI font where styles read it, the --ui-font variable behind Tailwind's font-sans. */
export function applyUiFont() {
  document.documentElement.style.setProperty("--ui-font", uiFontStack());
}

export function setUiFont(family: string) {
  try {
    localStorage.setItem(UI_FONT_KEY, family);
  } catch {
    // Applies until the page reloads.
  }
  applyUiFont();
  for (const listener of fontListeners) listener();
}

/** The UI font stack, following changes. */
export function useUiFont(): string {
  return useSyncExternalStore((listener) => {
    fontListeners.add(listener);
    return () => fontListeners.delete(listener);
  }, uiFontStack);
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
