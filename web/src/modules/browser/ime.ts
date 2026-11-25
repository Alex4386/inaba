/**
 * Turning what the browser panel's keyboard catcher hears into input for the host's
 * page. Typing reaches the page as text, not as keys: that way an input method
 * (Hangul, kana and kanji, pinyin) works, since it is the local browser that composes,
 * and the page only shows the composition and takes the result.
 */

/** What goes to the host's page: a key, text typed, or text being composed. */
export type Input =
  | { case: "key"; value: { type: "down" | "up"; key: string; code: string; keyCode: number; text: string; modifiers: number; commands: string[] } }
  | { case: "text"; value: string }
  | { case: "composition"; value: string };

/** The DOM events that matter, with only the fields that do. */
export type Heard =
  | { type: "compositionupdate"; data: string }
  | { type: "compositionend"; data: string }
  | { type: "input"; inputType: string; data: string | null; isComposing: boolean }
  | {
      type: "keydown" | "keyup";
      key: string;
      code: string;
      keyCode: number;
      isComposing: boolean;
      altKey: boolean;
      ctrlKey: boolean;
      metaKey: boolean;
      shiftKey: boolean;
    };

// Keys that act without typing, by the Windows key code CDP needs to act on them.
const keyCodes: Record<string, number> = {
  Backspace: 8, Tab: 9, Enter: 13, Escape: 27, PageUp: 33, PageDown: 34, End: 35, Home: 36,
  ArrowLeft: 37, ArrowUp: 38, ArrowRight: 39, ArrowDown: 40, Delete: 46,
};

// Shortcuts headless Chrome only follows as editing commands. Paste is not here: it
// brings this machine's clipboard, as text, through the paste event.
const shortcuts: Record<string, string> = { a: "selectAll", c: "copy", x: "cut", z: "undo", y: "redo" };

/** CDP's modifier bits: Alt 1, Ctrl 2, Meta 4, Shift 8. */
export const modifierBits = (e: { altKey: boolean; ctrlKey: boolean; metaKey: boolean; shiftKey: boolean }) =>
  (e.altKey ? 1 : 0) | (e.ctrlKey ? 2 : 0) | (e.metaKey ? 4 : 0) | (e.shiftKey ? 8 : 0);

/**
 * A translator for one keyboard catcher. It remembers which keys it sent down, so a
 * key's release goes to the page only if its press did: the Enter that confirms a
 * Japanese conversion is pressed for the input method and released after it is done.
 */
export function translator(): (e: Heard) => Input[] {
  const down = new Set<string>();
  return (e) => {
    const out = translate(e);
    for (const i of out) {
      if (i.case !== "key") continue;
      if (i.value.type === "down") down.add(i.value.code);
    }
    return out.filter((i) => i.case !== "key" || i.value.type === "down" || down.delete(i.value.code));
  };
}

/** What to send the page for one event heard, on its own; often nothing. */
export function translate(e: Heard): Input[] {
  switch (e.type) {
    case "compositionupdate":
      return [{ case: "composition", value: e.data }];
    case "compositionend":
      // Text commits the composition; an empty end is one given up, which clears it.
      return e.data ? [{ case: "text", value: e.data }] : [{ case: "composition", value: "" }];
    case "input":
      // Composed text arrives by the composition events instead.
      if (e.isComposing || e.inputType.startsWith("insertComposition")) return [];
      return e.inputType === "insertText" && e.data ? [{ case: "text", value: e.data }] : [];
    case "keydown":
    case "keyup": {
      // While an input method composes, the keys are its own; 229 is what browsers report for them.
      if (e.isComposing || e.key === "Process" || e.keyCode === 229) return [];
      const command = e.ctrlKey || e.metaKey;
      // A key that types arrives as text through the input event, unless it is a shortcut.
      if (e.key.length === 1 && !command && !e.altKey) return [];
      const lower = e.key.toLowerCase();
      if (command && lower === "v") return [];
      const commands = e.type === "keydown" && command && shortcuts[lower] ? [shortcuts[lower] === "undo" && e.shiftKey ? "redo" : shortcuts[lower]] : [];
      return [
        {
          case: "key",
          value: {
            type: e.type === "keydown" ? "down" : "up",
            key: e.key,
            code: e.code,
            keyCode: keyCodes[e.key] ?? e.keyCode,
            // Enter is the one acting key that also types, which is what submits a form.
            text: e.key === "Enter" && e.type === "keydown" && !command ? "\r" : "",
            modifiers: modifierBits(e),
            commands,
          },
        },
      ];
    }
  }
}
