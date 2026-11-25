import { expect, test } from "vitest";
import { parseDelimited } from "@/lib/csv";

test("parseDelimited splits plain rows", () => {
  expect(parseDelimited("a,b\n1,2\n", ",")).toEqual([
    ["a", "b"],
    ["1", "2"],
  ]);
  expect(parseDelimited("a\tb\r\n1\t2", "\t")).toEqual([
    ["a", "b"],
    ["1", "2"],
  ]);
});

test("parseDelimited keeps separators, quotes and line breaks inside quotes", () => {
  expect(parseDelimited('name,note\n"Kim, J","said ""hi""\nthen left"\n', ",")).toEqual([
    ["name", "note"],
    ["Kim, J", 'said "hi"\nthen left'],
  ]);
});

test("parseDelimited keeps empty fields", () => {
  expect(parseDelimited(",a,\n\n", ",")).toEqual([["", "a", ""], [""]]);
  expect(parseDelimited("", ",")).toEqual([]);
});
