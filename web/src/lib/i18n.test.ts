import { describe, expect, it } from "vitest";
import { browserLocale, dictionaries } from "@/lib/i18n";

describe("browserLocale", () => {
  it("takes the first language inaba has, Chinese by script or region", () => {
    expect(browserLocale(["fr-FR", "ko-KR"])).toBe("ko");
    expect(browserLocale(["ja"])).toBe("ja");
    expect(browserLocale(["zh-CN"])).toBe("zh-Hans");
    expect(browserLocale(["zh-TW"])).toBe("zh-Hant");
    expect(browserLocale(["zh-HK"])).toBe("zh-Hant");
    expect(browserLocale(["zh-Hant-CN"])).toBe("zh-Hant");
    expect(browserLocale(["zh"])).toBe("zh-Hans");
    expect(browserLocale(["kok", "de"])).toBe("en");
  });
});

it("every language fills in the placeholders English does", () => {
  const holes = (text: string) => [...text.matchAll(/\{+\w+\}+/g)].map((m) => m[0]).sort();
  for (const [locale, words] of Object.entries(dictionaries))
    for (const [key, text] of Object.entries(dictionaries.en)) expect(holes(words[key as keyof typeof words]), `${locale} ${key}`).toEqual(holes(text));
});
