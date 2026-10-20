import { describe, expect, test } from "bun:test";
import { segments, type Span } from "./segments";

const span = (start: number, end: number): Span => ({ start, end, type: "SECRET", rule: "r" });

describe("segments", () => {
  test("an empty text has no segments", () => {
    expect(segments("", [])).toEqual([]);
  });

  test("a text without spans is one plain segment", () => {
    expect(segments("abc", [])).toEqual([{ text: "abc" }]);
  });

  test("a span in the middle makes three segments", () => {
    const s = span(2, 4);
    expect(segments("abcdef", [s])).toEqual([{ text: "ab" }, { text: "cd", span: s }, { text: "ef" }]);
  });

  test("a span from the start to the end leaves no empty segment", () => {
    const s = span(0, 3);
    expect(segments("abc", [s])).toEqual([{ text: "abc", span: s }]);
  });

  test("adjacent spans leave no empty segment between them", () => {
    const a = span(1, 3);
    const b = span(3, 5);
    expect(segments("abcdef", [a, b])).toEqual([
      { text: "a" },
      { text: "bc", span: a },
      { text: "de", span: b },
      { text: "f" },
    ]);
  });

  // An ignored span must not move the cursor: the good span after it is still cut out.
  test.each([
    ["starts before the text", span(-1, 2)],
    ["ends after the text", span(3, 9)],
    ["is empty", span(3, 3)],
    ["is reversed", span(4, 2)],
  ])("a span that %s is ignored", (_, bad) => {
    const good = span(2, 4);
    expect(segments("abcdef", [bad])).toEqual([{ text: "abcdef" }]);
    expect(segments("abcdef", [bad, good])).toEqual([{ text: "ab" }, { text: "cd", span: good }, { text: "ef" }]);
  });

  test("a span that overlaps the previous one is ignored", () => {
    const first = span(1, 4);
    expect(segments("abcdef", [first, span(2, 5)])).toEqual([
      { text: "a" },
      { text: "bcd", span: first },
      { text: "ef" },
    ]);
  });

  test("offsets are UTF-16 units, as in String.slice", () => {
    const s = span(2, 4); // the emoji is a surrogate pair: two units
    expect(segments("😀abc", [s])).toEqual([{ text: "😀" }, { text: "ab", span: s }, { text: "c" }]);
  });

  test("a NUL is shown as U+2400 and the boundaries stay where they were", () => {
    const s = span(1, 3);
    expect(segments("a\0b", [])).toEqual([{ text: "a␀b" }]);
    expect(segments("x\0yz", [s])).toEqual([{ text: "x" }, { text: "␀y", span: s }, { text: "z" }]);
  });
});
