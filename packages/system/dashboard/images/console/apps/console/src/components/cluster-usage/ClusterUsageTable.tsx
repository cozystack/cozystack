import { useMemo, useState, type ReactNode } from "react"
import { Link } from "react-router"
import { humanizeBytes, humanizeCpu } from "../../lib/k8s-quantity.ts"
import type { NodeRow, ResourceTotals } from "../../lib/cluster-usage/types.ts"

interface ClusterUsageTableProps {
  rows: NodeRow[]
  extendedKeys: string[]
  /** True when pods-list cluster-wide failed — Requested cells become em dashes with a tooltip. */
  podsUnavailable?: boolean
}

const REQUESTED_UNAVAILABLE_REASON = "Requires cluster-wide pod read access"

function statusLabel(row: NodeRow): string {
  if (!row.ready) return "NotReady"
  if (!row.schedulable) return "SchedulingDisabled"
  return "Ready"
}

function cpuCell(totals: ResourceTotals, ready: boolean, podsUnavailable: boolean) {
  if (!ready || totals.allocatable <= 0) {
    return <div className="text-xs text-slate-400">—</div>
  }
  const hasUsed = totals.used !== undefined
  return (
    <div className="space-y-0.5 text-xs">
      {hasUsed ? (
        <div className="tabular-nums text-slate-700">
          {humanizeCpu(totals.used ?? 0)} / {humanizeCpu(totals.allocatable)} used
        </div>
      ) : null}
      {podsUnavailable ? (
        <div className="text-slate-400" title={REQUESTED_UNAVAILABLE_REASON}>
          — req
        </div>
      ) : (
        <div className="tabular-nums text-slate-500">
          {humanizeCpu(totals.requested)} / {humanizeCpu(totals.allocatable)} req
        </div>
      )}
    </div>
  )
}

function memoryCell(totals: ResourceTotals, ready: boolean, podsUnavailable: boolean) {
  if (!ready || totals.allocatable <= 0) {
    return <div className="text-xs text-slate-400">—</div>
  }
  const hasUsed = totals.used !== undefined
  return (
    <div className="space-y-0.5 text-xs">
      {hasUsed ? (
        <div className="tabular-nums text-slate-700">
          {humanizeBytes(totals.used ?? 0)} / {humanizeBytes(totals.allocatable)} used
        </div>
      ) : null}
      {podsUnavailable ? (
        <div className="text-slate-400" title={REQUESTED_UNAVAILABLE_REASON}>
          — req
        </div>
      ) : (
        <div className="tabular-nums text-slate-500">
          {humanizeBytes(totals.requested)} / {humanizeBytes(totals.allocatable)} req
        </div>
      )}
    </div>
  )
}

function extendedCell(
  totals: ResourceTotals | undefined,
  ready: boolean,
  podsUnavailable: boolean,
) {
  if (!ready || !totals) return <span className="text-xs text-slate-400">—</span>
  return (
    <div className="space-y-0.5 text-xs tabular-nums text-slate-700">
      <div>
        {podsUnavailable ? (
          <span className="text-slate-400" title={REQUESTED_UNAVAILABLE_REASON}>
            —
          </span>
        ) : (
          totals.requested
        )}{" "}
        / {totals.allocatable}
      </div>
      <div className="text-slate-400">capacity {totals.capacity}</div>
    </div>
  )
}

function statusContent(r: NodeRow) {
  return (
    <div className="space-y-1">
      <div className="text-xs text-slate-700">{statusLabel(r)}</div>
      {r.pressureConditions.length > 0 ? (
        <div className="flex flex-wrap gap-1">
          {r.pressureConditions.map((p) => (
            <span
              key={p}
              className="rounded-full bg-amber-50 px-2 py-0.5 text-[11px] text-amber-800"
            >
              {p}
            </span>
          ))}
        </div>
      ) : null}
      {r.taints.length > 0 ? (
        <div className="text-[11px] text-slate-500">+tainted {r.taints.length}</div>
      ) : null}
    </div>
  )
}

function rolesContent(r: NodeRow) {
  if (r.roles.length === 0) return <span className="text-xs text-slate-400">—</span>
  return (
    <div className="flex flex-wrap gap-1 text-xs text-slate-700">
      {r.roles.map((role) => (
        <span key={role} className="rounded-full bg-slate-100 px-2 py-0.5">
          {role}
        </span>
      ))}
    </div>
  )
}

function matchesFilter(row: NodeRow, q: string): boolean {
  const needle = q.trim().toLowerCase()
  if (!needle) return true
  if (row.name.toLowerCase().includes(needle)) return true
  if (row.roles.some((r) => r.toLowerCase().includes(needle))) return true
  return false
}

/**
 * Per-node table, transposed: each NODE is a column and each attribute
 * (Status, Roles, CPU, Memory, every discovered extended-resource key, then
 * Age) is a row, read top-to-bottom. The first column is a sticky label
 * column; node columns scroll horizontally when they overflow. The filter
 * input narrows which node columns are shown (by name or role).
 *
 * NotReady nodes show em dashes for CPU / Memory because status.capacity
 * stops being authoritative. When the cluster-wide pods list failed,
 * Requested figures are replaced by an em dash with an explanatory tooltip.
 */
export function ClusterUsageTable({
  rows,
  extendedKeys,
  podsUnavailable = false,
}: ClusterUsageTableProps) {
  const [filter, setFilter] = useState("")

  const visibleNodes = useMemo(
    () =>
      rows
        .filter((r) => matchesFilter(r, filter))
        .sort((a, b) => a.name.localeCompare(b.name)),
    [rows, filter],
  )

  const labelCell = "sticky left-0 z-10 bg-white px-4 py-3 text-xs font-medium text-slate-600"
  const labelHeader =
    "sticky left-0 z-10 bg-slate-50 px-4 py-3 text-xs font-medium uppercase tracking-wider text-slate-500"

  // Resource rows only — node metadata (Status / Roles / Age) is rendered in
  // each node's column header instead. Every row is a requestable resource,
  // so its label deep-links to the per-resource consumer drill-down.
  const attributeRows: {
    key: string
    label: string
    mono?: boolean
    linkKey?: string
    render: (r: NodeRow) => ReactNode
  }[] = [
    { key: "cpu", label: "CPU", linkKey: "cpu", render: (r) => cpuCell(r.standard.cpu, r.ready, podsUnavailable) },
    { key: "memory", label: "Memory", linkKey: "memory", render: (r) => memoryCell(r.standard.memory, r.ready, podsUnavailable) },
    ...extendedKeys.map((k) => ({
      key: k,
      label: k,
      mono: true,
      linkKey: k,
      render: (r: NodeRow) => extendedCell(r.extended[k], r.ready, podsUnavailable),
    })),
  ]

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-2">
        <input
          type="search"
          placeholder="Filter nodes by name or role…"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          aria-label="Filter nodes"
          className="w-64 max-w-full rounded border border-slate-200 px-3 py-1.5 text-sm focus:border-blue-500 focus:outline-none"
        />
        <span className="text-xs text-slate-500">
          {visibleNodes.length} of {rows.length}
        </span>
      </div>
      <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white shadow-sm">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-slate-200 bg-slate-50 text-left">
              <th className={labelHeader}>Node</th>
              {visibleNodes.map((n) => (
                <th
                  key={n.name}
                  data-node-col={n.name}
                  className="min-w-44 px-4 py-3 text-left align-top font-normal"
                >
                  <div className="space-y-1.5">
                    <div className="text-sm font-semibold text-slate-900">{n.name}</div>
                    {statusContent(n)}
                    {rolesContent(n)}
                    <div className="text-[11px] tabular-nums text-slate-400">Age {n.age}</div>
                  </div>
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100">
            {attributeRows.map((attr) => (
              <tr key={attr.key} data-attribute-row={attr.key} className="hover:bg-slate-50">
                <th
                  scope="row"
                  className={`${labelCell} text-left align-top ${attr.mono ? "font-mono" : ""}`}
                >
                  {attr.linkKey ? (
                    <Link
                      to={`/admin/capacity/cluster/r/${attr.linkKey}`}
                      className="text-blue-700 hover:text-blue-800 hover:underline"
                    >
                      {attr.label}
                    </Link>
                  ) : (
                    attr.label
                  )}
                </th>
                {visibleNodes.map((n) => (
                  <td key={n.name} className="px-4 py-3 align-top">
                    {attr.render(n)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
