import { useSyncExternalStore } from "react";
import type { MessageKey } from "@/lib/i18n";

// Every keyboard shortcut of the UI, so the handlers and Settings › Keyboard read the same bindings.
// A binding is modifiers then a key, joined by "+": "mod+k", "shift+alt+f", "b". "mod" is Cmd on a
// Mac and Ctrl elsewhere; the key is a lowercase letter, digit or KeyboardEvent.key name ("enter").

/** Where a shortcut is live; two in one scope, or in scopes nested one inside the other, cannot share a binding. */
export type Scope = "global" | "thread" | "composer" | "picker" | "dock";

// A key pressed in an inner scope also reaches the handlers of every scope around it (the composer and
// dock sit in the thread view, the picker opens from the composer), so those pairs clash too.
const parent: Record<Scope, Scope | undefined> = { global: undefined, thread: "global", composer: "thread", picker: "composer", dock: "thread" };
const inside = (inner: Scope, outer: Scope) => {
  for (let s: Scope | undefined = inner; s; s = parent[s]) if (s === outer) return true;
  return false;
};

export interface Shortcut {
  id: string;
  binding: string;
  label: MessageKey;
  values?: Record<string, string>;
  scope: Scope;
}

export const shortcuts: Shortcut[] = [
  { id: "palette", binding: "mod+k", label: "keys.palette", scope: "global" },
  { id: "sidebar", binding: "mod+b", label: "keys.sidebar", scope: "global" },
  { id: "find", binding: "mod+f", label: "keys.find", scope: "thread" },
  { id: "interrupt", binding: "mod+enter", label: "keys.interrupt", scope: "composer" },
  ...[1, 2, 3, 4].map((n): Shortcut => ({ id: `model.${n}`, binding: `mod+${n}`, label: "keys.model", values: { n: String(n) }, scope: "picker" })),
  { id: "dock.browser", binding: "b", label: "keys.dockBrowser", scope: "dock" },
  { id: "dock.shell", binding: "t", label: "keys.dockShell", scope: "dock" },
  { id: "dock.files", binding: "f", label: "keys.dockFiles", scope: "dock" },
  { id: "dock.changes", binding: "d", label: "keys.dockChanges", scope: "dock" },
];

// shortcut: navigator.platform is deprecated but still set in every browser; switch to userAgentData once Safari and Firefox ship it.
export const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);

const MODS = ["ctrl", "alt", "shift", "meta"] as const;
const KEY = "inaba.keybindings";
const listeners = new Set<() => void>();

function load(): Record<string, string> {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? "{}");
    // Only string bindings survive, so a hand-edited or stale value cannot break the handlers.
    return v && typeof v === "object" ? Object.fromEntries(Object.entries(v).filter(([, b]) => typeof b === "string" && b)) as Record<string, string> : {};
  } catch {
    return {};
  }
}
let overrides = load();
let snapshot = { ...overrides };

// Another tab's change is picked up here, so this tab neither runs stale bindings nor saves over that change.
if (typeof window !== "undefined") {
  window.addEventListener("storage", (e) => {
    if (e.key !== KEY && e.key !== null) return;
    overrides = load();
    snapshot = { ...overrides };
    for (const l of listeners) l();
  });
}

function save(next: Record<string, string>) {
  overrides = next;
  snapshot = { ...next };
  try {
    localStorage.setItem(KEY, JSON.stringify(next));
  } catch {
    // Applies until the page reloads.
  }
  for (const l of listeners) l();
}

/** The binding in effect for a shortcut: the user's own, else the default. */
export const bindingOf = (id: string) => overrides[id] ?? shortcuts.find((s) => s.id === id)?.binding ?? "";

export const setBinding = (id: string, binding: string) => save({ ...overrides, [id]: binding });
/** Puts one shortcut back on its default, unless another shortcut now holds that key; returns that one instead. */
export function resetBinding(id: string, mac = isMac): Shortcut | undefined {
  const other = conflictOf(id, shortcuts.find((s) => s.id === id)?.binding ?? "", mac);
  if (!other) save(Object.fromEntries(Object.entries(overrides).filter(([k]) => k !== id)));
  return other;
}
export const resetAllBindings = () => save({});

/** The user's overrides, following changes, so labels and the settings table re-render. */
export function useKeybindings(): Record<string, string> {
  return useSyncExternalStore((l) => {
    listeners.add(l);
    return () => listeners.delete(l);
  }, () => snapshot);
}

/** A binding with "mod" spelled out for this platform and modifiers in a fixed order, for comparing. */
export function normalize(binding: string, mac = isMac): string {
  const parts = binding.toLowerCase().split("+");
  const key = parts.pop() ?? "";
  const mods = new Set(parts.map((p) => (p === "mod" ? (mac ? "meta" : "ctrl") : p === "cmd" ? "meta" : p)));
  return [...MODS.filter((m) => mods.has(m)), key].join("+");
}

type Keys = Pick<KeyboardEvent, "key" | "code" | "ctrlKey" | "altKey" | "shiftKey" | "metaKey"> & { isComposing?: boolean; keyCode?: number };

/**
 * The combo a key event makes, or undefined for a lone modifier or a key typed into an IME.
 * Letters and digits come from the physical key, so Korean input, Shift and a Mac's Option still give "k" or "1".
 * shortcut: on Dvorak, Colemak or AZERTY the defaults sit on the QWERTY positions while the labels name the letter;
 * prefer e.key when it is an ASCII letter if those users report it.
 */
export function eventBinding(e: Keys): string | undefined {
  if (e.isComposing || e.keyCode === 229) return undefined;
  const code = /^(?:Key([A-Z])|Digit(\d))$/.exec(e.code);
  // "+" is the separator in a binding, so that key is stored by name.
  const key = code ? (code[1] ?? code[2]).toLowerCase() : e.key === " " ? "space" : e.key === "+" ? "plus" : e.key.toLowerCase();
  if (["control", "alt", "shift", "meta", "os", "dead", "process", "unidentified", ""].includes(key)) return undefined;
  return [...MODS.filter((m) => e[`${m}Key`]), key].join("+");
}

/** Whether a key event is the shortcut's current binding. */
export function matches(e: Keys, id: string, mac = isMac): boolean {
  const b = eventBinding(e);
  return !!b && b === normalize(bindingOf(id), mac);
}

/** The other shortcut a binding would collide with, if any. */
export function conflictOf(id: string, binding: string, mac = isMac): Shortcut | undefined {
  const self = shortcuts.find((s) => s.id === id);
  const want = normalize(binding, mac);
  return shortcuts.find(
    (s) => s.id !== id && (!self || inside(s.scope, self.scope) || inside(self.scope, s.scope)) && normalize(bindingOf(s.id), mac) === want,
  );
}

/** Keys the browser and the dock's focus handling need, which a shortcut must not take. */
export function reserved(binding: string, scope: Scope, mac = isMac): boolean {
  const b = normalize(binding, mac);
  const mod = mac ? "meta" : "ctrl";
  // Select all, copy, cut, paste, undo and redo in the message box and every text field.
  // Normalized the same way, so modifier order (shift before meta) cannot slip past.
  if (["a", "c", "x", "v", "z", "shift+z", "y"].some((k) => b === normalize(`${mod}+${k}`, mac))) return true;
  // A plain dock key is caught on the dock container, so these would trap focus or stop its buttons.
  return scope === "dock" && ["tab", "shift+tab", "enter", "space", "arrowup", "arrowdown", "arrowleft", "arrowright"].includes(b);
}

const symbols: Record<string, string> = { ctrl: "⌃", alt: "⌥", shift: "⇧", meta: "⌘" };
const names: Record<string, string> = { ctrl: "Ctrl", alt: "Alt", shift: "Shift", meta: "Win" };
const keyNames: Record<string, string> = { enter: "↵", escape: "Esc", arrowup: "↑", arrowdown: "↓", arrowleft: "←", arrowright: "→", backspace: "⌫", space: "Space", tab: "Tab", plus: "+" };

/** A binding as it is shown: "⌘⇧K" on a Mac, "Ctrl+Shift+K" elsewhere. */
export function formatBinding(binding: string, mac = isMac): string {
  const parts = normalize(binding, mac).split("+");
  const key = parts.pop() ?? "";
  const k = keyNames[key] ?? key.charAt(0).toUpperCase() + key.slice(1);
  return mac ? parts.map((p) => symbols[p]).join("") + k : [...parts.map((p) => names[p]), k].join("+");
}
