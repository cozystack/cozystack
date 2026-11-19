import { parseQuantity } from "../k8s-quantity.ts"
import { getExtendedResourceKeys } from "./extended-resources.ts"
import type {
  AggregateResources,
  Node,
  NodeMetrics,
  Pod,
  ResourceTotals,
  StandardResourceKey,
} from "./types.ts"
import { STANDARD_RESOURCE_KEYS, STANDARD_RESOURCE_KEY_SET } from "./types.ts"

function emptyTotals(): ResourceTotals {
  return { capacity: 0, allocatable: 0, requested: 0 }
}

/**
 * Whether a pod contributes to requested totals: it must be scheduled to a
 * known node and not terminal. Terminal pods (Succeeded/Failed) still appear in
 * API lists but no longer hold schedulable requests, and unscheduled/orphaned
 * pods aren't attributable to cluster capacity — counting either would inflate
 * the totals. Shared with the per-resource drill-down so its "Requested" tally
 * reconciles with this aggregate (only requests count, never limits).
 */
export function podCountsTowardRequested(pod: Pod, knownNodes: Set<string>): boolean {
  const nodeName = pod.spec?.nodeName
  if (!nodeName || !knownNodes.has(nodeName)) return false
  const phase = pod.status?.phase
  return phase !== "Succeeded" && phase !== "Failed"
}

/**
 * Computes cluster-wide totals for every standard and extended resource.
 *
 * Capacity and allocatable are summed from each node's status maps.
 * Requested is summed only from pods that are scheduled (have a
 * spec.nodeName) and whose nodeName actually appears in the node list;
 * unscheduled or orphaned pods are skipped so the per-node and aggregate
 * numbers stay reconcilable.
 *
 * Used is only populated for cpu and memory, mirroring what
 * metrics.k8s.io reports; ephemeral-storage and pods never get a 'used'
 * value because the API simply does not expose one.
 */
export function aggregateNodeResources(
  nodes: Node[],
  pods: Pod[],
  metrics: NodeMetrics[] | undefined,
): AggregateResources {
  const standard: Record<StandardResourceKey, ResourceTotals> = {
    cpu: emptyTotals(),
    memory: emptyTotals(),
    "ephemeral-storage": emptyTotals(),
    pods: emptyTotals(),
  }
  const extended: Record<string, ResourceTotals> = {}
  const knownNodes = new Set(nodes.map((n) => n.metadata.name))
  const extendedKeys = getExtendedResourceKeys(nodes)
  for (const key of extendedKeys) extended[key] = emptyTotals()

  for (const node of nodes) {
    const capacity = node.status?.capacity ?? {}
    const allocatable = node.status?.allocatable ?? {}
    for (const key of STANDARD_RESOURCE_KEYS) {
      standard[key].capacity += parseQuantity(capacity[key] ?? "0")
      standard[key].allocatable += parseQuantity(allocatable[key] ?? "0")
    }
    for (const key of extendedKeys) {
      extended[key].capacity += parseQuantity(capacity[key] ?? "0")
      extended[key].allocatable += parseQuantity(allocatable[key] ?? "0")
    }
  }

  for (const pod of pods) {
    if (!podCountsTowardRequested(pod, knownNodes)) continue
    for (const container of pod.spec?.containers ?? []) {
      const requests = container.resources?.requests
      if (!requests) continue
      for (const [key, value] of Object.entries(requests)) {
        if (STANDARD_RESOURCE_KEY_SET.has(key)) {
          standard[key as StandardResourceKey].requested += parseQuantity(value)
        } else if (extended[key]) {
          extended[key].requested += parseQuantity(value)
        }
      }
    }
  }

  if (metrics) {
    let cpuUsed = 0
    let memoryUsed = 0
    for (const m of metrics) {
      if (!knownNodes.has(m.metadata.name)) continue
      cpuUsed += parseQuantity(m.usage?.cpu ?? "0")
      memoryUsed += parseQuantity(m.usage?.memory ?? "0")
    }
    standard.cpu.used = cpuUsed
    standard.memory.used = memoryUsed
  }

  return { standard, extended }
}
