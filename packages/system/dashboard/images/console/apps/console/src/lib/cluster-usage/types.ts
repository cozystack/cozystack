import type { K8sResource } from "@cozystack/k8s-client"

/**
 * Minimal Kubernetes Node shape needed by the cluster-usage page. Only
 * the fields the page actually reads are declared; the rest of the K8s
 * Node object is ignored. Status fields are optional to match the
 * realistic case where a NotReady node may not have populated all of
 * its capacity / allocatable map yet.
 */

export interface NodeCondition {
  type: string
  status: "True" | "False" | "Unknown"
  reason?: string
  message?: string
  lastTransitionTime?: string
}

export interface NodeStatus {
  capacity?: Record<string, string>
  allocatable?: Record<string, string>
  conditions?: NodeCondition[]
}

export interface NodeTaint {
  key: string
  value?: string
  effect: string
  timeAdded?: string
}

export interface NodeSpec {
  unschedulable?: boolean
  taints?: NodeTaint[]
  providerID?: string
}

export type Node = K8sResource<NodeSpec, NodeStatus>

export interface PodContainer {
  name: string
  resources?: {
    requests?: Record<string, string>
    limits?: Record<string, string>
  }
}

export interface PodSpec {
  nodeName?: string
  containers: PodContainer[]
}

export interface PodStatus {
  phase?: string
}

export type Pod = K8sResource<PodSpec, PodStatus>

export interface PvcSpec {
  storageClassName?: string
  resources?: { requests?: Record<string, string> }
}

export interface PvcStatus {
  phase?: string
  capacity?: Record<string, string>
}

export type Pvc = K8sResource<PvcSpec, PvcStatus>

export interface NodeMetricsUsage {
  cpu: string
  memory: string
}

export type NodeMetrics = K8sResource<unknown, unknown> & {
  usage?: NodeMetricsUsage
  timestamp?: string
  window?: string
}

/**
 * Standard, well-known resource keys present in node.status.capacity.
 * Everything else is treated as an extended resource and rendered
 * verbatim by the cluster-usage page.
 */
export const STANDARD_RESOURCE_KEYS = ["cpu", "memory", "ephemeral-storage", "pods"] as const

export type StandardResourceKey = (typeof STANDARD_RESOURCE_KEYS)[number]

export const STANDARD_RESOURCE_KEY_SET: ReadonlySet<string> = new Set(STANDARD_RESOURCE_KEYS)

/**
 * Whether a key from `node.status.capacity` should be treated as an
 * extended resource. Standard scheduler resources and every hugepages-*
 * variant return false; everything else returns true.
 */
export function isExtendedResourceKey(key: string): boolean {
  if (STANDARD_RESOURCE_KEY_SET.has(key)) return false
  if (key.startsWith("hugepages-")) return false
  return true
}

/** A resource snapshot in canonical units — cores for CPU, bytes elsewhere. */
export interface ResourceTotals {
  capacity: number
  allocatable: number
  requested: number
  /** Present only when metrics.k8s.io reported a usage figure for this resource. */
  used?: number
}

export interface AggregateResources {
  /** Standard resources keyed by their canonical name. */
  standard: Record<StandardResourceKey, ResourceTotals>
  /** Extended resources keyed by their full Kubernetes key (e.g. `nvidia.com/gpu`). */
  extended: Record<string, ResourceTotals>
}

export interface NodeRow {
  name: string
  ready: boolean
  schedulable: boolean
  /** Free-form condition types found with status=True, e.g. MemoryPressure. */
  pressureConditions: string[]
  /** Roles inferred from `node-role.kubernetes.io/*` and `kubernetes.io/role` labels. */
  roles: string[]
  taints: string[]
  age: string
  creationTimestamp?: string
  /** Standard resource totals on this single node. */
  standard: Record<StandardResourceKey, ResourceTotals>
  /** Extended resource totals on this single node, keyed by full key. */
  extended: Record<string, ResourceTotals>
}
