import { expect, test } from "vitest";
import { isTestPath } from "@/lib/diff-files";

test("isTestPath spots test files by their usual names", () => {
  for (const path of ["internal/host/git_test.go", "web/src/moveTab.test.ts", "a/b.test.tsx", "x.spec.js", "test/fixture.json", "pkg/tests/a.go", "src/__tests__/a.ts"]) {
    expect(isTestPath(path), path).toBe(true);
  }
  for (const path of ["internal/host/git.go", "web/src/testing.ts", "contest/a.go", "src/latest.ts", "attest.go"]) {
    expect(isTestPath(path), path).toBe(false);
  }
});
