import { useMemo } from "react"
import { Link, useParams } from "react-router"
import { Section, Spinner } from "@cozystack/ui"
import { useK8sList } from "@cozystack/k8s-client"
import { ChevronLeft } from "lucide-react"
import { parseQuantity, humanizeBytes, humanizeCpu } from "../lib/k8s-quantity.ts"
import { workloadOwner } from "../lib/workload.ts"
import { podCountsTowardRequested } from "../lib/cluster-usage/aggregate.ts"
import { TENANT_NAMESPACE_PREFIX } from "../lib/constants.ts"
import { WorkloadCell } from "../components/WorkloadCell.tsx"
import type { Node, Pod } from "../lib/cluster-usage/types.ts"

/**
 * Admin → Resources → per-resource drill-down. Given a resource key
 * (e.g. `cpu`, `memory`, or an extended resource like
 * `nvidia.com/GH100_H200_SXM_141GB`) this lists the tenant workloads requesting
 * it, grouped by namespace and owning application. To stay consistent with the
 * Cluster page headline it counts the same way the aggregate does — requests
 * only (never limits), scheduled non-terminal pods only — but scoped to tenant
 * namespaces, so the Total is the tenant portion of the cluster-wide figure
 * (system/control-plane usage is excluded).
 *
 * Ownership is read from pod labels — Cozystack stamps
 * `apps.cozystack.io/application.{kind,name}` on every workload pod; we
 * fall back to the Helm `app.kubernetes.io/{instance,name}` labels and finally
 * to the bare pod name so nothing is silently dropped.
 *
 * The resource key arrives via a splat param (`cluster-usage/r/*`) so keys
 * containing slashes (every `vendor.com/model` GPU name) survive routing
 * without encoding.
 */

interface UsageRow {
  namespace: string
  kind: string
  name: string
  pods: number
  requested: number
}

function formatResource(resource: string, value: number): string {
  if (resource === "cpu") return humanizeCpu(value)
  if (resource === "memory" || resource === "ephemeral-storage") {
    return humanizeBytes(value)
  }
  return value % 1 === 0 ? `${value}` : value.toFixed(2)
}

/** Sum a single resource's requests across a pod's containers (requests only). */
function podRequestedResource(pod: Pod, resource: string): number {
  let total = 0
  for (const container of pod.spec?.containers ?? []) {
    const value = container.resources?.requests?.[resource]
    if (value !== undefined) total += parseQuantity(value)
  }
  return total
}

export function ClusterUsageResourcePage() {
  const params = useParams()
  const resource = params["*"] ?? ""

  const pods = useK8sList<Pod>({ apiGroup: "", apiVersion: "v1", plural: "pods" })
  const nodes = useK8sList<Node>({ apiGroup: "", apiVersion: "v1", plural: "nodes" })
  const isLoading = pods.isLoading || nodes.isLoading
  const error = pods.error ?? nodes.error

  const { rows, totalRequested } = useMemo(() => {
    const knownNodes = new Set((nodes.data?.items ?? []).map((n) => n.metadata.name))
    const byKey = new Map<string, UsageRow>()
    let totalRequested = 0
    for (const pod of pods.data?.items ?? []) {
      // Match the aggregate's definition of "requested" so this breakdown
      // reconciles with the headline number it drills into.
      if (!podCountsTowardRequested(pod, knownNodes)) continue
      const requested = podRequestedResource(pod, resource)
      if (requested <= 0) continue
      const namespace = pod.metadata.namespace ?? "—"
      // Tenant-scoped: skip system/control-plane namespaces (cozy-*, kube-system,
      // …). The Total is therefore the tenant portion of the cluster figure.
      if (!namespace.startsWith(TENANT_NAMESPACE_PREFIX)) continue
      const { kind, name } = workloadOwner(pod.metadata.labels, pod.metadata.name)
      const key = `${namespace}/${kind}/${name}`
      const existing = byKey.get(key)
      if (existing) {
        existing.pods += 1
        existing.requested += requested
      } else {
        byKey.set(key, { namespace, kind, name, pods: 1, requested })
      }
      totalRequested += requested
    }
    const rows = [...byKey.values()].sort((a, b) => b.requested - a.requested)
    return { rows, totalRequested }
  }, [pods.data, nodes.data, resource])

  return (
    <div className="space-y-6 p-6">
      <div>
        <Link
          to="/admin/capacity/cluster"
          className="mb-2 inline-flex items-center gap-1 text-sm text-slate-500 hover:text-slate-700"
        >
          <ChevronLeft className="size-3.5" /> Cluster
        </Link>
        <h1 className="font-mono text-xl font-semibold break-all text-slate-900">
          {resource}
        </h1>
        <p className="mt-0.5 text-sm text-slate-500">
          Tenant workloads requesting this resource, grouped by namespace and
          owning application (derived from pod labels). System and control-plane
          usage is excluded, so the total is the tenant portion of the
          cluster-wide figure.
        </p>
      </div>

      {isLoading ? (
        <div className="flex items-center gap-2 text-sm text-slate-500">
          <Spinner /> Loading…
        </div>
      ) : error ? (
        <Section>
          <div className="px-2 py-4 text-sm text-red-700">
            Failed to load cluster usage: {error.message}
          </div>
        </Section>
      ) : rows.length === 0 ? (
        <Section>
          <p className="py-6 text-center text-sm text-slate-500">
            No workloads are requesting{" "}
            <span className="font-mono">{resource}</span>.
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
              {rows.map((r) => {
                return (
                  <tr key={`${r.namespace}/${r.kind}/${r.name}`} className="hover:bg-slate-50">
                    <td className="px-3 py-2 text-slate-700">{r.namespace}</td>
                    <td className="px-3 py-2">
                      <WorkloadCell namespace={r.namespace} kind={r.kind} name={r.name} />
                    </td>
                    <td className="px-3 py-2 text-right tabular-nums text-slate-700">
                      {formatResource(resource, r.requested)}
                    </td>
                  </tr>
                )
              })}
            </tbody>
            <tfoot>
              <tr className="border-t border-slate-200 bg-slate-50 font-medium">
                <td className="px-3 py-2 text-slate-700" colSpan={2}>
                  Tenant total · {rows.length} workload{rows.length === 1 ? "" : "s"}
                </td>
                <td className="px-3 py-2 text-right tabular-nums text-slate-700">
                  {formatResource(resource, totalRequested)}
                </td>
              </tr>
            </tfoot>
          </table>
        </Section>
      )}
    </div>
  )
}
