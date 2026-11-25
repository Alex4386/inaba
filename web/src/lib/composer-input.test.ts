import { describe, expect, it } from "vitest";
import { attachmentProblem, builtinCommands, formatBytes, maxFileBytes, maxAttachments, maxImageBytes, maxTotalBytes, parseBuiltin } from "./composer-input";

describe("parseBuiltin", () => {
  it("reads a built-in command and its text", () => {
    expect(parseBuiltin("/side hello there", builtinCommands)).toEqual({ command: "side", arg: "hello there" });
    expect(parseBuiltin("  /btw  multi\nline  ", builtinCommands)).toEqual({ command: "btw", arg: "multi\nline" });
    expect(parseBuiltin("/clear", builtinCommands)).toEqual({ command: "clear", arg: "" });
  });
  it("leaves other messages alone", () => {
    expect(parseBuiltin("/sidebar x", builtinCommands)).toBeUndefined();
    expect(parseBuiltin("please /side", builtinCommands)).toBeUndefined();
    expect(parseBuiltin("/review", builtinCommands)).toBeUndefined();
    expect(parseBuiltin("/compact", ["side"])).toBeUndefined();
  });
});

describe("attachmentProblem", () => {
  const none = { count: 0, bytes: 0 };
  it("applies the host's caps", () => {
    expect(attachmentProblem({ size: maxImageBytes, type: "image/png" }, none)).toBeUndefined();
    expect(attachmentProblem({ size: maxImageBytes + 1, type: "image/png" }, none)).toBe("imageTooLarge");
    expect(attachmentProblem({ size: maxImageBytes + 1, type: "application/zip" }, none)).toBeUndefined();
    expect(attachmentProblem({ size: maxFileBytes + 1, type: "" }, none)).toBe("fileTooLarge");
    expect(attachmentProblem({ size: 1, type: "text/plain" }, { count: 1, bytes: maxTotalBytes })).toBe("totalTooLarge");
    expect(attachmentProblem({ size: 1, type: "text/plain" }, { count: maxAttachments, bytes: 0 })).toBe("tooMany");
    expect(attachmentProblem({ size: 0, type: "text/plain" }, none)).toBe("empty");
  });
});

describe("formatBytes", () => {
  it("picks a readable unit", () => {
    expect(formatBytes(820, "en")).toBe("820 B");
    expect(formatBytes(3_400_000, "en")).toBe("3.4 MB");
  });
});
