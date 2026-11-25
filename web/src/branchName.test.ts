import { expect, test, vi } from "vitest";

// NewThread pulls in the presence client, which reads the page's address when it loads.
vi.stubGlobal("location", new URL("http://localhost/ui/"));
const { validBranchName } = await import("./NewThread");

test("validBranchName takes names git takes", () => {
  for (const name of ["main", "feature/login", "fix-1.2", "user/a_b", "v1@2"]) expect(validBranchName(name), name).toBe(true);
});

test("validBranchName refuses names git refuses", () => {
  for (const name of ["", "@", "a b", "a..b", "a~1", "a^", "a:b", "a?", "a*", "a[b", "a\\b", "a@{1}", "-a", "/a", "a/", ".a", "a/.b", "a.", "a.lock", "a.lock/b", "a//b", "a\tb"])
    expect(validBranchName(name), name).toBe(false);
});
