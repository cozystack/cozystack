import { APPS_GROUP } from "@cozystack/types"

/**
 * Derive the owning application (kind + name) of a resource from its labels.
 * Cozystack's lineage controller stamps apps.cozystack.io/application.{kind,name}
 * on every workload object (Pods, PVCs, Services, …); we fall back to the Helm
 * instance/name labels and finally to the resource's own name so nothing is
 * silently dropped.
 */
export function workloadOwner(
  labels: Record<string, string> | undefined,
  fallbackName: string,
): { kind: string; name: string } {
  const l = labels ?? {}
  const kind = l[`${APPS_GROUP}/application.kind`]
  const name =
    l[`${APPS_GROUP}/application.name`] ??
    l["app.kubernetes.io/instance"] ??
    l["app.kubernetes.io/name"]
  if (kind && name) return { kind, name }
  if (name) return { kind: kind ?? "—", name }
  return { kind: kind ?? "—", name: fallbackName }
}
