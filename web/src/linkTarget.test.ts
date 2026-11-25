import { expect, test } from "vitest";
import { linkTarget } from "@/components/file-preview";

test("linkTarget resolves relative links against the file's folder", () => {
  expect(linkTarget("docs/README.md", "a.md")).toBe("docs/a.md");
  expect(linkTarget("docs/README.md", "./sub/b.md#part")).toBe("docs/sub/b.md");
  expect(linkTarget("docs/README.md", "../LICENSE?x=1")).toBe("LICENSE");
  expect(linkTarget("README.md", "/src/my%20file.ts")).toBe("src/my file.ts");
});

test("linkTarget leaves everything else alone", () => {
  expect(linkTarget("README.md", "https://example.com")).toBeUndefined();
  expect(linkTarget("README.md", "mailto:a@b.c")).toBeUndefined();
  expect(linkTarget("README.md", "//cdn.example.com/x")).toBeUndefined();
  expect(linkTarget("README.md", "#usage")).toBeUndefined();
  expect(linkTarget("README.md", "../outside.md")).toBeUndefined();
});
