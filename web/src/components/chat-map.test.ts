import { describe, expect, it } from "vitest";
import { tickOf } from "./chat-map";

describe("tickOf", () => {
  it("gives every tick its first exchange, and every exchange a tick", () => {
    for (const [exchanges, count] of [[2, 2], [7, 3], [100, 30], [1000, 41], [41, 41]]) {
      for (let k = 0; k < count; k++) expect(tickOf(Math.ceil((k * exchanges) / count), exchanges, count)).toBe(k);
      expect(tickOf(0, exchanges, count)).toBe(0);
      expect(tickOf(exchanges - 1, exchanges, count)).toBe(count - 1);
    }
  });
});
