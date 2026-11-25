import { expect, test } from "vitest";
import { isClipboardKey } from "./Terminal";

const key = (k: string, mods: Partial<KeyboardEvent> = {}) => ({ key: k, ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, ...mods });

test("off Mac, Ctrl+C copies only with a selection, otherwise it reaches the program", () => {
  expect(isClipboardKey(key("c", { ctrlKey: true }), true, false)).toBe(true);
  expect(isClipboardKey(key("c", { ctrlKey: true }), false, false)).toBe(false);
  expect(isClipboardKey(key("v", { ctrlKey: true }), false, false)).toBe(true);
  expect(isClipboardKey(key("c", { metaKey: true }), true, false)).toBe(false);
});

test("on Mac, only Cmd is a clipboard key; Ctrl+C always interrupts and Ctrl+V reaches the program", () => {
  expect(isClipboardKey(key("c", { metaKey: true }), true, true)).toBe(true);
  expect(isClipboardKey(key("V", { metaKey: true }), false, true)).toBe(true);
  expect(isClipboardKey(key("c", { ctrlKey: true }), true, true)).toBe(false);
  expect(isClipboardKey(key("v", { ctrlKey: true }), false, true)).toBe(false);
});

test("other keys and modifier mixes pass through", () => {
  expect(isClipboardKey(key("v"), false, false)).toBe(false);
  expect(isClipboardKey(key("v", { ctrlKey: true, shiftKey: true }), false, false)).toBe(false);
  expect(isClipboardKey(key("d", { ctrlKey: true }), true, false)).toBe(false);
});
