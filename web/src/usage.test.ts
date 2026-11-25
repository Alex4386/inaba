import { expect, test } from "vitest";
import { seriesTones } from "@/lib/usage";

test("a series keeps its color whatever its rank, and the sixth folds into other", () => {
  const a = seriesTones(["codex", "claude"]);
  const b = seriesTones(["claude", "codex", "claude"]);
  expect(a.get("claude")).toEqual(b.get("claude"));
  expect(a.get("claude")?.bg).toBe("bg-chart-1");
  expect(a.get("codex")?.bg).toBe("bg-chart-2");
  const many = seriesTones(["a", "b", "c", "d", "e", "f"]);
  expect(many.get("e")?.other).toBe(false);
  expect(many.get("f")?.other).toBe(true);
});
