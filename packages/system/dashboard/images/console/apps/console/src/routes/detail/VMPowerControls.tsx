import { useState } from "react"
import { Play, Square, RotateCw } from "lucide-react"
import { Button, StatusBadge } from "@cozystack/ui"
import {
  useK8sList,
  useK8sSubresource,
  type K8sResource,
} from "@cozystack/k8s-client"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import { releasePrefix } from "../../lib/app-definitions.ts"
import { vmConsoleApi, vmPowerStatus, vmSubstrate, type VMStatus } from "../../lib/vm-substrate.ts"

// The VirtualMachine object and the start/stop/restart action endpoints live
// in different groups, which the substrate of the VMInstance picks: KubeVirt
// serves the object under kubevirt.io and the actions under the
// subresources.kubevirt.io aggregated API; on Proxmox the object is kubemox's
// and the actions come from subresources.proxmox.cozystack.io. Tenant access
// to the action subresources is granted by the cozy:tenant:use ClusterRole
// (verb "update") for both.

type Power = "start" | "stop" | "restart"

export function VMPowerControls({
  ad,
  instance,
}: {
  ad: ApplicationDefinition
  instance: ApplicationInstance
}) {
  const ns = instance.metadata.namespace ?? ""
  // The cozystack app name (e.g. "demo-vm") maps to the KubeVirt VirtualMachine
  // named "<release.prefix><name>" (e.g. "vm-instance-demo-vm"). releasePrefix()
  // discovers the prefix from the ApplicationDefinition (falling back to
  // "<singular>-") so this resolves identically to VncTab — never hardcode it.
  const vmName = `${releasePrefix(ad)}${instance.metadata.name}`
  const substrate = vmSubstrate(instance)
  const api = vmConsoleApi(substrate)

  // The VirtualMachine object (and its status) is served by the substrate's
  // group. List it by a metadata.name field-selector rather than a one-shot
  // get so the useK8sList watch layer streams status transitions live — no poll.
  const vmListRef = { ...api.vm, namespace: ns }
  const { data: vmList } = useK8sList<K8sResource<unknown, VMStatus>>(vmListRef, {
    enabled: !!vmName && !!ns,
    fieldSelector: `metadata.name=${vmName}`,
  })
  const vm = vmList?.items[0]

  const action = useK8sSubresource(
    {
      apiGroup: api.subresources.apiGroup,
      apiVersion: api.subresources.apiVersion,
      plural: api.subresources.powerPlural,
      name: vmName,
      namespace: ns,
    },
    // The action endpoints live in the aggregated API, but the status lives
    // on the VirtualMachine object. Invalidate that resource so the
    // field-selected list above refetches immediately (the watch would catch
    // up on its own, but this makes the button feedback instant).
    { invalidate: vmListRef },
  )

  const [pending, setPending] = useState<Power | null>(null)

  const status = vmPowerStatus(substrate, vm)
  const isRunning = status === "Running"
  const isStopped = status === "Stopped"
  // A paused VM still has a running VirtualMachineInstance, so stop/restart
  // apply to it just as they do to a running one.
  const hasRunningInstance = isRunning || status === "Paused"
  const busy = action.isPending || pending !== null

  const run = async (sub: Power, confirmMsg?: string) => {
    if (confirmMsg && !confirm(confirmMsg)) return
    setPending(sub)
    try {
      await action.mutateAsync({ subresource: sub })
    } catch (err) {
      alert(`Failed to ${sub} VM: ${(err as Error).message}`)
    } finally {
      setPending(null)
    }
  }

  return (
    <div className="flex items-center gap-2">
      {status && (
        <StatusBadge tone={isRunning ? "ok" : "warn"}>{status}</StatusBadge>
      )}
      <Button
        variant="outline"
        size="sm"
        disabled={busy || !isStopped}
        onClick={() => run("start")}
      >
        <Play className="size-3.5" /> Start
      </Button>
      <Button
        variant="outline"
        size="sm"
        disabled={busy || !hasRunningInstance}
        onClick={() =>
          run("restart", `Restart VM "${instance.metadata.name}"?`)
        }
      >
        <RotateCw className="size-3.5" /> Restart
      </Button>
      <Button
        variant="outline"
        size="sm"
        disabled={busy || !hasRunningInstance}
        onClick={() => run("stop", `Stop VM "${instance.metadata.name}"?`)}
      >
        <Square className="size-3.5" /> Stop
      </Button>
    </div>
  )
}
