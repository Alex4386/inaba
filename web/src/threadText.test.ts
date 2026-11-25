import { expect, test } from "vitest";
import { matchOffsets, planMarkdown, quoteMarkdown } from "@/lib/thread-text";

test("matchOffsets finds every match case-insensitively without overlaps", () => {
  expect(matchOffsets("Foo foo FOO", "foo")).toEqual([0, 4, 8]);
  expect(matchOffsets("aaaa", "aa")).toEqual([0, 2]);
  expect(matchOffsets("abc", "")).toEqual([]);
  expect(matchOffsets("abc", "x")).toEqual([]);
  expect(matchOffsets("İx x", "x")).toEqual([1, 3]);
});

test("quoteMarkdown quotes each line and leaves room for the reply", () => {
  expect(quoteMarkdown("  one\n\ntwo\r\nthree \n")).toBe("> one\n>\n> two\n> three\n\n");
});

test("planMarkdown writes a task list", () => {
  expect(
    planMarkdown([
      { content: "Read", status: "completed" },
      { content: "Edit", status: "in_progress" },
      { content: "Test", status: "pending" },
    ]),
  ).toBe("- [x] Read\n- [ ] Edit (in progress)\n- [ ] Test");
});
