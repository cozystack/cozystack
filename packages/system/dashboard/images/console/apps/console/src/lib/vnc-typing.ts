import { XK_ISO_LEVEL3_SHIFT, XK_SHIFT_L, type Keystroke } from "./vnc-keymap.ts"

/**
 * Feed a planned key sequence to an RFB session.
 *
 * Every event is spaced out: qemu drains the emulated keyboard controller at
 * its own pace and a burst loses characters silently, which is why the
 * upstream KubeVirt console paces its paste the same way.
 */

export interface KeySender {
  sendKey(keysym: number, code: string, down: boolean): void
}

export interface TypeKeystrokesOptions {
  delayMs?: number
  signal?: AbortSignal
  isConnected?: () => boolean
  onProgress?: (typed: number, total: number) => void
  /** Injectable for tests; production uses a real timer. */
  sleep?: (ms: number) => Promise<void>
}

export interface TypeKeystrokesResult {
  typed: number
  stopped: "done" | "aborted" | "disconnected"
}

export const DEFAULT_KEY_DELAY_MS = 25

const SHIFT_KEY = { keysym: XK_SHIFT_L, code: "ShiftLeft" }
const ALT_GR_KEY = { keysym: XK_ISO_LEVEL3_SHIFT, code: "AltRight" }

function timerSleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

export async function typeKeystrokes(
  sender: KeySender,
  keystrokes: readonly Keystroke[],
  options: TypeKeystrokesOptions = {},
): Promise<TypeKeystrokesResult> {
  const {
    delayMs = DEFAULT_KEY_DELAY_MS,
    signal,
    isConnected = () => true,
    onProgress,
    sleep = timerSleep,
  } = options

  let heldShift = false
  let heldAltGr = false
  let typed = 0

  const press = async (keysym: number, code: string, down: boolean) => {
    sender.sendKey(keysym, code, down)
    await sleep(delayMs)
  }

  const releaseModifiers = async () => {
    if (heldAltGr) {
      await press(ALT_GR_KEY.keysym, ALT_GR_KEY.code, false)
      heldAltGr = false
    }
    if (heldShift) {
      await press(SHIFT_KEY.keysym, SHIFT_KEY.code, false)
      heldShift = false
    }
  }

  for (const stroke of keystrokes) {
    // A dropped session takes precedence: releasing a modifier into a socket
    // that is gone is pointless, and the caller needs to hear why we stopped.
    if (!isConnected()) return { typed, stopped: "disconnected" }
    if (signal?.aborted) {
      await releaseModifiers()
      return { typed, stopped: "aborted" }
    }

    if (heldShift !== stroke.shift) {
      await press(SHIFT_KEY.keysym, SHIFT_KEY.code, stroke.shift)
      heldShift = stroke.shift
    }
    if (heldAltGr !== stroke.altGr) {
      await press(ALT_GR_KEY.keysym, ALT_GR_KEY.code, stroke.altGr)
      heldAltGr = stroke.altGr
    }

    await press(stroke.keysym, stroke.code, true)
    await press(stroke.keysym, stroke.code, false)

    typed++
    onProgress?.(typed, keystrokes.length)
  }

  if (!isConnected()) return { typed, stopped: "disconnected" }
  await releaseModifiers()
  return { typed, stopped: "done" }
}
