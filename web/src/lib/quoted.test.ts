import { describe, expect, it } from "vitest";
import { quoted } from "./utils";

describe("quoted", () => {
  it("leaves plain arguments and quotes the rest for a POSIX shell", () => {
    expect(["claude", "--resume", "0b1c-2d3e"].map(quoted).join(" ")).toBe("claude --resume 0b1c-2d3e");
    expect(quoted("two words")).toBe("'two words'");
    expect(quoted("it's")).toBe(`'it'\\''s'`);
    expect(quoted("")).toBe("''");
  });
});
