import { Link } from "react-router"
import { humanizeBytes, humanizeCpu } from "../../lib/k8s-quantity.ts"
import type {
  AggregateResources,
  ResourceTotals,
  StandardResourceKey,
} from "../../lib/cluster-usage/types.ts"
import type { NodeSummary } from "../../hooks/useClusterUsageData.tsx"
import { ClusterUsageGauges } from "./ClusterUsageGauges.tsx"

interface ClusterUsageAggregatesProps {
  aggregates: AggregateResources
  /** Counts shown in the panel header — Ready / NotReady / SchedulingDisabled. */
  nodeSummary: NodeSummary
  /**
   * When true, every Requested figure is replaced with an em dash and a
   * tooltip explaining that cluster-wide pod read access is required.
   * Set by the page when the underlying pods watch failed.
   */
  podsUnavailable?: boolean
}

type ResourceFormat = "cpu" | "bytes" | "count"

interface ResourceRow {
  /** Display label. */
  label: string
  totals: ResourceTotals
  format: ResourceFormat
  /**
   * Resource key as it appears in pod container requests, used to build the
   * drill-down link. Null for rows that are not a requestable resource
   * (e.g. the node Pods count), which then render without a link.
   */
  linkKey: string | null
}

const STANDARD_ROWS: { key: StandardResourceKey; label: string; format: ResourceFormat; requestable: boolean }[] = [
  { key: "cpu", label: "CPU", format: "cpu", requestable: true },
  { key: "memory", label: "Memory", format: "bytes", requestable: true },
  { key: "ephemeral-storage", label: "Storage", format: "bytes", requestable: true },
  { key: "pods", label: "Pods", format: "count", requestable: false },
]

function formatValue(value: number, format: ResourceFormat): string {
  switch (format) {
    case "cpu":
      return humanizeCpu(value)
    case "bytes":
      return humanizeBytes(value)
    case "count":
    default:
      return value % 1 === 0 ? `${value}` : value.toFixed(2)
  }
}

function percent(value: number, allocatable: number): number | null {
  if (allocatable <= 0) return null
  return Math.min(100, Math.round((value / allocatable) * 100))
}

/**
 * Top panel of the Cluster Usage admin page. A header line shows total
 * node count broken down by Ready / NotReady / SchedulingDisabled,
 * followed by a single resources table laid out TOP-TO-BOTTOM: one row per
 * resource (the standard scheduler resources first, then every extended
 * resource discovered in node.status.capacity, alphabetical, full key
 * verbatim). Each requestable resource row links to a drill-down showing
 * which tenants/workloads consume it.
 */
export function ClusterUsageAggregates({
  aggregates,
  nodeSummary,
  podsUnavailable = false,
}: ClusterUsageAggregatesProps) {
  const extendedKeys = Object.keys(aggregates.extended).sort()

  const rows: ResourceRow[] = [
    ...STANDARD_ROWS.map((r) => ({
      label: r.label,
      totals: aggregates.standard[r.key],
      format: r.format,
      linkKey: r.requestable ? r.key : null,
    })),
    ...extendedKeys.map((key) => ({
      label: key,
      totals: aggregates.extended[key],
      format: "count" as ResourceFormat,
      linkKey: key,
    })),
  ]

  const REQUESTED_UNAVAILABLE_REASON = "Requires cluster-wide pod read access"

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 text-sm">
        <span className="font-medium text-slate-800">
          {nodeSummary.total} node{nodeSummary.total === 1 ? "" : "s"}
        </span>
        <span className="text-xs text-slate-500">
          {nodeSummary.ready} Ready · {nodeSummary.notReady} NotReady ·{" "}
          {nodeSummary.schedulingDisabled} SchedulingDisabled
        </span>
      </div>

      <ClusterUsageGauges aggregates={aggregates} podsUnavailable={podsUnavailable} />

      <div className="overflow-hidden rounded-lg border border-slate-200 bg-white shadow-sm">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-slate-200 bg-slate-50 text-left">
              <th className="px-3 py-2 font-medium text-slate-600">Resource</th>
              <th className="px-3 py-2 text-right font-medium text-slate-600">Capacity</th>
              <th className="px-3 py-2 text-right font-medium text-slate-600">Allocatable</th>
              <th className="px-3 py-2 text-right font-medium text-slate-600">Requested</th>
              <th className="px-3 py-2 text-right font-medium text-slate-600">Used</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100">
            {rows.map((row) => {
              const allocatableZero = row.totals.allocatable <= 0
              const requestedPct = percent(row.totals.requested, row.totals.allocatable)
              const usedDefined = row.totals.used !== undefined
              return (
                <tr
                  key={row.label}
                  data-resource-row={row.label}
                  className="hover:bg-slate-50"
                >
                  <td className="px-3 py-2">
                    {row.linkKey ? (
                      <Link
                        to={`/admin/capacity/cluster/r/${row.linkKey}`}
                        className="font-medium break-all text-blue-700 hover:text-blue-800 hover:underline"
                      >
                        {row.label}
                      </Link>
                    ) : (
                      <span className="font-medium break-all text-slate-700">{row.label}</span>
                    )}
                  </td>
                  <td className="px-3 py-2 text-right tabular-nums text-slate-600">
                    {allocatableZero ? "—" : formatValue(row.totals.capacity, row.format)}
                  </td>
                  <td className="px-3 py-2 text-right tabular-nums text-slate-600">
                    {allocatableZero ? "—" : formatValue(row.totals.allocatable, row.format)}
                  </td>
                  <td
                    className="px-3 py-2 text-right tabular-nums text-slate-700"
                    title={podsUnavailable ? REQUESTED_UNAVAILABLE_REASON : undefined}
                  >
                    {podsUnavailable || allocatableZero
                      ? "—"
                      : `${formatValue(row.totals.requested, row.format)}${
                          requestedPct !== null ? ` (${requestedPct}%)` : ""
                        }`}
                  </td>
                  <td className="px-3 py-2 text-right tabular-nums text-slate-600">
                    {usedDefined && !allocatableZero
                      ? formatValue(row.totals.used ?? 0, row.format)
                      : "—"}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}
