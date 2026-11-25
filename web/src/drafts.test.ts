import { expect, test } from "vitest";
import { historyCap, historyEntryChars, historyTotalChars, recallList, recallStep, withPrompt } from "@/lib/drafts";

test("withPrompt puts the newest first, keeps a repeat once and caps the list", () => {
  expect(withPrompt(["b", "a"], "c")).toEqual(["c", "b", "a"]);
  expect(withPrompt(["b", "a"], "b")).toEqual(["b", "a"]);
  expect(withPrompt(Array.from({ length: historyCap }, (_, i) => String(i)), "new")).toHaveLength(historyCap);
});

test("withPrompt skips huge prompts and keeps the total size bounded", () => {
  expect(withPrompt(["a"], "x".repeat(historyEntryChars + 1))).toEqual(["a"]);
  const big = "y".repeat(historyEntryChars);
  const list = withPrompt(Array.from({ length: historyCap }, () => big), "new");
  expect(list[0]).toBe("new");
  expect(list.join("").length).toBeLessThanOrEqual(historyTotalChars);
});

test("recallStep walks back through history and forward to the empty draft", () => {
  const history = ["newest", "older"];
  expect(recallStep(history, undefined, "older")).toEqual({ index: 0, text: "newest" });
  expect(recallStep(history, 0, "older")).toEqual({ index: 1, text: "older" });
  expect(recallStep(history, 1, "older")).toBeUndefined();
  expect(recallStep(history, 1, "newer")).toEqual({ index: 0, text: "newest" });
  expect(recallStep(history, 0, "newer")).toEqual({ text: "" });
  expect(recallStep(history, undefined, "newer")).toBeUndefined();
  expect(recallStep([], undefined, "older")).toBeUndefined();
});

test("recallList puts this browser's prompts first, then the thread's, each once", () => {
  expect(recallList(["b", "a"], ["c", "b", "", "x".repeat(historyEntryChars + 1)])).toEqual(["b", "a", "c"]);
  expect(recallList([], Array.from({ length: historyCap + 5 }, (_, i) => `p${i}`))).toHaveLength(historyCap);
});
