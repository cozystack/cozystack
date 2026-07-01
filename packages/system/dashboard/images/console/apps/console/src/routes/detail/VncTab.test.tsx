import { describe, it, expect, vi, afterEach } from "vitest"
import { screen, waitFor, cleanup } from "@testing-library/react"
import { K8sClient } from "@cozystack/k8s-client"
import { renderWithK8sProvider } from "../../test-utils/render.tsx"
import { VncTab } from "./VncTab.tsx"
import { VMPowerControls } from "./VMPowerControls.tsx"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"

function makeAd(kind: string, release?: { prefix?: string }): ApplicationDefinition {
  return {
    apiVersion: "cozystack.io/v1alpha1",
    kind: "ApplicationDefinition",
    metadata: { name: "virtual-machine" },
    spec: {
      application: {
        kind,
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

// Stopped keeps VncTab off the noVNC code path (no websocket in jsdom) while
// still exercising the name resolution and the gating branches.
function makeClient(printableStatus = "Stopped") {
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

function fieldSelector(client: K8sClient): string | undefined {
  const calls = (client.list as unknown as { mock: { calls: unknown[][] } }).mock.calls
  const search = calls[0]?.[4] as { fieldSelector?: string } | undefined
  return search?.fieldSelector
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe("VncTab gating", () => {
  it("shows the VMInstance-only notice and never queries for non-VM apps", () => {
    const client = makeClient()
    renderWithK8sProvider(
      <VncTab ad={makeAd("Postgres")} instance={instance} />,
      { client },
    )

    expect(screen.getByText(/VNC is only available for VMInstance/i)).toBeInTheDocument()
    expect(client.list).not.toHaveBeenCalled()
  })

  it("shows the not-running notice when the VM is stopped", async () => {
    const client = makeClient("Stopped")
    renderWithK8sProvider(<VncTab ad={makeAd("VMInstance")} instance={instance} />, { client })

    await waitFor(() =>
      expect(screen.getByText(/virtual machine is not running/i)).toBeInTheDocument(),
    )
  })
})

describe("VncTab VM-name resolution", () => {
  it("field-selects the name from the singular when release.prefix is unset", async () => {
    const client = makeClient()
    renderWithK8sProvider(<VncTab ad={makeAd("VMInstance")} instance={instance} />, { client })

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

  it("field-selects the name from an explicit release.prefix", async () => {
    const client = makeClient()
    renderWithK8sProvider(
      <VncTab ad={makeAd("VMInstance", { prefix: "custom-" })} instance={instance} />,
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

describe("VMPowerControls and VncTab agree on the VirtualMachine name", () => {
  it.each([
    ["unset prefix", undefined],
    ["explicit prefix", { prefix: "custom-" }],
  ])("resolve the identical name (%s)", async (_label, release) => {
    const pcClient = makeClient()
    renderWithK8sProvider(
      <VMPowerControls ad={makeAd("VMInstance", release)} instance={instance} />,
      { client: pcClient },
    )
    await waitFor(() => expect(pcClient.list).toHaveBeenCalled())

    const vncClient = makeClient()
    renderWithK8sProvider(
      <VncTab ad={makeAd("VMInstance", release)} instance={instance} />,
      { client: vncClient },
    )
    await waitFor(() => expect(vncClient.list).toHaveBeenCalled())

    expect(fieldSelector(pcClient)).toBe(fieldSelector(vncClient))
  })
})
