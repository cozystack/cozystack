import type { K8sCondition } from "@cozystack/k8s-client"
import type { ApplicationInstance } from "@cozystack/types"

export function readyCondition(
  instance: ApplicationInstance | undefined,
): K8sCondition | undefined {
  return instance?.status?.conditions?.find((c) => c.type === "Ready")
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
