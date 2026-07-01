import { parseQuantity } from "../k8s-quantity.ts"
import { formatAge } from "../status.ts"
import {
  STANDARD_RESOURCE_KEYS,
  STANDARD_RESOURCE_KEY_SET,
  isExtendedResourceKey,
} from "./types.ts"
import type {
  Node,
  NodeMetrics,
  NodeRow,
  Pod,
  ResourceTotals,
  StandardResourceKey,
} from "./types.ts"

const PRESSURE_TYPES = new Set([
  "MemoryPressure",
  "DiskPressure",
  "PIDPressure",
  "NetworkUnavailable",
])

function rolesFromLabels(labels: Record<string, string> | undefined): string[] {
  if (!labels) return []
  const roles = new Set<string>()
  const PREFIX = "node-role.kubernetes.io/"
  for (const key of Object.keys(labels)) {
    if (key.startsWith(PREFIX)) {
      const role = key.slice(PREFIX.length)
      // Some clusters write `node-role.kubernetes.io/=...` with an empty
      // role part; skip those to avoid an empty pill in the UI.
      if (role.length > 0) roles.add(role)
    }
  }
  if (roles.size === 0) {
    const legacy = labels["kubernetes.io/role"]
    if (legacy) roles.add(legacy)
  }
  return [...roles]
}

function emptyTotals(): ResourceTotals {
  return { capacity: 0, allocatable: 0, requested: 0 }
}

/**
 * Builds one NodeRow per cluster node, sorted by name. Each row carries
 * the totals for that node only — capacity and allocatable from
 * node.status, requested summed from pods bound to that node, and used
 * from the matching NodeMetrics entry when metrics are supplied.
 *
 * Pods without a spec.nodeName or scheduled on an unknown node are
 * skipped so per-node requested totals stay consistent with the
 * cluster-wide aggregate computed by aggregateNodeResources.
 */
export function derivePerNodeRows(
  nodes: Node[],
  pods: Pod[],
  metrics: NodeMetrics[] | undefined,
): NodeRow[] {
  const metricsByName = new Map<string, NodeMetrics>()
  for (const m of metrics ?? []) {
    metricsByName.set(m.metadata.name, m)
  }

  const podsByNode = new Map<string, Pod[]>()
  for (const pod of pods) {
    const nodeName = pod.spec?.nodeName
    if (!nodeName) continue
    const bucket = podsByNode.get(nodeName) ?? []
    bucket.push(pod)
    podsByNode.set(nodeName, bucket)
  }

  const rows: NodeRow[] = []
  for (const node of nodes) {
    const capacity = node.status?.capacity ?? {}
    const allocatable = node.status?.allocatable ?? {}
    const standard: Record<StandardResourceKey, ResourceTotals> = {
      cpu: emptyTotals(),
      memory: emptyTotals(),
      "ephemeral-storage": emptyTotals(),
      pods: emptyTotals(),
    }
    const extended: Record<string, ResourceTotals> = {}

    for (const key of STANDARD_RESOURCE_KEYS) {
      standard[key].capacity = parseQuantity(capacity[key] ?? "0")
      standard[key].allocatable = parseQuantity(allocatable[key] ?? "0")
    }
    for (const key of Object.keys(capacity)) {
      if (!isExtendedResourceKey(key)) continue
      extended[key] = {
        capacity: parseQuantity(capacity[key] ?? "0"),
        allocatable: parseQuantity(allocatable[key] ?? "0"),
        requested: 0,
      }
    }

    for (const pod of podsByNode.get(node.metadata.name) ?? []) {
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

    const metric = metricsByName.get(node.metadata.name)
    if (metric) {
      standard.cpu.used = parseQuantity(metric.usage?.cpu ?? "0")
      standard.memory.used = parseQuantity(metric.usage?.memory ?? "0")
    }

    const conditions = node.status?.conditions ?? []
    const readyCondition = conditions.find((c) => c.type === "Ready")
    const pressureConditions = conditions
      .filter((c) => PRESSURE_TYPES.has(c.type) && c.status === "True")
      .map((c) => c.type)

    rows.push({
      name: node.metadata.name,
      ready: readyCondition?.status === "True",
      schedulable: !node.spec?.unschedulable,
      pressureConditions,
      roles: rolesFromLabels(node.metadata.labels),
      taints: (node.spec?.taints ?? []).map((t) => t.key),
      age: formatAge(node.metadata.creationTimestamp),
      creationTimestamp: node.metadata.creationTimestamp,
      standard,
      extended,
    })
  }

  rows.sort((a, b) => a.name.localeCompare(b.name))
  return rows
}
