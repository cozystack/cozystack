import { describe, it, expect, vi, afterEach } from "vitest"
import { screen, waitFor, cleanup, act } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { K8sClient } from "@cozystack/k8s-client"
import { renderWithK8sProvider } from "../../test-utils/render.tsx"
import { VncTab } from "./VncTab.tsx"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"

const { FakeRFB } = vi.hoisted(() => {
  class FakeRFB extends EventTarget {
    static instances: FakeRFB[] = []
    sendKey = vi.fn()
    focus = vi.fn()
    disconnect = vi.fn()
    sendCtrlAltDel = vi.fn()
    scaleViewport = false
    resizeSession = false

    constructor() {
      super()
      FakeRFB.instances.push(this)
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

/** Render, wait for the RFB session, and report it connected. */
async function connectedSession() {
  renderWithK8sProvider(<VncTab ad={ad} instance={instance} />, { client: runningClient() })

  await waitFor(() => expect(FakeRFB.instances).toHaveLength(1))
  const rfb = FakeRFB.instances[0]
  act(() => {
    rfb.dispatchEvent(new CustomEvent("connect"))
  })
  return rfb
}

afterEach(() => {
  cleanup()
  FakeRFB.instances.length = 0
  vi.clearAllMocks()
})

describe("VncTab clipboard panel", () => {
  it("stays closed until the toolbar button is pressed", async () => {
    const user = userEvent.setup()
    await connectedSession()

    expect(screen.queryByLabelText(/text to type/i)).not.toBeInTheDocument()

    await user.click(screen.getByTitle("Clipboard"))

    expect(screen.getByLabelText(/text to type/i)).toBeInTheDocument()
  })

  it("types the panel's text into the live session", async () => {
    const user = userEvent.setup()
    const rfb = await connectedSession()

    await user.click(screen.getByTitle("Clipboard"))
    await user.type(screen.getByLabelText(/text to type/i), "hi")
    await user.click(screen.getByRole("button", { name: /send to vm/i }))

    await waitFor(() => expect(screen.getByText(/typed 2 characters/i)).toBeInTheDocument())
    const pressed = rfb.sendKey.mock.calls.filter(([, , down]) => down).map(([, code]) => code)
    expect(pressed).toEqual(["KeyH", "KeyI"])
  })

  it("returns focus to the console once the text has landed", async () => {
    const user = userEvent.setup()
    const rfb = await connectedSession()

    await user.click(screen.getByTitle("Clipboard"))
    await user.type(screen.getByLabelText(/text to type/i), "x")
    await user.click(screen.getByRole("button", { name: /send to vm/i }))

    await waitFor(() => expect(rfb.focus).toHaveBeenCalled())
  })

  it("surfaces clipboard text the session pushes", async () => {
    const user = userEvent.setup()
    const rfb = await connectedSession()

    act(() => {
      rfb.dispatchEvent(new CustomEvent("clipboard", { detail: { text: "from-guest" } }))
    })
    await user.click(screen.getByTitle("Clipboard"))

    expect(screen.getByDisplayValue("from-guest")).toBeInTheDocument()
  })

  it("refuses to type after the session drops", async () => {
    const user = userEvent.setup()
    const rfb = await connectedSession()

    await user.click(screen.getByTitle("Clipboard"))
    await user.type(screen.getByLabelText(/text to type/i), "hi")
    expect(screen.getByRole("button", { name: /send to vm/i })).toBeEnabled()

    act(() => {
      rfb.dispatchEvent(new CustomEvent("disconnect", { detail: { clean: true } }))
    })

    expect(screen.getByRole("button", { name: /send to vm/i })).toBeDisabled()
  })
})
