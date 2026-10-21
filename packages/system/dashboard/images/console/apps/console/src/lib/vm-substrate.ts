import type { ApplicationInstance } from "@cozystack/types"

/**
 * Where a vm-instance runs, and which APIs its console and power buttons go
 * through. Both substrates expose the same shape — a VirtualMachine object
 * with a power state, and an aggregated API with vnc, console, start, stop
 * and restart subresources — under different groups:
 *
 * - KubeVirt serves the object under kubevirt.io and the subresources under
 *   subresources.kubevirt.io (vnc and console on the VirtualMachineInstance).
 * - Proxmox (ADR-0043) keeps the object in kubemox's proxmox.alperen.cloud and
 *   the subresources under subresources.proxmox.cozystack.io, served by the
 *   platform's console proxy, all on the VirtualMachine itself.
 *
 * The VMInstance's spec.substrate picks one; everything else in the tabs is
 * shared.
 */
export type VMSubstrate = "kubevirt" | "proxmox"

export interface VMConsoleApi {
  substrate: VMSubstrate
  /** The VirtualMachine object whose status carries the power state. */
  vm: { apiGroup: string; apiVersion: string; plural: string }
  /** The aggregated API with the vnc, console and power subresources. */
  subresources: {
    apiGroup: string
    apiVersion: string
    /** The resource vnc and console hang off. */
    consolePlural: string
    /** The resource start, stop and restart hang off. */
    powerPlural: string
  }
}

/** The status of a listed VirtualMachine, in either substrate's shape. */
export interface VMStatus {
  /** KubeVirt */
  printableStatus?: string
  /** kubemox: what the hypervisor last reported */
  status?: { state?: string }
}

const KUBEVIRT: VMConsoleApi = {
  substrate: "kubevirt",
  vm: { apiGroup: "kubevirt.io", apiVersion: "v1", plural: "virtualmachines" },
  subresources: {
    apiGroup: "subresources.kubevirt.io",
    apiVersion: "v1",
    consolePlural: "virtualmachineinstances",
    powerPlural: "virtualmachines",
  },
}

const PROXMOX: VMConsoleApi = {
  substrate: "proxmox",
  vm: { apiGroup: "proxmox.alperen.cloud", apiVersion: "v1alpha1", plural: "virtualmachines" },
  subresources: {
    apiGroup: "subresources.proxmox.cozystack.io",
    apiVersion: "v1alpha1",
    consolePlural: "virtualmachines",
    powerPlural: "virtualmachines",
  },
}

export function vmSubstrate(instance: ApplicationInstance): VMSubstrate {
  return instance.spec?.substrate === "proxmox" ? "proxmox" : "kubevirt"
}

export function vmConsoleApi(substrate: VMSubstrate): VMConsoleApi {
  return substrate === "proxmox" ? PROXMOX : KUBEVIRT
}

/**
 * The power state the way KubeVirt prints it, so the tabs and the badge read
 * one vocabulary: Running, Stopped, Paused and the rest.
 */
export function vmPowerStatus(
  substrate: VMSubstrate,
  vm: { status?: VMStatus } | undefined,
): string | undefined {
  if (!vm?.status) return undefined
  if (substrate === "kubevirt") return vm.status.printableStatus
  const state = vm.status.status?.state
  if (!state) return undefined
  return state.charAt(0).toUpperCase() + state.slice(1)
}

/** The path, under the dashboard's /k8s/ proxy, of a console websocket. */
export function consoleSocketPath(
  api: VMConsoleApi,
  namespace: string,
  vmName: string,
  subresource: "vnc" | "console",
): string {
  const { apiGroup, apiVersion, consolePlural } = api.subresources
  return `/k8s/apis/${apiGroup}/${apiVersion}/namespaces/${namespace}/${consolePlural}/${vmName}/${subresource}`
}
