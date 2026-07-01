import { Link } from "react-router"
import { Section, Spinner } from "@cozystack/ui"
import { useClusterUsageData } from "../hooks/useClusterUsageData.tsx"
import { ClusterUsageTable } from "../components/cluster-usage/ClusterUsageTable.tsx"

/**
 * Admin → Resources → Nodes. The per-node breakdown, split out of the
 * Cluster Usage page onto its own tab. Reads the same useClusterUsageData
 * composite hook and renders the transposed node table (nodes as columns,
 * resources/attributes as rows). Gated the same way as Cluster Usage: a
 * direct hit without `nodes/list` shows a 403 notice with a link back.
 */
export function NodesPage() {
  const { nodes, perNode, aggregates, isLoading, error, errorStatus, podsUnavailable } =
    useClusterUsageData()
  const extendedKeys = Object.keys(aggregates.extended).sort()

  return (
    <div className="space-y-6 p-6">
      <div>
        <h1 className="text-xl font-semibold text-slate-900">Nodes</h1>
        <p className="mt-0.5 text-sm text-slate-500">
          Per-node capacity, allocation and usage across the cluster, including
          any discovered extended resources.
        </p>
      </div>
      {isLoading ? (
        <div className="flex items-center gap-2 text-sm text-slate-500">
          <Spinner /> Loading…
        </div>
      ) : error ? (
        <Section>
          {errorStatus === 403 ? (
            <div className="px-2 py-4 text-sm text-slate-700">
              You do not have permission to view cluster nodes.{" "}
              <Link to="/console" className="text-blue-700 underline hover:text-blue-800">
                Back to console
              </Link>
              .
            </div>
          ) : (
            <div className="px-2 py-4 text-sm text-red-700">
              Failed to load cluster nodes: {error.message}
            </div>
          )}
        </Section>
      ) : nodes.length === 0 ? (
        <Section>
          <p className="py-6 text-center text-sm text-slate-500">No nodes found.</p>
        </Section>
      ) : (
        <ClusterUsageTable
          rows={perNode}
          extendedKeys={extendedKeys}
          podsUnavailable={podsUnavailable}
        />
      )}
    </div>
  )
}
