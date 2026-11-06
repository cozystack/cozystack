import { describe, it, expect } from "vitest"
import type { ApplicationInstance } from "@cozystack/types"
import { consoleSocketPath, vmConsoleApi, vmPowerStatus, vmSubstrate } from "./vm-substrate.ts"

function instance(spec?: Record<string, unknown>): ApplicationInstance {
  return {
    apiVersion: "apps.cozystack.io/v1alpha1",
    kind: "VMInstance",
    metadata: { name: "demo-vm", namespace: "tenant-root" },
    ...(spec ? { spec } : {}),
  }
}

describe("vmSubstrate", () => {
  it("is kubevirt unless the VMInstance says proxmox", () => {
    expect(vmSubstrate(instance())).toBe("kubevirt")
    expect(vmSubstrate(instance({ substrate: "kubevirt" }))).toBe("kubevirt")
    expect(vmSubstrate(instance({ substrate: "proxmox" }))).toBe("proxmox")
  })
})

describe("vmConsoleApi", () => {
  it("routes KubeVirt consoles to the VirtualMachineInstance and power to the VirtualMachine", () => {
    const api = vmConsoleApi("kubevirt")
    expect(api.vm).toEqual({ apiGroup: "kubevirt.io", apiVersion: "v1", plural: "virtualmachines" })
    expect(consoleSocketPath(api, "tenant-root", "vm-instance-demo-vm", "vnc")).toBe(
      "/k8s/apis/subresources.kubevirt.io/v1/namespaces/tenant-root/virtualmachineinstances/vm-instance-demo-vm/vnc",
    )
    expect(api.subresources.powerPlural).toBe("virtualmachines")
  })

  it("routes Proxmox consoles and power to the platform's aggregated API on the VirtualMachine", () => {
    const api = vmConsoleApi("proxmox")
    expect(api.vm).toEqual({
      apiGroup: "proxmox.alperen.cloud",
      apiVersion: "v1alpha1",
      plural: "virtualmachines",
    })
    expect(consoleSocketPath(api, "tenant-root", "vm-instance-demo-vm", "console")).toBe(
      "/k8s/apis/subresources.proxmox.cozystack.io/v1alpha1/namespaces/tenant-root/virtualmachines/vm-instance-demo-vm/console",
    )
    expect(api.subresources).toMatchObject({
      apiGroup: "subresources.proxmox.cozystack.io",
      apiVersion: "v1alpha1",
      powerPlural: "virtualmachines",
    })
  })
})

describe("vmPowerStatus", () => {
  it("reads KubeVirt's printableStatus as is", () => {
    expect(vmPowerStatus("kubevirt", { status: { printableStatus: "Paused" } })).toBe("Paused")
    expect(vmPowerStatus("kubevirt", undefined)).toBeUndefined()
  })

  it("prints kubemox's hypervisor state in KubeVirt's vocabulary", () => {
    expect(vmPowerStatus("proxmox", { status: { status: { state: "running" } } })).toBe("Running")
    expect(vmPowerStatus("proxmox", { status: { status: { state: "stopped" } } })).toBe("Stopped")
    expect(vmPowerStatus("proxmox", { status: { status: {} } })).toBeUndefined()
    expect(vmPowerStatus("proxmox", { status: {} })).toBeUndefined()
  })
})
