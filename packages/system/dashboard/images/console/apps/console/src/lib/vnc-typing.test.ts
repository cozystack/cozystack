import { describe, it, expect, vi } from "vitest"
import { planKeystrokes } from "./vnc-keymap.ts"
import { typeKeystrokes, DEFAULT_KEY_DELAY_MS, type KeySender } from "./vnc-typing.ts"

interface SentKey {
  keysym: number
  code: string
  down: boolean
}

function recorder(): { sender: KeySender; sent: SentKey[] } {
  const sent: SentKey[] = []
  return {
    sent,
    sender: {
      sendKey: (keysym, code, down) => {
        sent.push({ keysym, code, down })
      },
    },
  }
}

const noSleep = () => Promise.resolve()

function trace(sent: SentKey[]): string[] {
  return sent.map((k) => `${k.code}${k.down ? "↓" : "↑"}`)
}

// Every run opens by releasing the modifiers the host may still be holding,
// waiting out what is left of noVNC's delivery window, then releasing the
// chord modifiers once more — all before the first character.
const HOST_RELEASES = 8
const CHORD_RELEASES = 6
const PRELUDE = HOST_RELEASES + CHORD_RELEASES
// The wait itself is a sleep with no key event behind it.
const PRELUDE_SLEEPS = PRELUDE + 1

function typedTrace(sent: SentKey[]): string[] {
  return trace(sent.slice(PRELUDE))
}

describe("typeKeystrokes", () => {
  it("presses and releases each key in order", async () => {
    const { sender, sent } = recorder()

    const result = await typeKeystrokes(sender, planKeystrokes("ab").keystrokes, {
      sleep: noSleep,
    })

    expect(typedTrace(sent)).toEqual(["KeyA↓", "KeyA↑", "KeyB↓", "KeyB↑"])
    expect(result).toEqual({ typed: 2, stopped: "done" })
  })

  it("holds Shift across a run of shifted characters and releases it once", async () => {
    const { sender, sent } = recorder()

    await typeKeystrokes(sender, planKeystrokes("ABc").keystrokes, { sleep: noSleep })

    // The repeat for a late-delivered chord modifier lands in the middle and
    // deliberately leaves Shift alone: that one is ours, held for the capitals.
    expect(typedTrace(sent)).toEqual([
      "ShiftLeft↓",
      "KeyA↓",
      "KeyA↑",
      "KeyB↓",
      "KeyB↑",
      "ShiftLeft↑",
      "KeyC↓",
      "KeyC↑",
    ])
  })

  it("releases a held modifier when the text ends", async () => {
    const { sender, sent } = recorder()

    await typeKeystrokes(sender, planKeystrokes("A").keystrokes, { sleep: noSleep })

    expect(trace(sent).at(-1)).toBe("ShiftLeft↑")
  })

  it("waits between key events so the guest keyboard buffer keeps up", async () => {
    const { sender } = recorder()
    const sleep = vi.fn(() => Promise.resolve())

    await typeKeystrokes(sender, planKeystrokes("ab").keystrokes, {
      sleep,
      delayMs: 7,
    })

    expect(sleep).toHaveBeenCalledTimes(PRELUDE_SLEEPS + 4)
    expect(sleep).toHaveBeenCalledWith(7)
  })

  it("paces itself at the measured default when no delay is given", async () => {
    const { sender } = recorder()
    const sleep = vi.fn(() => Promise.resolve())

    await typeKeystrokes(sender, planKeystrokes("a").keystrokes, { sleep })

    // Measured, not chosen: at no pacing a 1000-character paste came back
    // with characters dropped and reordered, while 25 ms round-tripped clean.
    expect(sleep).toHaveBeenCalledWith(DEFAULT_KEY_DELAY_MS)
    expect(DEFAULT_KEY_DELAY_MS).toBe(25)
  })

  it("releases the chord modifiers again once a late delivery could have landed", async () => {
    const { sender, sent } = recorder()

    await typeKeystrokes(sender, planKeystrokes("abcdef").keystrokes, { sleep: noSleep })

    // On a Windows host noVNC sits on the first Ctrl for 100 ms and hands it
    // to the guest on a timer, which fires after the prelude has run. The
    // repeat is what catches that, and it happens once, not per character.
    const releases = trace(sent).filter((k) => k === "ControlLeft↑")
    expect(releases).toHaveLength(2)
  })

  it("reports progress as characters land", async () => {
    const { sender } = recorder()
    const onProgress = vi.fn()

    await typeKeystrokes(sender, planKeystrokes("abc").keystrokes, {
      sleep: noSleep,
      onProgress,
    })

    expect(onProgress.mock.calls).toEqual([
      [1, 3],
      [2, 3],
      [3, 3],
    ])
  })
})

describe("typeKeystrokes and the host's own modifiers", () => {
  it("releases every modifier the host may hold before the first character", async () => {
    const { sender, sent } = recorder()

    await typeKeystrokes(sender, planKeystrokes("a").keystrokes, { sleep: noSleep })

    // The paste begins while the user is still holding the shortcut, and the
    // guest was told that modifier is down. Typing into that state sends
    // chords, not characters: under Ctrl an "m" is Return.
    expect(trace(sent).slice(0, HOST_RELEASES)).toEqual([
      "ControlLeft↑",
      "ControlRight↑",
      "AltLeft↑",
      "AltRight↑",
      "MetaLeft↑",
      "MetaRight↑",
      "ShiftLeft↑",
      "ShiftRight↑",
    ])
  })

  it("does not send the prelude when there is nothing to type", async () => {
    const { sender, sent } = recorder()

    await typeKeystrokes(sender, planKeystrokes("").keystrokes, { sleep: noSleep })

    expect(sent).toEqual([])
  })
})

describe("typeKeystrokes interruption", () => {
  it("stops on abort and releases the modifier it was holding", async () => {
    const { sender, sent } = recorder()
    const controller = new AbortController()

    const result = await typeKeystrokes(sender, planKeystrokes("ABC").keystrokes, {
      sleep: noSleep,
      signal: controller.signal,
      // Cancel once a character has gone in, which is where a user cancelling
      // a paste actually lands.
      onProgress: (typed) => {
        if (typed >= 1) controller.abort()
      },
    })

    expect(result.stopped).toBe("aborted")
    expect(result.typed).toBe(1)
    expect(trace(sent).at(-1)).toBe("ShiftLeft↑")
    expect(typedTrace(sent)[0]).toBe("ShiftLeft↓")
  })

  it("stops typing when the session drops and sends nothing more", async () => {
    const { sender, sent } = recorder()
    let connected = true

    const result = await typeKeystrokes(sender, planKeystrokes("abc").keystrokes, {
      sleep: noSleep,
      isConnected: () => connected,
      // Drop it after a character, where a session actually goes: inside the
      // prelude nothing is pressed yet, so the assertion would hold no matter
      // what the loop did.
      onProgress: (typed) => {
        if (typed >= 1) connected = false
      },
    })

    expect(result).toEqual({ typed: 1, stopped: "disconnected" })
    expect(typedTrace(sent)).toEqual(["KeyA↓", "KeyA↑"])
  })

  it("does not try to release modifiers into a dropped session", async () => {
    const { sender, sent } = recorder()
    let connected = true

    // Shift goes down for the capital, then the session drops: the release
    // must not be sent into a socket that is gone.
    await typeKeystrokes(sender, planKeystrokes("AB").keystrokes, {
      sleep: noSleep,
      isConnected: () => connected,
      onProgress: (typed) => {
        if (typed >= 1) connected = false
      },
    })

    expect(typedTrace(sent)).toEqual(["ShiftLeft↓", "KeyA↓", "KeyA↑"])
  })

  it("leaves the last modifier alone when the session dies on the final character", async () => {
    const { sender, sent } = recorder()
    let connected = true

    // One character, so the drop lands after the loop has finished and the run
    // reaches its closing release — the one path where it is reachable at all.
    await typeKeystrokes(sender, planKeystrokes("A").keystrokes, {
      sleep: noSleep,
      isConnected: () => connected,
      onProgress: (typed) => {
        if (typed >= 1) connected = false
      },
    })

    expect(typedTrace(sent)).toEqual(["ShiftLeft↓", "KeyA↓", "KeyA↑"])
    expect(trace(sent).at(-1)).not.toBe("ShiftLeft↑")
  })

  it("sends nothing at all when the session is already down", async () => {
    const { sender, sent } = recorder()

    const result = await typeKeystrokes(sender, planKeystrokes("a").keystrokes, {
      sleep: noSleep,
      isConnected: () => false,
    })

    expect(sent).toEqual([])
    expect(result).toEqual({ typed: 0, stopped: "disconnected" })
  })
})
