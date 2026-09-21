import { describe, it, expect } from "vitest"
import { planKeystrokes, KEYBOARD_LAYOUTS, type Keystroke } from "./vnc-keymap.ts"

function codes(strokes: Keystroke[]): string[] {
  return strokes.map((s) => `${s.code}${s.shift ? "+shift" : ""}${s.altGr ? "+altgr" : ""}`)
}

describe("planKeystrokes on the US layout", () => {
  it("maps plain letters to their own keys", () => {
    const { keystrokes, unsupported } = planKeystrokes("abz", "en-us")

    expect(codes(keystrokes)).toEqual(["KeyA", "KeyB", "KeyZ"])
    expect(unsupported).toEqual([])
  })

  it("shifts capitals and the upper-register punctuation", () => {
    expect(codes(planKeystrokes("A!?", "en-us").keystrokes)).toEqual([
      "KeyA+shift",
      "Digit1+shift",
      "Slash+shift",
    ])
  })

  it("carries the Unicode code point as the keysym for Latin-1 characters", () => {
    expect(planKeystrokes("aA", "en-us").keystrokes.map((s) => s.keysym)).toEqual([0x61, 0x41])
  })

  it("prefers the main block over the 102nd key for characters reachable on both", () => {
    // "<" sits on IntlBackslash in the layout table and on Comma+Shift. A US
    // keyboard usually has no 102nd key, so the main block must win.
    expect(codes(planKeystrokes("<>", "en-us").keystrokes)).toEqual([
      "Comma+shift",
      "Period+shift",
    ])
  })
})

describe("planKeystrokes whitespace handling", () => {
  it("maps space, tab and newline to their own keys", () => {
    const { keystrokes } = planKeystrokes(" \t\n", "en-us")

    expect(codes(keystrokes)).toEqual(["Space", "Tab", "Enter"])
    expect(keystrokes.map((s) => s.keysym)).toEqual([0x20, 0xff09, 0xff0d])
  })

  it("collapses CRLF into a single Enter", () => {
    expect(codes(planKeystrokes("a\r\nb", "en-us").keystrokes)).toEqual(["KeyA", "Enter", "KeyB"])
  })

  it("treats a bare CR as Enter", () => {
    expect(codes(planKeystrokes("a\rb", "en-us").keystrokes)).toEqual(["KeyA", "Enter", "KeyB"])
  })
})

describe("planKeystrokes on non-US layouts", () => {
  it("follows QWERTZ key positions for German", () => {
    expect(codes(planKeystrokes("zy", "de").keystrokes)).toEqual(["KeyY", "KeyZ"])
  })

  it("reaches German characters that need AltGr", () => {
    expect(codes(planKeystrokes("@", "de").keystrokes)).toEqual(["KeyQ+altgr"])
  })

  it("follows AZERTY key positions for French", () => {
    expect(codes(planKeystrokes("aqzw", "fr").keystrokes)).toEqual([
      "KeyQ",
      "KeyA",
      "KeyW",
      "KeyZ",
    ])
  })

  it("types Cyrillic on the Russian layout", () => {
    expect(codes(planKeystrokes("фыва", "ru").keystrokes)).toEqual([
      "KeyA",
      "KeyS",
      "KeyD",
      "KeyF",
    ])
  })

  it("uses the 0x01000000 keysym range for characters outside Latin-1", () => {
    expect(planKeystrokes("ф", "ru").keystrokes.map((s) => s.keysym)).toEqual([0x01000444])
  })

  it("still types digits on the Russian layout", () => {
    expect(codes(planKeystrokes("12", "ru").keystrokes)).toEqual(["Digit1", "Digit2"])
  })
})

describe("planKeystrokes unsupported characters", () => {
  it("reports characters the layout cannot reach and skips them", () => {
    const { keystrokes, unsupported } = planKeystrokes("aфb", "en-us")

    expect(codes(keystrokes)).toEqual(["KeyA", "KeyB"])
    expect(unsupported).toEqual(["ф"])
  })

  it("reports each unsupported character once, in order of appearance", () => {
    expect(planKeystrokes("яф-я", "en-us").unsupported).toEqual(["я", "ф"])
  })

  it("reports control characters rather than dropping them silently", () => {
    expect(planKeystrokes("a\u0007b", "en-us").unsupported).toEqual(["\u0007"])
  })

  it("counts an astral code point as one unsupported character", () => {
    expect(planKeystrokes("a🙂", "en-us").unsupported).toEqual(["🙂"])
  })
})

const PRINTABLE_ASCII = Array.from({ length: 0x7f - 0x20 }, (_, i) =>
  String.fromCharCode(0x20 + i),
).join("")

describe("layout tables", () => {
  it.each(["en-us", "de", "fr"] as const)("reaches all of printable ASCII on %s", (layout) => {
    expect(planKeystrokes(PRINTABLE_ASCII, layout).unsupported).toEqual([])
  })

  it("cannot reach Latin letters on the Russian layout", () => {
    // Not a gap in the table: a guest sitting in the Cyrillic layout has no
    // Latin letter on any key. Pasting them needs the guest switched to a
    // Latin layout, which the console cannot do on the user's behalf.
    const { unsupported } = planKeystrokes(PRINTABLE_ASCII, "ru")

    expect(unsupported.join("")).toBe(
      "#$&'<>@ABCDEFGHIJKLMNOPQRSTUVWXYZ[]^`abcdefghijklmnopqrstuvwxyz{}~",
    )
  })

  it("declares exactly the layouts the tables carry", () => {
    expect(KEYBOARD_LAYOUTS).toEqual(["en-us", "de", "fr", "ru"])
  })
})
