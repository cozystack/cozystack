import type { K8sCondition } from "@cozystack/k8s-client"
import type { ApplicationInstance } from "@cozystack/types"

// Ready follows the Helm release only, so a vm-disk whose DataVolume never
// populates is Ready. Other WorkloadsReady=False causes are left out on purpose:
// a stopped VM has no pod and reports that too.
const dataVolumeNotReady = "DataVolumeNotReady"

export function readyCondition(
  instance: ApplicationInstance | undefined,
): K8sCondition | undefined {
  const conditions = instance?.status?.conditions
  const ready = conditions?.find((c) => c.type === "Ready")
  const workloads = conditions?.find((c) => c.type === "WorkloadsReady")
  if (ready?.status === "True" && workloads?.reason === dataVolumeNotReady && workloads.status !== "True") {
    return workloads
  }
  return ready
}

export function formatAge(timestamp: string | undefined): string {
  if (!timestamp) return "—"
  const diff = Date.now() - new Date(timestamp).getTime()
  const s = Math.floor(diff / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h`
  const d = Math.floor(h / 24)
  return `${d}d`
}
