import { expect, test } from "vitest";
import { allTabs, dockOf, type Docks, moveTab, newTabId, oneChanges, placeTab, withoutTab } from "@/lib/tabs";

test("moveTab drops a tab where it is dragged", () => {
  expect(moveTab(["a", "b", "c"], "a", "c")).toEqual(["b", "c", "a"]);
  expect(moveTab(["a", "b", "c"], "c", "a")).toEqual(["c", "a", "b"]);
  expect(moveTab(["a", "b", "c"], "a", "b")).toEqual(["b", "a", "c"]);
  expect(moveTab(["a", "b", "c"], "b", "b")).toEqual(["a", "b", "c"]);
});

test("newTabId opens any surface as many times as wanted", () => {
  expect(newTabId("shell", [])).toBe("shell-1");
  expect(newTabId("shell", ["shell-1", "shell-3"])).toBe("shell-2");
  expect(newTabId("browser", ["browser-1"])).toBe("browser-2");
  expect(newTabId("changes", ["changes-1"])).toBe("changes-1");
  expect(newTabId("changes", [])).toBe("changes-1");
  expect(newTabId("changes", ["shell-1", "changes-2"])).toBe("changes-2");
});

test("oneChanges drops a second changes tab from a saved layout", () => {
  const docks: Docks<"right" | "bottom"> = { right: { open: true, tabs: ["changes-1", "changes-2", "shell-1"], active: "changes-2" }, bottom: { open: false, tabs: ["changes-3"] } };
  const fixed = oneChanges(docks);
  expect(fixed.right).toEqual({ open: true, tabs: ["changes-1", "shell-1"], active: undefined });
  expect(fixed.bottom.tabs).toEqual([]);
  expect(oneChanges(fixed)).toEqual(fixed);
});

test("placeTab moves a tab between any docks and keeps one copy", () => {
  const docks: Docks<"right" | "bottom" | "left"> = { right: { open: false, tabs: ["shell-1", "files-1"], active: "files-1" }, bottom: { open: false, tabs: [] as string[] }, left: { open: true, tabs: ["browser-1"] } };
  const moved = placeTab(docks, "bottom", "files-1");
  expect(moved.bottom).toEqual({ open: true, tabs: ["files-1"], active: "files-1" });
  expect(moved.right).toEqual({ open: false, tabs: ["shell-1"], active: undefined });
  expect(dockOf(moved, "files-1")).toBe("bottom");
  expect(allTabs(placeTab(moved, "left", "files-1")).filter((t) => t === "files-1")).toHaveLength(1);
  expect(withoutTab(moved, "browser-1").left.tabs).toEqual([]);
});
