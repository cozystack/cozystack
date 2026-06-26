import { useMemo } from "react"
import { Link } from "react-router"
import { Section, Spinner } from "@cozystack/ui"
import { useK8sList, K8sApiError } from "@cozystack/k8s-client"
import { parseQuantity, humanizeBytes } from "../../lib/k8s-quantity.ts"
import { TENANT_NAMESPACE_PREFIX } from "../../lib/constants.ts"
import type { Pvc } from "../../lib/cluster-usage/types.ts"

interface StorageClassRow {
  storageClass: string
  pvcs: number
  requested: number
  bound: number
}

const NO_CLASS = "(no class)"

/**
 * Persistent Storage panel on the Cluster page: PersistentVolumeClaims across
 * tenant namespaces aggregated by StorageClass (claim count, total requested,
 * total bound capacity). Unlike the node-allocatable resources above there is
 * no fixed cap — dynamic provisioners have no cluster-wide allocatable — so
 * this is a usage tally rather than a gauge. Each StorageClass links to a
 * per-class drill-down of the consuming workloads.
 */
export function ClusterStorageSection() {
  const { data, isLoading, error } = useK8sList<Pvc>({
    apiGroup: "",
    apiVersion: "v1",
    plural: "persistentvolumeclaims",
  })

  const rows = useMemo<StorageClassRow[]>(() => {
    const byClass = new Map<string, StorageClassRow>()
    for (const pvc of data?.items ?? []) {
      const ns = pvc.metadata.namespace ?? ""
      if (!ns.startsWith(TENANT_NAMESPACE_PREFIX)) continue
      const sc = pvc.spec?.storageClassName || NO_CLASS
      const requested = parseQuantity(pvc.spec?.resources?.requests?.storage ?? "0")
      const bound = parseQuantity(pvc.status?.capacity?.storage ?? "0")
      const existing = byClass.get(sc)
      if (existing) {
        existing.pvcs += 1
        existing.requested += requested
        existing.bound += bound
      } else {
        byClass.set(sc, { storageClass: sc, pvcs: 1, requested, bound })
      }
    }
    return [...byClass.values()].sort((a, b) => a.storageClass.localeCompare(b.storageClass))
  }, [data])

  if (isLoading) {
    return (
      <div className="flex items-center gap-2 text-sm text-slate-500">
        <Spinner /> Loading…
      </div>
    )
  }

  if (error) {
    const forbidden = error instanceof K8sApiError && error.status === 403
    return (
      <Section>
        <p className="px-2 py-4 text-sm text-red-700">
          {forbidden
            ? "You do not have permission to view persistent volume claims."
            : `Failed to load persistent volume claims: ${error.message}`}
        </p>
      </Section>
    )
  }

  if (rows.length === 0) {
    return (
      <Section>
        <p className="py-6 text-center text-sm text-slate-500">
          No persistent volume claims found.
        </p>
      </Section>
    )
  }

  return (
    <Section>
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-slate-200 bg-slate-50 text-left">
            <th className="px-3 py-2 font-medium text-slate-600">Storage Class</th>
            <th className="px-3 py-2 text-right font-medium text-slate-600">PVCs</th>
            <th className="px-3 py-2 text-right font-medium text-slate-600">Requested</th>
            <th className="px-3 py-2 text-right font-medium text-slate-600">Bound</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100">
          {rows.map((r) => (
            <tr key={r.storageClass} data-storageclass-row={r.storageClass} className="hover:bg-slate-50">
              <td className="px-3 py-2">
                {r.storageClass === NO_CLASS ? (
                  <span className="font-medium text-slate-700">{r.storageClass}</span>
                ) : (
                  <Link
                    to={`/admin/capacity/cluster/sc/${r.storageClass}`}
                    className="font-medium break-all text-blue-700 hover:text-blue-800 hover:underline"
                  >
                    {r.storageClass}
                  </Link>
                )}
              </td>
              <td className="px-3 py-2 text-right tabular-nums text-slate-600">{r.pvcs}</td>
              <td className="px-3 py-2 text-right tabular-nums text-slate-700">
                {humanizeBytes(r.requested)}
              </td>
              <td className="px-3 py-2 text-right tabular-nums text-slate-600">
                {r.bound > 0 ? humanizeBytes(r.bound) : "—"}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </Section>
  )
}
