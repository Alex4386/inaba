import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { unreachable } from "./errors";

describe("unreachable", () => {
  it("knows a call that never reached the host from one the host answered", () => {
    for (const e of [
      new ConnectError("Failed to fetch", Code.Unknown),
      new ConnectError("Load failed", Code.Unknown),
      new ConnectError("NetworkError when attempting to fetch resource.", Code.Unknown),
      new ConnectError("the host is not connected", Code.Unavailable),
      new ConnectError("the host did not answer", Code.Unavailable),
      new TypeError("The direct connection failed."),
    ])
      expect(unreachable(e)).toBe(true);
    for (const e of [
      new ConnectError("the browser did not answer: timeout", Code.Unavailable),
      new ConnectError("could not start claude", Code.Unavailable),
      new ConnectError("Failed to fetch", Code.PermissionDenied),
      // The host's own words that only mention a network are its to show.
      new ConnectError("claude: network error talking to the API", Code.Unknown),
      new ConnectError("the host is not connected to the VPN", Code.Unavailable),
      new Error("Failed to fetch"),
    ])
      expect(unreachable(e)).toBe(false);
  });
});
