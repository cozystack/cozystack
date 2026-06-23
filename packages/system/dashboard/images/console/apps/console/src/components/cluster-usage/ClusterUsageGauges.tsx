import { Link } from "react-router"
import { GaugeCard, type QuotaEntry } from "../QuotaDisplay.tsx"
import { humanizeBytes, humanizeCpu } from "../../lib/k8s-quantity.ts"
import type {
  AggregateResources,
  ResourceTotals,
  StandardResourceKey,
} from "../../lib/cluster-usage/types.ts"

interface ClusterUsageGaugesProps {
  aggregates: AggregateResources
  /** When true, Requested is unknown, so the request-vs-allocatable gauges are hidden. */
  podsUnavailable?: boolean
}

// `linkKey` is the resource key used to deep-link the gauge to the per-resource
// consumer drill-down. Pods is not a requestable container resource, so it has
// no drill-down (matching the resources table).
const STANDARD: {
  key: StandardResourceKey
  label: string
  format: (n: number) => string
  linkKey: string | null
}[] = [
  { key: "cpu", label: "CPU", format: humanizeCpu, linkKey: "cpu" },
  { key: "memory", label: "Memory", format: humanizeBytes, linkKey: "memory" },
  { key: "ephemeral-storage", label: "Storage", format: humanizeBytes, linkKey: "ephemeral-storage" },
  { key: "pods", label: "Pods", format: (n) => String(n), linkKey: null },
]

/** Build a quota-style gauge entry from cluster totals (requested vs allocatable). */
function entryFrom(
  label: string,
  totals: ResourceTotals | undefined,
  format: (n: number) => string,
): QuotaEntry | null {
  if (!totals || totals.allocatable <= 0) return null
  const usedNum = totals.requested
  const hardNum = totals.allocatable
  const pctReal = (usedNum / hardNum) * 100
  return {
    label,
    usedRaw: String(usedNum),
    hardRaw: String(hardNum),
    usedNum,
    hardNum,
    pct: Math.min(100, pctReal),
    pctReal,
    display: `${format(usedNum)} / ${format(hardNum)}`,
  }
}

/**
 * Cluster-wide allocation gauges: one ring per resource showing Requested vs
 * Allocatable, reusing the quota GaugeCard so it matches the per-tenant quota
 * rings. Each ring links to the per-resource consumer drill-down (except Pods,
 * which is not a requestable resource). Hidden when the cluster-wide pods list
 * is unavailable (Requested would be unknown and every ring would read 0%).
 */
export function ClusterUsageGauges({
  aggregates,
  podsUnavailable = false,
}: ClusterUsageGaugesProps) {
  if (podsUnavailable) return null

  const extendedKeys = Object.keys(aggregates.extended).sort()
  const cards: { entry: QuotaEntry; linkKey: string | null }[] = [
    ...STANDARD.map((s) => ({
      entry: entryFrom(s.label, aggregates.standard[s.key], s.format),
      linkKey: s.linkKey,
    })),
    ...extendedKeys.map((k) => ({
      entry: entryFrom(k, aggregates.extended[k], (n) => String(n)),
      linkKey: k,
    })),
  ].filter((c): c is { entry: QuotaEntry; linkKey: string | null } => c.entry !== null)

  if (cards.length === 0) return null

  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
      {cards.map(({ entry, linkKey }, i) =>
        linkKey ? (
          <Link
            key={entry.label}
            to={`/admin/capacity/cluster/r/${linkKey}`}
            className="block rounded-xl outline-none transition hover:opacity-90 focus-visible:ring-2 focus-visible:ring-blue-400"
          >
            <GaugeCard entry={entry} index={i} />
          </Link>
        ) : (
          <GaugeCard key={entry.label} entry={entry} index={i} />
        ),
      )}
    </div>
  )
}
