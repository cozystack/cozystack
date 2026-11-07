import { describe, it, expect } from "vitest"
import { consoleSocketPath, vmConsoleApi, vmPowerStatus, vmSubstrate } from "./vm-substrate.ts"

describe("vmSubstrate", () => {
  it("is kubevirt unless the platform config says proxmox", () => {
    expect(vmSubstrate({})).toBe("kubevirt")
    expect(vmSubstrate({ vmSubstrate: "kubevirt" })).toBe("kubevirt")
    expect(vmSubstrate({ vmSubstrate: "proxmox" })).toBe("proxmox")
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
