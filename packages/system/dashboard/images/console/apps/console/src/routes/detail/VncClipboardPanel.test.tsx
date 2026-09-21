import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { VncClipboardPanel } from "./VncClipboardPanel.tsx"
import type { KeySender } from "../../lib/vnc-typing.ts"

function recorder(): { sender: KeySender; keys: string[] } {
  const keys: string[] = []
  return {
    keys,
    sender: {
      sendKey: (_keysym, code, down) => {
        if (down) keys.push(code)
      },
    },
  }
}

function renderPanel(props: Partial<Parameters<typeof VncClipboardPanel>[0]> = {}) {
  const fallback = recorder()
  return render(
    <VncClipboardPanel
      sender={fallback.sender}
      guestClipboard={null}
      onClose={() => {}}
      keyDelayMs={0}
      {...props}
    />,
  )
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe("VncClipboardPanel sending", () => {
  it("types the text into the session on send", async () => {
    const user = userEvent.setup()
    const { sender, keys } = recorder()
    renderPanel({ sender })

    await user.type(screen.getByLabelText(/text to type/i), "ab")
    await user.click(screen.getByRole("button", { name: /send to vm/i }))

    await waitFor(() => expect(keys).toEqual(["KeyA", "KeyB"]))
  })

  it("refuses to send while the text is empty", () => {
    renderPanel()

    expect(screen.getByRole("button", { name: /send to vm/i })).toBeDisabled()
  })

  it("refuses to send while the session is down", async () => {
    const user = userEvent.setup()
    renderPanel({ sender: null })

    await user.type(screen.getByLabelText(/text to type/i), "ab")

    expect(screen.getByRole("button", { name: /send to vm/i })).toBeDisabled()
  })

  it("reports how much landed once the text is typed", async () => {
    const user = userEvent.setup()
    renderPanel()

    await user.type(screen.getByLabelText(/text to type/i), "abc")
    await user.click(screen.getByRole("button", { name: /send to vm/i }))

    await waitFor(() => expect(screen.getByText(/typed 3 characters/i)).toBeInTheDocument())
  })

  it("hands focus back to the console after typing", async () => {
    const user = userEvent.setup()
    const onTypingFinished = vi.fn()
    renderPanel({ onTypingFinished })

    await user.type(screen.getByLabelText(/text to type/i), "a")
    await user.click(screen.getByRole("button", { name: /send to vm/i }))

    await waitFor(() => expect(onTypingFinished).toHaveBeenCalled())
  })
})

describe("VncClipboardPanel layout awareness", () => {
  it("warns about characters the selected layout cannot type", async () => {
    const user = userEvent.setup()
    renderPanel()

    await user.selectOptions(screen.getByLabelText(/guest keyboard layout/i), "ru")
    await user.type(screen.getByLabelText(/text to type/i), "kubectl")

    expect(await screen.findByText(/cannot be typed on the russian layout/i)).toBeInTheDocument()
  })

  it("types Cyrillic on the Russian layout without complaint", async () => {
    const user = userEvent.setup()
    const { sender, keys } = recorder()
    renderPanel({ sender })

    await user.selectOptions(screen.getByLabelText(/guest keyboard layout/i), "ru")
    await user.type(screen.getByLabelText(/text to type/i), "фы")
    await user.click(screen.getByRole("button", { name: /send to vm/i }))

    await waitFor(() => expect(keys).toEqual(["KeyA", "KeyS"]))
    expect(screen.queryByText(/cannot be typed/i)).not.toBeInTheDocument()
  })
})

describe("VncClipboardPanel browser clipboard", () => {
  it("fills the box from the browser clipboard", async () => {
    const user = userEvent.setup()
    const readText = vi.fn().mockResolvedValue("from-browser")
    vi.stubGlobal("navigator", { ...navigator, clipboard: { readText } })
    renderPanel()

    await user.click(screen.getByRole("button", { name: /read clipboard/i }))

    await waitFor(() =>
      expect(screen.getByLabelText(/text to type/i)).toHaveValue("from-browser"),
    )
  })

  it("explains it when the browser denies clipboard access", async () => {
    const user = userEvent.setup()
    const readText = vi.fn().mockRejectedValue(new Error("Denied"))
    vi.stubGlobal("navigator", { ...navigator, clipboard: { readText } })
    renderPanel()

    await user.click(screen.getByRole("button", { name: /read clipboard/i }))

    expect(await screen.findByText(/could not read the browser clipboard/i)).toBeInTheDocument()
  })
})

describe("VncClipboardPanel guest-to-browser direction", () => {
  it("says why nothing arrives from the VM when the session pushed nothing", () => {
    renderPanel({ guestClipboard: null })

    expect(screen.getByText(/nothing received from the vm/i)).toBeInTheDocument()
  })

  it("shows what the VM pushed and copies it to the browser clipboard", async () => {
    const user = userEvent.setup()
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } })
    renderPanel({ guestClipboard: "from-guest" })

    expect(screen.getByDisplayValue("from-guest")).toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: /copy to browser/i }))

    await waitFor(() => expect(writeText).toHaveBeenCalledWith("from-guest"))
  })
})
