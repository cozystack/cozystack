import { Link } from "react-router"
import { Section, Spinner } from "@cozystack/ui"
import { useClusterUsageData } from "../hooks/useClusterUsageData.tsx"
import { ClusterUsageAggregates } from "../components/cluster-usage/ClusterUsageAggregates.tsx"

/**
 * Administration → Cluster Usage. Single cluster-scoped page that
 * renders aggregate utilisation on top and a per-node table below.
 * Both panels read from the same useClusterUsageData composite hook,
 * so they always agree on totals.
 *
 * Tenant-scoped users never reach this page through normal navigation
 * because the sidebar entry is gated by a SelfSubjectAccessReview on
 * `nodes list`. On direct URL navigation a 403 message with a link
 * back to the console is shown instead of a browser 403; richer
 * page-level fallbacks (read-only view via cached metrics, etc.) are
 * explicitly out of scope for the first iteration.
 */
export function ClusterUsagePage() {
  const {
    nodes,
    aggregates,
    nodeSummary,
    isLoading,
    error,
    errorStatus,
    podsUnavailable,
  } = useClusterUsageData()

  return (
    <div className="space-y-6 p-6">
      <div>
        <h1 className="text-xl font-semibold text-slate-900">Cluster</h1>
        <p className="mt-0.5 text-sm text-slate-500">
          Cluster-scoped capacity, allocation and usage across all nodes,
          including any discovered extended resources.
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
              <Link
                to="/console"
                className="text-blue-700 underline hover:text-blue-800"
              >
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
        <ClusterUsageAggregates
          aggregates={aggregates}
          nodeSummary={nodeSummary}
          podsUnavailable={podsUnavailable}
        />
      )}
    </div>
  )
}
