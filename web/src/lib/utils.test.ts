import { expect, test } from "vitest";
import { pathNames, shortPath } from "./utils";

test("a path is cut to its last two names, with either slash", () => {
  expect(shortPath("/home/me/src/inaba")).toBe("…/src/inaba");
  expect(shortPath("C:\\Users\\me\\inaba\\")).toBe("…/me/inaba");
  expect(shortPath("/srv/inaba")).toBe("/srv/inaba");
  expect(shortPath("/")).toBe("/");
  expect(pathNames("C:\\Users/me")).toEqual(["C:", "Users", "me"]);
});
