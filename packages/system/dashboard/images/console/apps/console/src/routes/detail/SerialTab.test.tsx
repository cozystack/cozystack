import { describe, it, expect, vi, afterEach, beforeEach } from "vitest"
import { screen, waitFor, cleanup } from "@testing-library/react"
import { K8sClient } from "@cozystack/k8s-client"
import { renderWithK8sProvider } from "../../test-utils/render.tsx"
import { SerialTab } from "./SerialTab.tsx"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"

const { terminals } = vi.hoisted(() => {
  const terminals: Array<Record<string, unknown>> = []
  return { terminals }
})

// xterm draws through canvas APIs jsdom does not implement; the tab's own
// wiring is what these tests are about.
vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    written: string[] = []
    onDataHandler: ((data: string) => void) | null = null
    constructor(options: unknown) {
      terminals.push({ options, instance: this })
    }
    loadAddon() {}
    open() {}
    focus() {}
    dispose() {}
    selection = ""
    keyHandler: ((event: KeyboardEvent) => boolean) | null = null
    hasSelection() {
      return this.selection !== ""
    }
    getSelection() {
      return this.selection
    }
    attachCustomKeyEventHandler(handler: (event: KeyboardEvent) => boolean) {
      this.keyHandler = handler
    }
    write(text: string) {
      this.written.push(text)
    }
    onData(handler: (data: string) => void) {
      this.onDataHandler = handler
      return { dispose: () => {} }
    }
  },
}))

vi.mock("@xterm/addon-fit", () => ({
  FitAddon: class {
    fit() {}
  },
}))

vi.mock("@xterm/xterm/css/xterm.css", () => ({}))

// jsdom ships no ResizeObserver; every browser the console targets has one.
class FakeResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

const sockets: FakeWebSocket[] = []

class FakeWebSocket {
  url: string
  protocols?: string | string[]
  binaryType = ""
  sent: Uint8Array[] = []
  closed = false
  onopen: (() => void) | null = null
  onclose: ((e: { code: number; reason: string }) => void) | null = null
  onerror: (() => void) | null = null
  onmessage: ((e: { data: unknown }) => void) | null = null

  constructor(url: string, protocols?: string | string[]) {
    this.url = url
    this.protocols = protocols
    sockets.push(this)
  }

  send(data: Uint8Array) {
    this.sent.push(data)
  }

  close() {
    this.closed = true
  }
}

function makeAd(kind: string): ApplicationDefinition {
  return {
    apiVersion: "cozystack.io/v1alpha1",
    kind: "ApplicationDefinition",
    metadata: { name: "virtual-machine" },
    spec: {
      application: { kind, plural: "vminstances", singular: "vm-instance", openAPISchema: "{}" },
    },
  }
}

const instance: ApplicationInstance = {
  apiVersion: "apps.cozystack.io/v1alpha1",
  kind: "VMInstance",
  metadata: { name: "demo-vm", namespace: "tenant-root" },
}

function makeClient(printableStatus = "Running") {
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
        status: { printableStatus },
      },
    ],
  })
  vi.spyOn(client, "watch").mockReturnValue(() => {})
  return client
}

beforeEach(() => {
  vi.stubGlobal("ResizeObserver", FakeResizeObserver)
})

afterEach(() => {
  cleanup()
  sockets.length = 0
  terminals.length = 0
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("SerialTab gating", () => {
  it("offers nothing for a non-VM application and never queries", () => {
    const client = makeClient()
    renderWithK8sProvider(<SerialTab ad={makeAd("Postgres")} instance={instance} />, { client })

    expect(screen.getByText(/only available for VMInstance/i)).toBeInTheDocument()
    expect(client.list).not.toHaveBeenCalled()
    expect(sockets).toHaveLength(0)
  })

  it("opens no console while the VM is stopped", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket)
    renderWithK8sProvider(<SerialTab ad={makeAd("VMInstance")} instance={instance} />, {
      client: makeClient("Stopped"),
    })

    await waitFor(() =>
      expect(screen.getByText(/virtual machine is not running/i)).toBeInTheDocument(),
    )
    expect(sockets).toHaveLength(0)
  })
})

describe("SerialTab console stream", () => {
  it("connects to the VMI console subresource for the resolved VM name", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket)
    renderWithK8sProvider(<SerialTab ad={makeAd("VMInstance")} instance={instance} />, {
      client: makeClient(),
    })

    await waitFor(() => expect(sockets).toHaveLength(1))
    expect(sockets[0].url).toBe(
      "ws://localhost:3000/k8s/apis/subresources.kubevirt.io/v1/namespaces/tenant-root/virtualmachineinstances/vm-instance-demo-vm/console",
    )
    expect(sockets[0].protocols).toEqual(["plain.kubevirt.io"])
  })

  it("writes what the guest sends into the terminal", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket)
    renderWithK8sProvider(<SerialTab ad={makeAd("VMInstance")} instance={instance} />, {
      client: makeClient(),
    })

    await waitFor(() => expect(sockets).toHaveLength(1))
    sockets[0].onopen?.()
    sockets[0].onmessage?.({ data: new TextEncoder().encode("login: ").buffer })

    const terminal = terminals[0].instance as { written: string[] }
    await waitFor(() => expect(terminal.written).toEqual(["login: "]))
  })

  it("sends what the user types as binary", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket)
    renderWithK8sProvider(<SerialTab ad={makeAd("VMInstance")} instance={instance} />, {
      client: makeClient(),
    })

    await waitFor(() => expect(sockets).toHaveLength(1))
    sockets[0].onopen?.()
    const terminal = terminals[0].instance as { onDataHandler: ((d: string) => void) | null }
    terminal.onDataHandler?.("ls\n")

    expect(sockets[0].sent).toHaveLength(1)
    expect(new TextDecoder().decode(sockets[0].sent[0])).toBe("ls\n")
  })

  it("shows the connection state once the socket opens", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket)
    renderWithK8sProvider(<SerialTab ad={makeAd("VMInstance")} instance={instance} />, {
      client: makeClient(),
    })

    await waitFor(() => expect(sockets).toHaveLength(1))
    expect(screen.getByText(/connecting/i)).toBeInTheDocument()

    sockets[0].onopen?.()

    expect(await screen.findByText(/^Connected$/)).toBeInTheDocument()
  })

  it("surfaces an unclean close instead of looking idle", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket)
    renderWithK8sProvider(<SerialTab ad={makeAd("VMInstance")} instance={instance} />, {
      client: makeClient(),
    })

    await waitFor(() => expect(sockets).toHaveLength(1))
    sockets[0].onopen?.()
    sockets[0].onclose?.({ code: 1006, reason: "" })

    expect(await screen.findByText(/connection closed \(1006\)/i)).toBeInTheDocument()
  })

  it("copies the selection on the terminal's own chord, not on plain Ctrl+C", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } })
    vi.stubGlobal("WebSocket", FakeWebSocket)
    renderWithK8sProvider(<SerialTab ad={makeAd("VMInstance")} instance={instance} />, {
      client: makeClient(),
    })

    await waitFor(() => expect(terminals).toHaveLength(1))
    const terminal = terminals[0].instance as {
      selection: string
      keyHandler: ((event: KeyboardEvent) => boolean) | null
    }
    terminal.selection = "copied from the guest"

    const chord = (init: KeyboardEventInit) =>
      terminal.keyHandler?.(new KeyboardEvent("keydown", { code: "KeyC", ...init }))

    // Plain Ctrl+C is the interrupt and must reach the guest untouched.
    expect(chord({ ctrlKey: true })).toBe(true)
    expect(writeText).not.toHaveBeenCalled()

    expect(chord({ ctrlKey: true, shiftKey: true })).toBe(false)
    expect(writeText).toHaveBeenCalledWith("copied from the guest")
  })

  it("leaves Ctrl+V to the terminal on a Mac, where the browser pastes on Cmd+V", async () => {
    vi.spyOn(navigator, "platform", "get").mockReturnValue("MacIntel")
    vi.stubGlobal("WebSocket", FakeWebSocket)
    renderWithK8sProvider(<SerialTab ad={makeAd("VMInstance")} instance={instance} />, {
      client: makeClient(),
    })

    await waitFor(() => expect(terminals).toHaveLength(1))
    const terminal = terminals[0].instance as {
      keyHandler: ((event: KeyboardEvent) => boolean) | null
    }

    expect(
      terminal.keyHandler?.(new KeyboardEvent("keydown", { code: "KeyV", ctrlKey: true })),
    ).toBe(true)
  })

  it("hands Ctrl+V back to the browser instead of letting xterm send SYN", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket)
    renderWithK8sProvider(<SerialTab ad={makeAd("VMInstance")} instance={instance} />, {
      client: makeClient(),
    })

    await waitFor(() => expect(terminals).toHaveLength(1))
    const terminal = terminals[0].instance as {
      keyHandler: ((event: KeyboardEvent) => boolean) | null
    }

    // xterm turns Ctrl with a letter into a control byte and cancels the
    // keydown, which kills the browser's own paste. Returning false keeps
    // xterm out of it.
    const handled = terminal.keyHandler?.(
      new KeyboardEvent("keydown", { code: "KeyV", ctrlKey: true }),
    )

    expect(handled).toBe(false)
  })

  it("leaves other Ctrl chords to the terminal", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket)
    renderWithK8sProvider(<SerialTab ad={makeAd("VMInstance")} instance={instance} />, {
      client: makeClient(),
    })

    await waitFor(() => expect(terminals).toHaveLength(1))
    const terminal = terminals[0].instance as {
      keyHandler: ((event: KeyboardEvent) => boolean) | null
    }

    // Ctrl+D, Ctrl+Z and friends are the terminal's business.
    expect(
      terminal.keyHandler?.(new KeyboardEvent("keydown", { code: "KeyD", ctrlKey: true })),
    ).toBe(true)
  })

  it("leaves the copy chord to the guest when nothing is selected", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } })
    vi.stubGlobal("WebSocket", FakeWebSocket)
    renderWithK8sProvider(<SerialTab ad={makeAd("VMInstance")} instance={instance} />, {
      client: makeClient(),
    })

    await waitFor(() => expect(terminals).toHaveLength(1))
    const terminal = terminals[0].instance as {
      keyHandler: ((event: KeyboardEvent) => boolean) | null
    }

    const handled = terminal.keyHandler?.(
      new KeyboardEvent("keydown", { code: "KeyC", metaKey: true }),
    )

    expect(handled).toBe(true)
    expect(writeText).not.toHaveBeenCalled()
  })

  it("closes the socket when the tab goes away", async () => {
    vi.stubGlobal("WebSocket", FakeWebSocket)
    const { unmount } = renderWithK8sProvider(
      <SerialTab ad={makeAd("VMInstance")} instance={instance} />,
      { client: makeClient() },
    )

    await waitFor(() => expect(sockets).toHaveLength(1))
    unmount()

    expect(sockets[0].closed).toBe(true)
  })
})
