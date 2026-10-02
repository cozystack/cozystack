import { describe, it, expect, vi, afterEach } from "vitest"
import { screen, waitFor, cleanup, act, fireEvent } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { K8sClient } from "@cozystack/k8s-client"
import { renderWithK8sProvider } from "../../test-utils/render.tsx"
import { VncTab } from "./VncTab.tsx"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"

const { FakeRFB } = vi.hoisted(() => {
  const MODIFIERS: Record<string, string> = {
    ControlLeft: "Ctrl",
    ControlRight: "Ctrl",
    AltLeft: "Alt",
    AltRight: "AltGr",
    MetaLeft: "Meta",
    MetaRight: "Meta",
    ShiftLeft: "Shift",
    ShiftRight: "Shift",
  }

  // Latin-1 keysyms are the code point; anything past it is offset into the
  // Unicode plane. Function keysyms (0xff00..0xffff) are neither — Enter is
  // 0xff0d, and treating it as an offset yields a negative code point, which
  // throws where an assertion should have been.
  const FUNCTION_KEYSYMS: Record<number, string> = { 0xff0d: "⏎", 0xff09: "⇥" }

  function charFor(keysym: number): string {
    if (keysym in FUNCTION_KEYSYMS) return FUNCTION_KEYSYMS[keysym]
    if (keysym >= 0x01000000) return String.fromCodePoint(keysym - 0x01000000)
    if (keysym <= 0xff) return String.fromCodePoint(keysym)
    return `<keysym 0x${keysym.toString(16)}>`
  }

  /**
   * Stands in for noVNC, and models the one thing a list of sendKey calls
   * cannot: the guest's keyboard state. A character typed while Ctrl is down
   * does not reach the guest as that character — it reaches it as a chord,
   * which is how a paste can run a command nobody pasted.
   */
  class FakeRFB extends EventTarget {
    static instances: FakeRFB[] = []
    sendKey = vi.fn((keysym: number, code: string, down: boolean) => {
      if (this.armed) {
        if (this.armed.afterEvents <= 0) {
          const { code: armedCode } = this.armed
          this.armed = null
          this.sendKey(armedCode.startsWith("Control") ? 0xffe3 : 0xffe9, armedCode, true)
        } else {
          this.armed.afterEvents--
        }
      }
      const modifier = MODIFIERS[code]
      if (modifier) {
        if (down) this.held.add(modifier)
        else this.held.delete(modifier)
        return
      }
      if (!down) return
      const char = charFor(keysym)
      const chord = [...this.held].filter((m) => m !== "Shift")
      this.guestSaw += chord.length > 0 ? `<${chord.join("+")}+${char}>` : char
    })
    disconnect = vi.fn()
    sendCtrlAltDel = vi.fn()
    focus = vi.fn()
    scaleViewport = false
    resizeSession = false
    canvas: HTMLCanvasElement
    /** Modifiers the guest currently believes are down. */
    held = new Set<string>()
    /** A modifier noVNC is sitting on, and how many events until it lands. */
    armed: { code: string; afterEvents: number } | null = null
    /** What the guest ended up receiving, chords marked. */
    guestSaw = ""

    // The real RFB builds a focusable canvas inside the target element and
    // takes the keyboard from it; the paste shortcut keys off exactly that.
    constructor(target: HTMLElement) {
      super()
      this.canvas = document.createElement("canvas")
      this.canvas.tabIndex = -1
      target.appendChild(this.canvas)
      FakeRFB.instances.push(this)
    }

    /** noVNC sends a modifier down the moment it is pressed on the canvas. */
    pressModifier(code: string) {
      this.sendKey(code.startsWith("Control") ? 0xffe3 : 0xffe9, code, true)
    }

    /**
     * The Windows path. The keydown tells the guest nothing and arms AltGr
     * detection; the modifier is delivered once, by noVNC's own 100 ms timer
     * or by the next keyup, whichever comes first. Both land within that
     * window of the keydown — which is to say, while the paste is still in
     * its opening releases, not after a character has been typed. The fake
     * delivers partway through the opening releases, where noVNC's own timer
     * lands at the default pace, so the test cannot choose the moment.
     */
    armHeldModifier(code: string, afterEvents = 4) {
      this.armed = { code, afterEvents }
    }
  }
  return { FakeRFB }
})

vi.mock("@novnc/novnc/lib/rfb", () => ({ default: FakeRFB }))

const ad: ApplicationDefinition = {
  apiVersion: "cozystack.io/v1alpha1",
  kind: "ApplicationDefinition",
  metadata: { name: "virtual-machine" },
  spec: {
    application: {
      kind: "VMInstance",
      plural: "vminstances",
      singular: "vm-instance",
      openAPISchema: "{}",
    },
  },
}

const instance: ApplicationInstance = {
  apiVersion: "apps.cozystack.io/v1alpha1",
  kind: "VMInstance",
  metadata: { name: "demo-vm", namespace: "tenant-root" },
}

function runningClient() {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockResolvedValue({
    apiVersion: "kubevirt.io/v1",
    kind: "VirtualMachineList",
    metadata: {},
    items: [
      {
        apiVersion: "kubevirt.io/v1",
        kind: "VirtualMachine",
        metadata: { name: "vm-instance-demo-vm" },
        status: { printableStatus: "Running" },
      },
    ],
  })
  vi.spyOn(client, "watch").mockReturnValue(() => {})
  return client
}

interface Session {
  rfb: InstanceType<typeof FakeRFB>
  sink: HTMLTextAreaElement
  unmount: () => void
}

async function connectedSession(keyDelayMs: number | undefined = 0): Promise<Session> {
  const { container, unmount } = renderWithK8sProvider(
    <VncTab ad={ad} instance={instance} keyDelayMs={keyDelayMs} />,
    { client: runningClient() },
  )

  await waitFor(() => expect(FakeRFB.instances).toHaveLength(1))
  const rfb = FakeRFB.instances[0]
  act(() => {
    rfb.dispatchEvent(new CustomEvent("connect"))
  })
  rfb.canvas.focus()

  const sink = container.querySelector("textarea")
  if (!sink) throw new Error("paste sink missing")
  return { rfb, sink, unmount }
}

function pressPaste(init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", {
    key: "v",
    code: "KeyV",
    ctrlKey: true,
    bubbles: true,
    cancelable: true,
    ...init,
  })
  act(() => {
    ;(document.activeElement ?? document).dispatchEvent(event)
  })
  return event
}

function paste(sink: HTMLTextAreaElement, text: string) {
  fireEvent.paste(sink, { clipboardData: { getData: () => text } })
}

function pressedKeys(rfb: InstanceType<typeof FakeRFB>): string[] {
  return rfb.sendKey.mock.calls.filter(([, , down]) => down).map(([, code]) => code)
}

afterEach(() => {
  cleanup()
  FakeRFB.instances.length = 0
  vi.restoreAllMocks()
})

describe("VncTab paste shortcut", () => {
  it("moves the focus to the paste sink on Ctrl+V", async () => {
    const { sink } = await connectedSession()

    pressPaste()

    expect(document.activeElement).toBe(sink)
  })

  it("ignores Cmd+V where the platform pastes with Ctrl", async () => {
    const { rfb, sink } = await connectedSession()

    // Taking both chords costs the guest one it has its own use for.
    pressPaste({ ctrlKey: false, metaKey: true })

    expect(document.activeElement).toBe(rfb.canvas)
    expect(document.activeElement).not.toBe(sink)
  })

  it("takes Cmd+V, and leaves Ctrl+V to the guest, on a Mac", async () => {
    vi.spyOn(navigator, "platform", "get").mockReturnValue("MacIntel")
    const { rfb, sink } = await connectedSession()

    pressPaste()
    expect(document.activeElement).toBe(rfb.canvas)

    pressPaste({ ctrlKey: false, metaKey: true })
    expect(document.activeElement).toBe(sink)
  })

  it("hides the shortcut from noVNC, which would cancel the paste", async () => {
    const { rfb } = await connectedSession()
    const seenByNoVnc = vi.fn()
    rfb.canvas.addEventListener("keydown", seenByNoVnc)

    const event = pressPaste()

    expect(seenByNoVnc).not.toHaveBeenCalled()
    // Not prevented: cancelling the keydown would cancel the browser's paste.
    expect(event.defaultPrevented).toBe(false)
  })

  it("leaves a bare V to the guest", async () => {
    const { sink, rfb } = await connectedSession()

    pressPaste({ ctrlKey: false })

    expect(document.activeElement).toBe(rfb.canvas)
    expect(document.activeElement).not.toBe(sink)
  })

  it("ignores the shortcut while the focus sits outside the console", async () => {
    const { sink } = await connectedSession()
    const outside = document.createElement("input")
    document.body.appendChild(outside)
    outside.focus()

    pressPaste()

    expect(document.activeElement).toBe(outside)
    expect(document.activeElement).not.toBe(sink)
    outside.remove()
  })
})

describe("VncTab pasted text", () => {
  it("types what the browser pasted into the session", async () => {
    const { rfb, sink } = await connectedSession()

    pressPaste()
    paste(sink, "hi")

    await waitFor(() => expect(pressedKeys(rfb)).toEqual(["KeyH", "KeyI"]))
  })

  it("does not type the paste as chords while the modifier is still held", async () => {
    const { rfb, sink } = await connectedSession()

    // The user is still holding Ctrl: noVNC told the guest so the moment the
    // key went down, and the release only comes on keyup.
    rfb.pressModifier("ControlLeft")

    pressPaste()
    paste(sink, "cm")

    await waitFor(() => expect(rfb.guestSaw.length).toBeGreaterThanOrEqual(2))
    // Typed under Ctrl these are not characters at all: Ctrl+C interrupts and
    // Ctrl+M is Return, so the guest runs whatever was on the line.
    expect(rfb.guestSaw).toBe("cm")
  })

  it("does not type the paste as chords while Cmd is still held", async () => {
    vi.spyOn(navigator, "platform", "get").mockReturnValue("MacIntel")
    const { rfb, sink } = await connectedSession()

    // noVNC maps Cmd to Alt_L, so a Cmd+V paste arrives as Alt chords.
    rfb.pressModifier("MetaLeft")

    pressPaste({ ctrlKey: false, metaKey: true })
    paste(sink, "ab")

    await waitFor(() => expect(rfb.guestSaw.length).toBeGreaterThanOrEqual(2))
    expect(rfb.guestSaw).toBe("ab")
  })

  it("renders a newline the guest received instead of throwing on its keysym", async () => {
    const { rfb, sink } = await connectedSession()

    pressPaste()
    paste(sink, "hi\n")

    // Enter is keysym 0xff0d, which is neither Latin-1 nor a Unicode offset.
    await waitFor(() => expect(rfb.guestSaw).toBe("hi⏎"))
  })

  it("types plain characters when the host was sitting on the modifier", async () => {
    const { rfb, sink } = await connectedSession()

    // noVNC has not told the guest about Ctrl yet; it will, on its own, while
    // the paste is still opening.
    rfb.armHeldModifier("ControlLeft")

    pressPaste()
    paste(sink, "mkdir")

    // Under a held Ctrl these are chords, and Ctrl+M is Return, so the guest
    // would submit whatever the line already held.
    await waitFor(() => expect(rfb.guestSaw).toBe("mkdir"))
  })

  it("hands the keyboard back to the console", async () => {
    const { rfb, sink } = await connectedSession()

    pressPaste()
    paste(sink, "x")

    await waitFor(() => expect(rfb.focus).toHaveBeenCalled())
  })

  it("stops typing when the tab is unmounted mid-paste", async () => {
    const { rfb, sink, unmount } = await connectedSession()

    pressPaste()
    paste(sink, "abcdefghijklmnopqrstuvwxyz")
    await waitFor(() => expect(rfb.guestSaw.length).toBeGreaterThan(0))
    unmount()

    const atUnmount = rfb.guestSaw.length
    await new Promise((resolve) => setTimeout(resolve, 200))

    // A character may already be in flight; the loop must not run on.
    expect(rfb.guestSaw.length - atUnmount).toBeLessThanOrEqual(1)
    expect(rfb.guestSaw.length).toBeLessThan(26)
  })

  it("types nothing once the session has dropped", async () => {
    const { rfb, sink } = await connectedSession()
    act(() => {
      rfb.dispatchEvent(new CustomEvent("disconnect", { detail: { clean: true } }))
    })

    paste(sink, "gone")

    await waitFor(() => expect(rfb.sendKey).not.toHaveBeenCalled())
  })

  it("refuses a second paste while the first is still typing", async () => {
    const { rfb, sink } = await connectedSession()

    pressPaste()
    paste(sink, "aaaa")
    paste(sink, "zzzz")

    // Real timers: the run is a prelude, four characters and one repeat, so
    // give it room rather than racing the default timeout under load.
    await waitFor(() => expect(screen.getByText(/^Connected$/)).toBeInTheDocument(), {
      timeout: 5000,
    })
    expect(rfb.guestSaw).toBe("aaaa")
  })

  it("says a paste was cut short rather than reporting it as finished", async () => {
    const { rfb, sink } = await connectedSession()

    pressPaste()
    paste(sink, "abcdefghij")
    await waitFor(() => expect(rfb.sendKey).toHaveBeenCalled())
    act(() => {
      rfb.dispatchEvent(new CustomEvent("disconnect", { detail: { clean: false, reason: "gone" } }))
    })

    expect(await screen.findByText(/paste stopped after \d+/i)).toBeInTheDocument()
  })

  it("abandons the paste when the session is torn down and rebuilt", async () => {
    const { rfb, sink } = await connectedSession()

    pressPaste()
    paste(sink, "abcdefghijklmnop")
    await waitFor(() => expect(rfb.sendKey).toHaveBeenCalled())

    // Reconnect tears the session down and builds a new one. The paste
    // belongs to the old session and must not continue into either.
    await userEvent.click(screen.getByTitle("Reconnect"))
    await waitFor(() => expect(FakeRFB.instances).toHaveLength(2))
    const replacement = FakeRFB.instances[1]
    act(() => {
      replacement.dispatchEvent(new CustomEvent("connect"))
    })

    const beforeWait = rfb.guestSaw.length
    await new Promise((resolve) => setTimeout(resolve, 250))

    expect(replacement.guestSaw).toBe("")
    expect(rfb.guestSaw.length - beforeWait).toBeLessThanOrEqual(1)
  })

  it("keeps nothing of the pasted text in the sink", async () => {
    const { sink } = await connectedSession()

    pressPaste()
    paste(sink, "secret")

    expect(sink.value).toBe("")
  })
})

describe("VncTab paste shortcut on other keyboard layouts", () => {
  it("matches the chord by the character, not only by the physical key", async () => {
    const { sink } = await connectedSession()

    // On Dvorak the V character does not sit on the KeyV position.
    pressPaste({ code: "Period", key: "v" })

    expect(document.activeElement).toBe(sink)
  })
})

describe("VncTab paste when the clipboard gives nothing", () => {
  it("says the clipboard was blocked and takes the keyboard back", async () => {
    // The session has to come up on real timers: waitFor never advances under
    // fake ones, and a hung await here leaks them into the next test.
    const { rfb, sink } = await connectedSession()

    // An empty or refused clipboard raises no paste event at all.
    pressPaste()
    expect(document.activeElement).toBe(sink)

    expect(await screen.findByText(/clipboard blocked/i)).toBeInTheDocument()
    expect(rfb.focus).toHaveBeenCalled()
  })
})

describe("VncTab paste feedback in the toolbar", () => {
  it("paces itself at the measured default when the caller gives no delay", async () => {
    // Rendered without the prop, because nothing else reads the component's
    // own default: setting it to zero would otherwise leave the suite green
    // while the guest drops characters.
    const { container } = renderWithK8sProvider(<VncTab ad={ad} instance={instance} />, {
      client: runningClient(),
    })
    await waitFor(() => expect(FakeRFB.instances).toHaveLength(1))
    const rfb = FakeRFB.instances[0]
    act(() => {
      rfb.dispatchEvent(new CustomEvent("connect"))
    })
    rfb.canvas.focus()
    const sink = container.querySelector("textarea")
    if (!sink) throw new Error("paste sink missing")

    const started = Date.now()
    pressPaste()
    paste(sink, "ab")
    await waitFor(() => expect(rfb.guestSaw).toBe("ab"), { timeout: 5000 })

    // Fourteen opening releases and two characters at 25 ms a piece.
    expect(Date.now() - started).toBeGreaterThan(200)
  })

  it("counts the characters while they are typed", async () => {
    // The one test that needs real pacing: with no delay the run finishes
    // before a count can be observed.
    const { sink } = await connectedSession(5)

    pressPaste()
    paste(sink, "abcdef")

    // The count starts after the prelude, so match any position rather than
    // racing the first one.
    expect(await screen.findByText(/pasting \d+\/6/i, undefined, { timeout: 5000 })).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText(/^Connected$/)).toBeInTheDocument(), {
      timeout: 5000,
    })
  })

  it("sends nothing at all when a character cannot be typed", async () => {
    const { rfb, sink } = await connectedSession()

    // Dropping the Cyrillic and typing the rest would run "rm -rf /srv",
    // newline included, before any notice reached the user.
    pressPaste()
    paste(sink, "rm -rf /srv/данные\n")

    expect(await screen.findByText(/nothing sent/i)).toBeInTheDocument()
    expect(rfb.guestSaw).toBe("")
  })

  it("counts the distinct characters it could not type", async () => {
    const { sink } = await connectedSession()

    pressPaste()
    paste(sink, "привет")

    expect(await screen.findByText(/6 chars not on layout/i)).toBeInTheDocument()
  })
})
