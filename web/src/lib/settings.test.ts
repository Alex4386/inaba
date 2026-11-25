import { afterEach, expect, test, vi } from "vitest";
import { accentLowContrast, contrastRatio, defaultTheme, luminance, parseHex, readableForeground, setThemePrefs, themePrefs } from "@/lib/settings";

afterEach(() => vi.unstubAllGlobals());

const stubStorage = (saved: string | null) => vi.stubGlobal("localStorage", { getItem: () => saved });

test("themePrefs falls back per field on bad storage", () => {
  stubStorage("not json");
  expect(themePrefs()).toEqual(defaultTheme);
  stubStorage("null");
  expect(themePrefs()).toEqual(defaultTheme);
  stubStorage(JSON.stringify({ preset: "evil", accent: "red", radius: 9 }));
  expect(themePrefs()).toEqual(defaultTheme);
  stubStorage(JSON.stringify({ preset: "warm", accent: "2563EB", radius: -1 }));
  expect(themePrefs()).toEqual({ ...defaultTheme, preset: "warm", accent: "#2563eb" });
});

test("theme changes hold in memory while storage is blocked", () => {
  const blocked = () => {
    throw new Error("blocked");
  };
  vi.stubGlobal("localStorage", { getItem: blocked, setItem: blocked, removeItem: blocked });
  // The node test environment has no DOM; applyTheme only needs the root's dataset and style.
  vi.stubGlobal("document", { documentElement: { dataset: {}, style: { setProperty() {}, removeProperty() {} } } });
  setThemePrefs({ preset: "cool", radius: 1 });
  expect(themePrefs()).toEqual({ ...defaultTheme, preset: "cool", radius: 1 });
  setThemePrefs();
  expect(themePrefs()).toEqual(defaultTheme);
});

test("accentLowContrast flags accents lost on either background", () => {
  expect(contrastRatio("#000000", "#ffffff")).toBeCloseTo(21);
  expect(accentLowContrast("#2563eb")).toBe(false);
  expect(accentLowContrast("#fde047")).toBe(true);
  expect(accentLowContrast("#1e1b4b")).toBe(true);
});

test("luminance spans black to white", () => {
  expect(luminance("#000000")).toBe(0);
  expect(luminance("#ffffff")).toBeCloseTo(1);
  expect(luminance("#808080")).toBeCloseTo(0.2159, 3);
});

test("readableForeground picks the side with more contrast", () => {
  expect(readableForeground("#000000")).toBe("#fafafa");
  expect(readableForeground("#ffffff")).toBe("#0a0a0a");
  expect(readableForeground("#2563eb")).toBe("#fafafa");
  expect(readableForeground("#facc15")).toBe("#0a0a0a");
  expect(readableForeground("#16a34a")).toBe("#0a0a0a");
});

test("parseHex accepts six hex digits only", () => {
  expect(parseHex(" 2563EB ")).toBe("#2563eb");
  expect(parseHex("#abcdef")).toBe("#abcdef");
  expect(parseHex("#abc")).toBeUndefined();
  expect(parseHex("red")).toBeUndefined();
  expect(parseHex("#12345g")).toBeUndefined();
});
