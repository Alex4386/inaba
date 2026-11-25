import { beforeEach, describe, expect, it } from "vitest";
import { bindingOf, conflictOf, eventBinding, formatBinding, matches, reserved, resetAllBindings, resetBinding, setBinding } from "@/lib/keybindings";

const key = (key: string, code: string, mods: Partial<Record<"ctrlKey" | "altKey" | "shiftKey" | "metaKey", boolean>> = {}, extra = {}) => ({
  key,
  code,
  ctrlKey: false,
  altKey: false,
  shiftKey: false,
  metaKey: false,
  ...mods,
  ...extra,
});

describe("keybindings", () => {
  beforeEach(() => resetAllBindings());

  it("maps mod to Cmd on a Mac and Ctrl elsewhere", () => {
    expect(matches(key("k", "KeyK", { metaKey: true }), "palette", true)).toBe(true);
    expect(matches(key("k", "KeyK", { ctrlKey: true }), "palette", true)).toBe(false);
    expect(matches(key("k", "KeyK", { ctrlKey: true }), "palette", false)).toBe(true);
    // Extra modifiers make a different combo.
    expect(matches(key("K", "KeyK", { ctrlKey: true, shiftKey: true }), "palette", false)).toBe(false);
  });

  it("reads letters from the physical key and ignores IME composition", () => {
    expect(eventBinding(key("ㅠ", "KeyB"))).toBe("b");
    expect(eventBinding(key("b", "KeyB", {}, { isComposing: true }))).toBeUndefined();
    expect(eventBinding(key("Process", "KeyB", {}, { keyCode: 229 }))).toBeUndefined();
    expect(eventBinding(key("Shift", "ShiftLeft", { shiftKey: true }))).toBeUndefined();
    expect(eventBinding(key("Enter", "Enter", { metaKey: true }))).toBe("meta+enter");
  });

  it("follows a changed binding", () => {
    setBinding("palette", "mod+p");
    expect(matches(key("p", "KeyP", { metaKey: true }), "palette", true)).toBe(true);
    expect(matches(key("k", "KeyK", { metaKey: true }), "palette", true)).toBe(false);
  });

  it("finds conflicts within a scope and across nested scopes only", () => {
    expect(conflictOf("dock.shell", "b", true)?.id).toBe("dock.browser");
    expect(conflictOf("dock.shell", "f", true)?.id).toBe("dock.files");
    expect(conflictOf("find", "mod+k", true)?.id).toBe("palette");
    expect(conflictOf("palette", "mod+1", true)?.id).toBe("model.1");
    expect(conflictOf("palette", "mod+k", true)).toBeUndefined();
    // The composer sits in the thread view, so its keys reach thread handlers too.
    expect(conflictOf("interrupt", "mod+f", true)?.id).toBe("find");
    expect(conflictOf("find", "mod+enter", true)?.id).toBe("interrupt");
    // The picker opens from the composer; the dock is a sibling of the composer, not inside it.
    expect(conflictOf("model.1", "mod+enter", true)?.id).toBe("interrupt");
    expect(conflictOf("interrupt", "mod+1", true)?.id).toBe("model.1");
    setBinding("dock.browser", "mod+e");
    expect(conflictOf("interrupt", "mod+e", true)).toBeUndefined();
  });

  it("refuses a reset that would collide, and resets otherwise", () => {
    setBinding("palette", "mod+p");
    setBinding("sidebar", "mod+k");
    expect(resetBinding("palette", true)?.id).toBe("sidebar");
    expect(bindingOf("palette")).toBe("mod+p");
    setBinding("sidebar", "mod+b");
    expect(resetBinding("palette", true)).toBeUndefined();
    expect(bindingOf("palette")).toBe("mod+k");
  });

  it("stores the + key by name and keeps editing and dock focus keys free", () => {
    const b = eventBinding(key("+", "Equal", { metaKey: true, shiftKey: true }));
    expect(b).toBe("shift+meta+plus");
    expect(formatBinding(b!, true)).toBe("⇧⌘+");
    expect(reserved("mod+v", "composer", true)).toBe(true);
    expect(reserved("ctrl+v", "composer", true)).toBe(false);
    expect(reserved("tab", "dock", true)).toBe(true);
    expect(reserved("g", "dock", true)).toBe(false);
  });

  it("formats per platform", () => {
    expect(formatBinding("mod+shift+k", true)).toBe("⇧⌘K");
    expect(formatBinding("mod+shift+k", false)).toBe("Ctrl+Shift+K");
    expect(formatBinding("mod+enter", true)).toBe("⌘↵");
  });
});

it("redo stays reserved on a Mac whatever order the modifiers come in", () => {
  expect(reserved("shift+meta+z", "composer", true)).toBe(true);
  expect(reserved("meta+shift+z", "composer", true)).toBe(true);
  expect(reserved("ctrl+shift+z", "composer", false)).toBe(true);
});
