import { useMemo } from "react"
import { Link, useParams } from "react-router"
import { Section, Spinner } from "@cozystack/ui"
import { useK8sList } from "@cozystack/k8s-client"
import { ChevronLeft } from "lucide-react"
import { parseQuantity, humanizeBytes } from "../lib/k8s-quantity.ts"
import { workloadOwner } from "../lib/workload.ts"
import { TENANT_NAMESPACE_PREFIX } from "../lib/constants.ts"
import { WorkloadCell } from "../components/WorkloadCell.tsx"
import type { Pvc } from "../lib/cluster-usage/types.ts"

interface UsageRow {
  namespace: string
  kind: string
  name: string
  requested: number
}

/**
 * Admin → Resources → per-StorageClass drill-down. Reached from the Persistent
 * Storage panel (/admin/capacity/cluster/sc/<class>). Lists the workloads that
 * own PersistentVolumeClaims in that StorageClass across tenant namespaces,
 * summing requested storage and grouping by the owning application (the same
 * Workload abstraction used for the node-resource drill-down). The class name
 * arrives via a splat param so names with slashes survive routing.
 */
export function StorageClassUsagePage() {
  const params = useParams()
  const storageClass = params["*"] ?? ""

  const { data, isLoading, error } = useK8sList<Pvc>({
    apiGroup: "",
    apiVersion: "v1",
    plural: "persistentvolumeclaims",
  })

  const { rows, totalRequested } = useMemo(() => {
    const byKey = new Map<string, UsageRow>()
    let totalRequested = 0
    for (const pvc of data?.items ?? []) {
      if ((pvc.spec?.storageClassName || "") !== storageClass) continue
      const namespace = pvc.metadata.namespace ?? "—"
      if (!namespace.startsWith(TENANT_NAMESPACE_PREFIX)) continue
      const requested = parseQuantity(pvc.spec?.resources?.requests?.storage ?? "0")
      const { kind, name } = workloadOwner(pvc.metadata.labels, pvc.metadata.name)
      const key = `${namespace}/${kind}/${name}`
      const existing = byKey.get(key)
      if (existing) existing.requested += requested
      else byKey.set(key, { namespace, kind, name, requested })
      totalRequested += requested
    }
    const rows = [...byKey.values()].sort((a, b) => b.requested - a.requested)
    return { rows, totalRequested }
  }, [data, storageClass])

  return (
    <div className="space-y-6 p-6">
      <div>
        <Link
          to="/admin/capacity/cluster"
          className="mb-2 inline-flex items-center gap-1 text-sm text-slate-500 hover:text-slate-700"
        >
          <ChevronLeft className="size-3.5" /> Cluster
        </Link>
        <h1 className="font-mono text-xl font-semibold break-all text-slate-900">{storageClass}</h1>
        <p className="mt-0.5 text-sm text-slate-500">
          Workloads with PersistentVolumeClaims in this StorageClass across all
          tenants, with their total requested storage.
        </p>
      </div>

      {isLoading ? (
        <div className="flex items-center gap-2 text-sm text-slate-500">
          <Spinner /> Loading…
        </div>
      ) : error ? (
        <Section>
          <div className="px-2 py-4 text-sm text-red-700">
            Failed to load persistent volume claims: {error.message}
          </div>
        </Section>
      ) : rows.length === 0 ? (
        <Section>
          <p className="py-6 text-center text-sm text-slate-500">
            No tenant workloads use <span className="font-mono">{storageClass}</span>.
          </p>
        </Section>
      ) : (
        <Section>
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-slate-200 bg-slate-50 text-left">
                <th className="px-3 py-2 font-medium text-slate-600">Tenant (namespace)</th>
                <th className="px-3 py-2 font-medium text-slate-600">Workload</th>
                <th className="px-3 py-2 text-right font-medium text-slate-600">Requested</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {rows.map((r) => (
                <tr key={`${r.namespace}/${r.kind}/${r.name}`} className="hover:bg-slate-50">
                  <td className="px-3 py-2 text-slate-700">{r.namespace}</td>
                  <td className="px-3 py-2">
                    <WorkloadCell namespace={r.namespace} kind={r.kind} name={r.name} />
                  </td>
                  <td className="px-3 py-2 text-right tabular-nums text-slate-700">
                    {humanizeBytes(r.requested)}
                  </td>
                </tr>
              ))}
            </tbody>
            <tfoot>
              <tr className="border-t border-slate-200 bg-slate-50 font-medium">
                <td className="px-3 py-2 text-slate-700" colSpan={2}>
                  Total · {rows.length} workload{rows.length === 1 ? "" : "s"}
                </td>
                <td className="px-3 py-2 text-right tabular-nums text-slate-700">
                  {humanizeBytes(totalRequested)}
                </td>
              </tr>
            </tfoot>
          </table>
        </Section>
      )}
    </div>
  )
}
