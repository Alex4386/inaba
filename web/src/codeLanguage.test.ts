import { expect, test } from "vitest";
import { codeLanguage } from "@/components/code-block";

test("codeLanguage reads the fenced block's language", () => {
  expect(codeLanguage(["language-ts"])).toBe("ts");
  expect(codeLanguage(["foo", "language-Mermaid"])).toBe("mermaid");
  expect(codeLanguage("language-go extra")).toBe("go");
  expect(codeLanguage(undefined)).toBe("");
  expect(codeLanguage([])).toBe("");
});
