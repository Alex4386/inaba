import { describe, expect, it } from "vitest";
import { isBrowserTool } from "./tabs";

describe("isBrowserTool", () => {
  it("knows Playwright MCP's tools however a provider titles them", () => {
    for (const title of ["mcp__browser__browser_navigate", "browser.browser_click", "browser_snapshot (browser MCP Server)", "Browser_type"]) expect(isBrowserTool(title)).toBe(true);
    for (const title of ["Bash", "mcp__inaba__list_threads", "Read src/browser.ts", "open the browser", ""]) expect(isBrowserTool(title)).toBe(false);
  });
});
