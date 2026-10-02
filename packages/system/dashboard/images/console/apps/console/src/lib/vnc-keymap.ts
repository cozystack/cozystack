/**
 * Turn text into the key presses that reproduce it on a US guest keyboard.
 *
 * KubeVirt's VNC console carries no clipboard channel: qemu only forwards RFB
 * cut-text to a `qemu-vdagent` chardev, and virt-launcher does not add one
 * (`pkg/virt-launcher/virtwrap/converter/compute/graphics.go` emits a bare
 * `<graphics type='vnc'>`). Pasting therefore means typing, one key at a time.
 *
 * What a scancode means is decided by the guest, and nothing in the RFB stream
 * says which layout it has active, so this assumes US — the layout a default
 * cloud image boots with. On a guest sitting in another layout the characters
 * would arrive wrong, which is why `unsupported` exists and why the serial
 * console, where text travels as bytes, is the better road for text.
 *
 * The table below was generated from @kubevirt-ui-ext/vnc-keymaps (MIT,
 * github.com/kubevirt-ui/vnc-keymaps), itself derived from the X11 xkb data,
 * and cross-checked against noVNC's DOM-code-to-scancode table.
 */

export interface Keystroke {
  /** DOM code of the physical key, which noVNC maps to an XT scancode. */
  code: string
  keysym: number
  shift: boolean
}

export interface KeystrokePlan {
  keystrokes: Keystroke[]
  /** Distinct characters no key on this layout produces, in input order. */
  unsupported: string[]
}

// The 48 character keys of a 102-key keyboard, in row order. The row strings
// below are indexed by this array, one code point per key.
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
// it duplicates characters the main block already carries. It is filled in
// last so the main block always wins a tie.
const INTL_BACKSLASH = KEYS.indexOf("IntlBackslash")

const BASE_ROW = "1234567890-=qwertyuiop[]\\asdfghjkl;'`<zxcvbnm,./"
const SHIFT_ROW = "!@#$%^&*()_+QWERTYUIOP{}|ASDFGHJKL:\"~>ZXCVBNM<>?"

const XK_RETURN = 0xff0d
const XK_TAB = 0xff09
const XK_SPACE = 0x20

export const XK_SHIFT_L = 0xffe1

/** X11 encodes anything past Latin-1 as the code point in the Unicode plane. */
function keysymOf(char: string): number {
  const codePoint = char.codePointAt(0) ?? 0
  return codePoint <= 0xff ? codePoint : 0x01000000 + codePoint
}

function buildCharKeys(): Map<string, Keystroke> {
  const map = new Map<string, Keystroke>()

  const add = (index: number, char: string, shift: boolean) => {
    if (char === " " || map.has(char)) return
    map.set(char, { code: KEYS[index], keysym: keysymOf(char), shift })
  }

  const levels = [
    [BASE_ROW, false],
    [SHIFT_ROW, true],
  ] as const

  for (const [row, shift] of levels) {
    const chars = [...row]
    for (let i = 0; i < chars.length; i++) {
      if (i === INTL_BACKSLASH) continue
      add(i, chars[i], shift)
    }
  }

  // The 102nd key only after every other key of every level, so it is used
  // only for characters the main block cannot reach at all.
  for (const [row, shift] of levels) {
    add(INTL_BACKSLASH, [...row][INTL_BACKSLASH], shift)
  }

  return map
}

const CHAR_KEYS = buildCharKeys()

const FIXED_KEYS: Record<string, Keystroke> = {
  " ": { code: "Space", keysym: XK_SPACE, shift: false },
  "\t": { code: "Tab", keysym: XK_TAB, shift: false },
  "\n": { code: "Enter", keysym: XK_RETURN, shift: false },
}

/**
 * Plan the key presses for `text`. Characters the layout cannot produce are
 * collected in `unsupported` instead of being typed as something else — a
 * paste that silently drops or mistypes characters is worse than one that
 * says what it could not send.
 */
export function planKeystrokes(text: string): KeystrokePlan {
  const keystrokes: Keystroke[] = []
  const unsupported: string[] = []
  const seenUnsupported = new Set<string>()

  // CR, CRLF and LF all mean one Enter.
  const normalised = text.replace(/\r\n?/g, "\n")

  for (const char of normalised) {
    const stroke = FIXED_KEYS[char] ?? CHAR_KEYS.get(char)
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
