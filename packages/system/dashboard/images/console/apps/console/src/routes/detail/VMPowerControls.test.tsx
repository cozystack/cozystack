import { describe, it, expect, vi, afterEach } from "vitest"
import { screen, waitFor, cleanup } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { K8sClient } from "@cozystack/k8s-client"
import { renderWithK8sProvider } from "../../test-utils/render.tsx"
import { VMPowerControls } from "./VMPowerControls.tsx"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"

function makeAd(release?: { prefix?: string }): ApplicationDefinition {
  return {
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
      ...(release ? { release } : {}),
    },
  }
}

const instance: ApplicationInstance = {
  apiVersion: "apps.cozystack.io/v1alpha1",
  kind: "VMInstance",
  metadata: { name: "demo-vm", namespace: "tenant-root" },
}

const proxmoxInstance: ApplicationInstance = { ...instance, spec: { substrate: "proxmox" } }

function makeClient(printableStatus?: string) {
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
  // No resourceVersion above means useK8sList never upgrades to a watch, but
  // stub it anyway so a future change can't make the test hit the network.
  vi.spyOn(client, "watch").mockReturnValue(() => {})
  vi.spyOn(client, "subresource").mockResolvedValue(undefined)
  return client
}

// On the Proxmox substrate the object is kubemox's VirtualMachine, whose
// status carries what the hypervisor last reported.
function makeProxmoxClient(state?: string) {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockResolvedValue({
    apiVersion: "proxmox.alperen.cloud/v1alpha1",
    kind: "VirtualMachineList",
    metadata: {},
    items: [
      {
        apiVersion: "proxmox.alperen.cloud/v1alpha1",
        kind: "VirtualMachine",
        metadata: { name: "vm-instance-demo-vm" },
        status: { status: { state } },
      },
    ],
  })
  vi.spyOn(client, "watch").mockReturnValue(() => {})
  vi.spyOn(client, "subresource").mockResolvedValue(undefined)
  return client
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe("VMPowerControls VM-name resolution", () => {
  it("field-selects the VM name from the singular when release.prefix is unset", async () => {
    const client = makeClient("Stopped")
    renderWithK8sProvider(<VMPowerControls ad={makeAd()} instance={instance} />, { client })

    await waitFor(() =>
      expect(client.list).toHaveBeenCalledWith(
        "kubevirt.io",
        "v1",
        "virtualmachines",
        "tenant-root",
        expect.objectContaining({ fieldSelector: "metadata.name=vm-instance-demo-vm" }),
      ),
    )
  })

  it("field-selects the VM name from an explicit release.prefix", async () => {
    const client = makeClient("Stopped")
    renderWithK8sProvider(
      <VMPowerControls ad={makeAd({ prefix: "custom-" })} instance={instance} />,
      { client },
    )

    await waitFor(() =>
      expect(client.list).toHaveBeenCalledWith(
        "kubevirt.io",
        "v1",
        "virtualmachines",
        "tenant-root",
        expect.objectContaining({ fieldSelector: "metadata.name=custom-demo-vm" }),
      ),
    )
  })
})

describe("VMPowerControls button state", () => {
  it("enables Start and disables Stop/Restart when the VM is Stopped", async () => {
    const client = makeClient("Stopped")
    renderWithK8sProvider(<VMPowerControls ad={makeAd()} instance={instance} />, { client })

    await waitFor(() => expect(screen.getByText("Stopped")).toBeInTheDocument())
    expect(screen.getByRole("button", { name: "Start" })).toBeEnabled()
    expect(screen.getByRole("button", { name: "Restart" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "Stop" })).toBeDisabled()
  })

  it("disables Start and enables Stop/Restart when the VM is Running", async () => {
    const client = makeClient("Running")
    renderWithK8sProvider(<VMPowerControls ad={makeAd()} instance={instance} />, { client })

    await waitFor(() => expect(screen.getByText("Running")).toBeInTheDocument())
    expect(screen.getByRole("button", { name: "Start" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "Restart" })).toBeEnabled()
    expect(screen.getByRole("button", { name: "Stop" })).toBeEnabled()
  })

  it("treats a Paused VM like a running one for Stop/Restart (it still has a VMI)", async () => {
    const client = makeClient("Paused")
    renderWithK8sProvider(<VMPowerControls ad={makeAd()} instance={instance} />, { client })

    await waitFor(() => expect(screen.getByText("Paused")).toBeInTheDocument())
    expect(screen.getByRole("button", { name: "Start" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "Restart" })).toBeEnabled()
    expect(screen.getByRole("button", { name: "Stop" })).toBeEnabled()
  })
})

describe("VMPowerControls actions", () => {
  it("invokes the start action against the subresources.kubevirt.io API", async () => {
    const client = makeClient("Stopped")
    renderWithK8sProvider(<VMPowerControls ad={makeAd()} instance={instance} />, { client })

    const user = userEvent.setup()
    const startButton = await screen.findByRole("button", { name: "Start" })
    await waitFor(() => expect(startButton).toBeEnabled())
    await user.click(startButton)

    expect(client.subresource).toHaveBeenCalledWith(
      "subresources.kubevirt.io",
      "v1",
      "virtualmachines",
      "vm-instance-demo-vm",
      "start",
      "tenant-root",
      {},
      undefined,
    )
  })
})

describe("VMPowerControls on the Proxmox substrate", () => {
  it("watches the kubemox VirtualMachine of the same name", async () => {
    const client = makeProxmoxClient("stopped")
    renderWithK8sProvider(<VMPowerControls ad={makeAd()} instance={proxmoxInstance} />, { client })

    await waitFor(() =>
      expect(client.list).toHaveBeenCalledWith(
        "proxmox.alperen.cloud",
        "v1alpha1",
        "virtualmachines",
        "tenant-root",
        expect.objectContaining({ fieldSelector: "metadata.name=vm-instance-demo-vm" }),
      ),
    )
  })

  it("reads the hypervisor state as the power status", async () => {
    const client = makeProxmoxClient("running")
    renderWithK8sProvider(<VMPowerControls ad={makeAd()} instance={proxmoxInstance} />, { client })

    await waitFor(() => expect(screen.getByText("Running")).toBeInTheDocument())
    expect(screen.getByRole("button", { name: "Start" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "Stop" })).toBeEnabled()
  })

  it("invokes the start action against subresources.proxmox.cozystack.io", async () => {
    const client = makeProxmoxClient("stopped")
    renderWithK8sProvider(<VMPowerControls ad={makeAd()} instance={proxmoxInstance} />, { client })

    const user = userEvent.setup()
    const startButton = await screen.findByRole("button", { name: "Start" })
    await waitFor(() => expect(startButton).toBeEnabled())
    await user.click(startButton)

    expect(client.subresource).toHaveBeenCalledWith(
      "subresources.proxmox.cozystack.io",
      "v1alpha1",
      "virtualmachines",
      "vm-instance-demo-vm",
      "start",
      "tenant-root",
      {},
      undefined,
    )
  })
})
