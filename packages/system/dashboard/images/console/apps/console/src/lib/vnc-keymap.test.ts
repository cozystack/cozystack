import { describe, it, expect } from "vitest"
import { planKeystrokes, type Keystroke } from "./vnc-keymap.ts"

function codes(strokes: Keystroke[]): string[] {
  return strokes.map((s) => `${s.code}${s.shift ? "+shift" : ""}`)
}

describe("planKeystrokes", () => {
  it("maps plain letters to their own keys", () => {
    const { keystrokes, unsupported } = planKeystrokes("abz")

    expect(codes(keystrokes)).toEqual(["KeyA", "KeyB", "KeyZ"])
    expect(unsupported).toEqual([])
  })

  it("shifts capitals and the upper-register punctuation", () => {
    expect(codes(planKeystrokes("A!?").keystrokes)).toEqual([
      "KeyA+shift",
      "Digit1+shift",
      "Slash+shift",
    ])
  })

  it("carries the Unicode code point as the keysym", () => {
    expect(planKeystrokes("aA").keystrokes.map((s) => s.keysym)).toEqual([0x61, 0x41])
  })

  it("prefers the main block over the 102nd key for characters reachable on both", () => {
    // "<" sits on IntlBackslash in the layout table and on Comma+Shift. A US
    // keyboard usually has no 102nd key, so the main block must win.
    expect(codes(planKeystrokes("<>").keystrokes)).toEqual(["Comma+shift", "Period+shift"])
  })

  it("reaches every printable ASCII character", () => {
    const ascii = Array.from({ length: 0x7f - 0x20 }, (_, i) => String.fromCharCode(0x20 + i)).join(
      "",
    )

    expect(planKeystrokes(ascii).unsupported).toEqual([])
  })
})

describe("planKeystrokes whitespace handling", () => {
  it("maps space, tab and newline to their own keys", () => {
    const { keystrokes } = planKeystrokes(" \t\n")

    expect(codes(keystrokes)).toEqual(["Space", "Tab", "Enter"])
    expect(keystrokes.map((s) => s.keysym)).toEqual([0x20, 0xff09, 0xff0d])
  })

  it("collapses CRLF into a single Enter", () => {
    expect(codes(planKeystrokes("a\r\nb").keystrokes)).toEqual(["KeyA", "Enter", "KeyB"])
  })

  it("treats a bare CR as Enter", () => {
    expect(codes(planKeystrokes("a\rb").keystrokes)).toEqual(["KeyA", "Enter", "KeyB"])
  })
})

describe("planKeystrokes unsupported characters", () => {
  it("reports characters the layout cannot reach and skips them", () => {
    const { keystrokes, unsupported } = planKeystrokes("aфb")

    expect(codes(keystrokes)).toEqual(["KeyA", "KeyB"])
    expect(unsupported).toEqual(["ф"])
  })

  it("reports each unsupported character once, in order of appearance", () => {
    expect(planKeystrokes("яф-я").unsupported).toEqual(["я", "ф"])
  })

  it("reports control characters rather than dropping them silently", () => {
    expect(planKeystrokes("a\u0007b").unsupported).toEqual(["\u0007"])
  })

  it("counts an astral code point as one unsupported character", () => {
    expect(planKeystrokes("a🙂").unsupported).toEqual(["🙂"])
  })
})
