import { useMemo } from "react"
import {
  useK8sList,
  useApiGroupAvailable,
  K8sApiError,
  type K8sList,
} from "@cozystack/k8s-client"
import { aggregateNodeResources } from "../lib/cluster-usage/aggregate.ts"
import { derivePerNodeRows } from "../lib/cluster-usage/per-node.ts"
import type {
  AggregateResources,
  Node,
  NodeMetrics,
  NodeRow,
  Pod,
} from "../lib/cluster-usage/types.ts"

/**
 * Polling interval for NodeMetrics. Matches the default
 * --metric-resolution of metrics-server (15s) plus a small buffer; a
 * faster cadence returns identical values and wastes requests.
 */
export const CLUSTER_USAGE_METRICS_REFETCH_MS = 30_000

export interface NodeSummary {
  total: number
  ready: number
  notReady: number
  schedulingDisabled: number
}

interface ClusterUsageData {
  nodes: Node[]
  pods: Pod[]
  metrics: NodeMetrics[] | undefined
  aggregates: AggregateResources
  perNode: NodeRow[]
  nodeSummary: NodeSummary
  isLoading: boolean
  /**
   * The hook's primary error: a nodes-list failure. Pods and metrics
   * failures are surfaced through their own flags so callers can degrade
   * gracefully instead of replacing the whole page with an error block.
   */
  error: Error | null
  /** HTTP status of `error`, if it was a K8sApiError. */
  errorStatus: number | null
  /** True when the cluster-wide pods list failed. Requested values are unreliable. */
  podsUnavailable: boolean
  metricsAvailable: boolean
}

/**
 * Composite hook that powers the Cluster Usage admin page. Subscribes
 * to nodes and pods via K8s watches (push-based updates, no polling),
 * and — only when metrics.k8s.io is discovered on the cluster — polls
 * NodeMetrics on a 30-second cadence. metrics.k8s.io is not watchable,
 * so a refetch interval is the only option; the rest of the page works
 * fine without it.
 *
 * The pods watch is cluster-wide and unfiltered. On a multi-thousand-
 * pod cluster that is a few megabytes of JSON kept hot in memory plus
 * continuous patch events. The trade-off is accepted for now because
 * (a) Requested totals need every pod regardless of namespace, and
 * (b) the watch already exists for the rest of the console. If the
 * cost ever becomes painful, the natural follow-up is a field-selector
 * projection on spec.nodeName + containers[*].resources.requests, or
 * a server-side aggregation endpoint.
 *
 * A 403 on the metrics fetch is treated as 'no usage data, no
 * page-level error' — the Used overlay disappears, the rest of the
 * panel still renders. A pods-list error is surfaced through the
 * `podsUnavailable` flag so the page can degrade gracefully. A
 * nodes-list error is the only kind that takes over the page; everything
 * downstream of it is undefined.
 */
export function useClusterUsageData(): ClusterUsageData {
  const nodesQuery = useK8sList<Node>({
    apiGroup: "",
    apiVersion: "v1",
    plural: "nodes",
  })

  const podsQuery = useK8sList<Pod>({
    apiGroup: "",
    apiVersion: "v1",
    plural: "pods",
  })

  const { available: metricsAvailable, isLoading: metricsDiscoveryLoading } =
    useApiGroupAvailable("metrics.k8s.io")

  const metricsQuery = useK8sList<NodeMetrics>(
    {
      apiGroup: "metrics.k8s.io",
      apiVersion: "v1beta1",
      plural: "nodes",
    },
    {
      enabled: metricsAvailable,
      watch: false,
      refetchInterval: CLUSTER_USAGE_METRICS_REFETCH_MS,
    },
  )

  const nodes = useMemo<Node[]>(
    () => nodesQuery.data?.items ?? [],
    [nodesQuery.data],
  )
  const pods = useMemo<Pod[]>(() => podsQuery.data?.items ?? [], [podsQuery.data])
  const metricsItems = useMemo(
    () => metricsQueryItems(metricsQuery.data, metricsQuery.error),
    [metricsQuery.data, metricsQuery.error],
  )

  const aggregates = useMemo(
    () => aggregateNodeResources(nodes, pods, metricsItems),
    [nodes, pods, metricsItems],
  )
  const perNode = useMemo(
    () => derivePerNodeRows(nodes, pods, metricsItems),
    [nodes, pods, metricsItems],
  )
  const nodeSummary = useMemo<NodeSummary>(() => {
    let ready = 0
    let notReady = 0
    let schedulingDisabled = 0
    for (const row of perNode) {
      if (!row.ready) notReady++
      else if (!row.schedulable) schedulingDisabled++
      else ready++
    }
    return { total: perNode.length, ready, notReady, schedulingDisabled }
  }, [perNode])

  const nodesError = (nodesQuery.error as Error | null) ?? null
  const errorStatus = nodesError instanceof K8sApiError ? nodesError.status : null

  return {
    nodes,
    pods,
    metrics: metricsItems,
    aggregates,
    perNode,
    nodeSummary,
    isLoading:
      nodesQuery.isLoading || podsQuery.isLoading || metricsDiscoveryLoading,
    // Pods and metrics errors are not promoted to page-level errors.
    // The caller renders cell-level placeholders instead.
    error: nodesError,
    errorStatus,
    podsUnavailable: podsQuery.error != null,
    metricsAvailable,
  }
}

function metricsQueryItems(
  list: K8sList<NodeMetrics> | undefined,
  error: unknown,
): NodeMetrics[] | undefined {
  if (error) return undefined
  if (!list) return undefined
  return list.items
}
