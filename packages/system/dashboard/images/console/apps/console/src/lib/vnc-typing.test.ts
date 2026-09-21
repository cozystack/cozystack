import { describe, it, expect, vi } from "vitest"
import { planKeystrokes } from "./vnc-keymap.ts"
import { typeKeystrokes, type KeySender } from "./vnc-typing.ts"

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

describe("typeKeystrokes", () => {
  it("presses and releases each key in order", async () => {
    const { sender, sent } = recorder()

    const result = await typeKeystrokes(sender, planKeystrokes("ab", "en-us").keystrokes, {
      sleep: noSleep,
    })

    expect(trace(sent)).toEqual(["KeyA↓", "KeyA↑", "KeyB↓", "KeyB↑"])
    expect(result).toEqual({ typed: 2, stopped: "done" })
  })

  it("holds Shift across a run of shifted characters and releases it once", async () => {
    const { sender, sent } = recorder()

    await typeKeystrokes(sender, planKeystrokes("ABc", "en-us").keystrokes, { sleep: noSleep })

    expect(trace(sent)).toEqual([
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

    await typeKeystrokes(sender, planKeystrokes("A", "en-us").keystrokes, { sleep: noSleep })

    expect(trace(sent).at(-1)).toBe("ShiftLeft↑")
  })

  it("drives AltGr through the right-hand Alt key", async () => {
    const { sender, sent } = recorder()

    await typeKeystrokes(sender, planKeystrokes("@", "de").keystrokes, { sleep: noSleep })

    expect(trace(sent)).toEqual(["AltRight↓", "KeyQ↓", "KeyQ↑", "AltRight↑"])
    expect(sent[0].keysym).toBe(0xfe03)
  })

  it("waits between key events so the guest keyboard buffer keeps up", async () => {
    const { sender } = recorder()
    const sleep = vi.fn(() => Promise.resolve())

    await typeKeystrokes(sender, planKeystrokes("ab", "en-us").keystrokes, {
      sleep,
      delayMs: 7,
    })

    expect(sleep).toHaveBeenCalledTimes(4)
    expect(sleep).toHaveBeenCalledWith(7)
  })

  it("reports progress as characters land", async () => {
    const { sender } = recorder()
    const onProgress = vi.fn()

    await typeKeystrokes(sender, planKeystrokes("abc", "en-us").keystrokes, {
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

describe("typeKeystrokes interruption", () => {
  it("stops on abort and releases the modifier it was holding", async () => {
    const { sender, sent } = recorder()
    const controller = new AbortController()
    const sleep = () => {
      controller.abort()
      return Promise.resolve()
    }

    const result = await typeKeystrokes(sender, planKeystrokes("ABC", "en-us").keystrokes, {
      sleep,
      signal: controller.signal,
    })

    expect(result.stopped).toBe("aborted")
    expect(result.typed).toBe(1)
    expect(trace(sent).at(-1)).toBe("ShiftLeft↑")
  })

  it("stops typing when the session drops and sends nothing more", async () => {
    const { sender, sent } = recorder()
    let connected = true

    const result = await typeKeystrokes(sender, planKeystrokes("abc", "en-us").keystrokes, {
      sleep: () => {
        connected = false
        return Promise.resolve()
      },
      isConnected: () => connected,
    })

    expect(result).toEqual({ typed: 1, stopped: "disconnected" })
    expect(trace(sent)).toEqual(["KeyA↓", "KeyA↑"])
  })

  it("does not try to release modifiers into a dropped session", async () => {
    const { sender, sent } = recorder()
    let connected = true

    await typeKeystrokes(sender, planKeystrokes("AB", "en-us").keystrokes, {
      sleep: () => {
        connected = false
        return Promise.resolve()
      },
      isConnected: () => connected,
    })

    expect(trace(sent)).toEqual(["ShiftLeft↓", "KeyA↓", "KeyA↑"])
  })

  it("sends nothing at all when the session is already down", async () => {
    const { sender, sent } = recorder()

    const result = await typeKeystrokes(sender, planKeystrokes("a", "en-us").keystrokes, {
      sleep: noSleep,
      isConnected: () => false,
    })

    expect(sent).toEqual([])
    expect(result).toEqual({ typed: 0, stopped: "disconnected" })
  })
})
