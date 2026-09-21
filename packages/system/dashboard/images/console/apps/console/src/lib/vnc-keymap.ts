/**
 * Turn text into the key presses that reproduce it on a guest keyboard.
 *
 * KubeVirt's VNC console carries no clipboard channel: qemu only forwards
 * RFB cut-text to a `qemu-vdagent` chardev, and virt-launcher does not add
 * one (`pkg/virt-launcher/virtwrap/converter/compute/graphics.go` emits a bare
 * `<graphics type='vnc'>`). Pasting therefore means typing, one key at a time,
 * on the layout the guest currently has active.
 *
 * The layout rows below were generated from @kubevirt-ui-ext/vnc-keymaps
 * (MIT, github.com/kubevirt-ui/vnc-keymaps), itself derived from the X11 xkb
 * data, and cross-checked against noVNC's DOM-code-to-scancode table.
 */

export const KEYBOARD_LAYOUTS = ["en-us", "de", "fr", "ru"] as const

export type KeyboardLayout = (typeof KEYBOARD_LAYOUTS)[number]

export const LAYOUT_LABELS: Record<KeyboardLayout, string> = {
  "en-us": "English (US)",
  de: "German (no dead keys)",
  fr: "French (no dead keys)",
  ru: "Russian",
}

export interface Keystroke {
  /** DOM code of the physical key, which noVNC maps to an XT scancode. */
  code: string
  keysym: number
  shift: boolean
  altGr: boolean
}

export interface KeystrokePlan {
  keystrokes: Keystroke[]
  /** Distinct characters no key on this layout produces, in input order. */
  unsupported: string[]
}

// The 48 character keys of a 102-key keyboard, in row order. Every layout row
// string below is indexed by this array, one code point per key.
const KEYS = [
  "Digit1", "Digit2", "Digit3", "Digit4", "Digit5", "Digit6",
  "Digit7", "Digit8", "Digit9", "Digit0", "Minus", "Equal",
  "KeyQ", "KeyW", "KeyE", "KeyR", "KeyT", "KeyY",
  "KeyU", "KeyI", "KeyO", "KeyP", "BracketLeft", "BracketRight",
  "Backslash", "KeyA", "KeyS", "KeyD", "KeyF", "KeyG",
  "KeyH", "KeyJ", "KeyK", "KeyL", "Semicolon", "Quote",
  "Backquote", "IntlBackslash", "KeyZ", "KeyX", "KeyC", "KeyV",
  "KeyB", "KeyN", "KeyM", "Comma", "Period", "Slash",
]

// The 102nd key is absent from most keyboards this console is driven from, and
// on several layouts it duplicates a character the main block already carries.
// It is filled in last so the main block always wins a tie.
const INTL_BACKSLASH = KEYS.indexOf("IntlBackslash")

interface LayoutRows {
  base: string
  shift: string
  altGr: string
}

const LAYOUT_ROWS: Record<KeyboardLayout, LayoutRows> = {
  "en-us": {
    base: "1234567890-=qwertyuiop[]\\asdfghjkl;'`<zxcvbnm,./",
    shift: "!@#$%^&*()_+QWERTYUIOP{}|ASDFGHJKL:\"~>ZXCVBNM<>?",
    altGr: "                                     |          ",
  },
  de: {
    base: "1234567890ß´qwertzuiopü+#asdfghjklöä^<yxcvbnm,.-",
    shift: "!\"§$%&/()=?`QWERTZUIOPÜ*'ASDFGHJKLÖÄ°>YXCVBNM;:_",
    altGr: "¹²³¼½¬{[]}\\¸@ €¶ŧ←↓→øþ¨~’æ ðđŋħʒĸł˝^¬|»«¢„“”µ· –",
  },
  fr: {
    base: "&é\"'(-è_çà)=azertyuiop^$*qsdfghjklmù²<wxcvbn,;:!",
    shift: "1234567890°+AZERTYUIOP¨£µQSDFGHJKLM%~>WXCVBN?./§",
    altGr: "¹~#{[|`\\^@]}æ«€¶ŧ←↓→øþ√¤`@ßðđŋħʒĸłµ^¬|ł»¢„“”´ ·…",
  },
  ru: {
    base: "1234567890-=йцукенгшщзхъ\\фывапролджэё/ячсмитьбю.",
    shift: "!\"№;%:?*()_+ЙЦУКЕНГШЩЗХЪ/ФЫВАПРОЛДЖЭЁ|ЯЧСМИТЬБЮ,",
    altGr: "                                     |          ",
  },
}

const XK_RETURN = 0xff0d
const XK_TAB = 0xff09
const XK_SPACE = 0x20

export const XK_SHIFT_L = 0xffe1
export const XK_ISO_LEVEL3_SHIFT = 0xfe03

/** X11 encodes anything past Latin-1 as the code point in the Unicode plane. */
function keysymOf(char: string): number {
  const codePoint = char.codePointAt(0) ?? 0
  return codePoint <= 0xff ? codePoint : 0x01000000 + codePoint
}

type CharKeys = Map<string, Keystroke>

const charKeysCache = new Map<KeyboardLayout, CharKeys>()

function buildCharKeys(layout: KeyboardLayout): CharKeys {
  const rows = LAYOUT_ROWS[layout]
  const map: CharKeys = new Map()

  const add = (index: number, char: string, shift: boolean, altGr: boolean) => {
    if (char === " " || map.has(char)) return
    map.set(char, { code: KEYS[index], keysym: keysymOf(char), shift, altGr })
  }

  // Fewest modifiers first, so a character reachable without AltGr never goes
  // through it.
  const levels = [
    [rows.base, false, false],
    [rows.shift, true, false],
    [rows.altGr, false, true],
  ] as const

  for (const [row, shift, altGr] of levels) {
    const chars = [...row]
    for (let i = 0; i < chars.length; i++) {
      if (i === INTL_BACKSLASH) continue
      add(i, chars[i], shift, altGr)
    }
  }

  // The 102nd key only after every other key of every level, so it is used
  // only for characters the main block cannot reach at all.
  for (const [row, shift, altGr] of levels) {
    add(INTL_BACKSLASH, [...row][INTL_BACKSLASH], shift, altGr)
  }

  return map
}

function charKeysFor(layout: KeyboardLayout): CharKeys {
  const cached = charKeysCache.get(layout)
  if (cached) return cached
  const built = buildCharKeys(layout)
  charKeysCache.set(layout, built)
  return built
}

const FIXED_KEYS: Record<string, Keystroke> = {
  " ": { code: "Space", keysym: XK_SPACE, shift: false, altGr: false },
  "\t": { code: "Tab", keysym: XK_TAB, shift: false, altGr: false },
  "\n": { code: "Enter", keysym: XK_RETURN, shift: false, altGr: false },
}

/**
 * Plan the key presses for `text` on `layout`. Characters the layout cannot
 * produce are collected in `unsupported` instead of being typed as something
 * else — a paste that silently drops or mistypes characters is worse than one
 * that says what it could not send.
 */
export function planKeystrokes(text: string, layout: KeyboardLayout): KeystrokePlan {
  const charKeys = charKeysFor(layout)
  const keystrokes: Keystroke[] = []
  const unsupported: string[] = []
  const seenUnsupported = new Set<string>()

  // CR, CRLF and LF all mean one Enter.
  const normalised = text.replace(/\r\n?/g, "\n")

  for (const char of normalised) {
    const stroke = FIXED_KEYS[char] ?? charKeys.get(char)
    if (stroke) {
      keystrokes.push(stroke)
      continue
    }
    if (!seenUnsupported.has(char)) {
      seenUnsupported.add(char)
      unsupported.push(char)
    }
  }

  return { keystrokes, unsupported }
}
