import { describe, expect, it } from "vitest";
import { type Heard, type Input, translator } from "./ime";

const none = { altKey: false, ctrlKey: false, metaKey: false, shiftKey: false };
// A key an input method takes: browsers report it with key code 229.
const imeKey = (type: "keydown" | "keyup", key: string, isComposing: boolean): Heard => ({ type, key, code: "", keyCode: 229, isComposing, ...none });
const key = (type: "keydown" | "keyup", key: string, code: string, keyCode: number, mods: Partial<typeof none> = {}): Heard => ({ type, key, code, keyCode, isComposing: false, ...none, ...mods });
const update = (data: string): Heard => ({ type: "compositionupdate", data });
const end = (data: string): Heard => ({ type: "compositionend", data });
const composing = (data: string): Heard => ({ type: "input", inputType: "insertCompositionText", data, isComposing: true });
const typed = (data: string): Heard => ({ type: "input", inputType: "insertText", data, isComposing: false });

/** What the page gets for a run of events, as text: c(…) a composition, t(…) text, k↓/k↑ keys. */
function run(events: Heard[]): string[] {
  const t = translator();
  return events.flatMap(t).map((i: Input) =>
    i.case === "composition" ? `c(${i.value})` : i.case === "text" ? `t(${i.value})` : `k${i.value.type === "down" ? "↓" : "↑"}${i.value.key}${i.value.commands.length ? `[${i.value.commands}]` : ""}`,
  );
}

/** One keystroke of an input method: its press, the composition as it now stands, its release. */
const stroke = (k: string, composition: string, first = false) => [imeKey("keydown", k, !first), update(composition), composing(composition), imeKey("keyup", k, true)];

describe("typing through an input method", () => {
  it("composes Hangul syllable by syllable", () => {
    // 한글, then a space: the ㄱ that cannot join 한 ends it and starts the next syllable.
    expect(
      run([
        ...stroke("ㅎ", "ㅎ", true),
        ...stroke("ㅏ", "하"),
        ...stroke("ㄴ", "한"),
        imeKey("keydown", "ㄱ", true),
        end("한"),
        update("ㄱ"),
        composing("ㄱ"),
        imeKey("keyup", "ㄱ", true),
        ...stroke("ㅡ", "그"),
        ...stroke("ㄹ", "글"),
        imeKey("keydown", " ", true),
        end("글"),
        typed(" "),
        key("keyup", " ", "Space", 32),
      ]),
    ).toEqual(["c(ㅎ)", "c(하)", "c(한)", "t(한)", "c(ㄱ)", "c(그)", "c(글)", "t(글)", "t( )"]);
  });

  it("takes a Hangul backspace as the input method's own", () => {
    expect(run([...stroke("ㅎ", "ㅎ", true), ...stroke("ㅏ", "하"), imeKey("keydown", "Backspace", true), update("ㅎ"), composing("ㅎ")])).toEqual(["c(ㅎ)", "c(하)", "c(ㅎ)"]);
  });

  it("turns romaji into kana, then kana into kanji", () => {
    expect(
      run([
        ...stroke("n", "n", true),
        ...stroke("i", "に"),
        ...stroke("h", "にh"),
        ...stroke("o", "にほ"),
        ...stroke("n", "にほn"),
        ...stroke("n", "にほん"),
        // Space converts, Enter accepts the conversion; both keys are the input method's.
        imeKey("keydown", " ", true),
        update("日本"),
        composing("日本"),
        imeKey("keyup", " ", true),
        imeKey("keydown", "Enter", true),
        end("日本"),
        // The release of that Enter comes after the composition, looking like any other.
        key("keyup", "Enter", "Enter", 13),
      ]),
    ).toEqual(["c(n)", "c(に)", "c(にh)", "c(にほ)", "c(にほn)", "c(にほん)", "c(日本)", "t(日本)"]);
  });

  it("shows pinyin while it is typed, then the characters chosen", () => {
    const letters = ["z", "zh", "zho", "zhon", "zhong", "zhong w", "zhong we", "zhong wen"];
    expect(
      run([
        ...letters.flatMap((c, i) => stroke(c.at(-1)!, c, i === 0)),
        // A candidate is picked with Space or its number.
        imeKey("keydown", "1", true),
        end("中文"),
        imeKey("keyup", "1", false),
      ]),
    ).toEqual([...letters.map((c) => `c(${c})`), "t(中文)"]);
  });

  it("clears a composition given up", () => {
    expect(run([...stroke("ㅎ", "ㅎ", true), imeKey("keydown", "Escape", true), end("")])).toEqual(["c(ㅎ)", "c()"]);
  });
});

describe("typing without one", () => {
  it("sends characters as text, and acting keys as keys", () => {
    expect(
      run([
        key("keydown", "h", "KeyH", 72),
        typed("h"),
        key("keyup", "h", "KeyH", 72),
        key("keydown", "I", "KeyI", 73, { shiftKey: true }),
        typed("I"),
        key("keyup", "I", "KeyI", 73, { shiftKey: true }),
        key("keydown", "Backspace", "Backspace", 8),
        key("keyup", "Backspace", "Backspace", 8),
        key("keydown", "Enter", "Enter", 13),
        key("keyup", "Enter", "Enter", 13),
      ]),
    ).toEqual(["t(h)", "t(I)", "k↓Backspace", "k↑Backspace", "k↓Enter", "k↑Enter"]);
  });

  it("types Enter as a carriage return, which is what submits a form", () => {
    const [enter] = translator()(key("keydown", "Enter", "Enter", 13));
    expect(enter).toMatchObject({ case: "key", value: { text: "\r", keyCode: 13 } });
  });

  it("sends shortcuts as the editing commands headless Chrome follows", () => {
    expect(
      run([
        key("keydown", "a", "KeyA", 65, { ctrlKey: true }),
        key("keyup", "a", "KeyA", 65, { ctrlKey: true }),
        key("keydown", "z", "KeyZ", 90, { metaKey: true, shiftKey: true }),
        key("keydown", "c", "KeyC", 67, { metaKey: true }),
      ]),
    ).toEqual(["k↓a[selectAll]", "k↑a", "k↓z[redo]", "k↓c[copy]"]);
  });

  it("leaves pasting to the paste event, which brings this machine's clipboard", () => {
    expect(run([key("keydown", "v", "KeyV", 86, { metaKey: true }), key("keyup", "v", "KeyV", 86, { metaKey: true })])).toEqual([]);
  });
});
