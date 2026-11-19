import type { K8sList, K8sResource } from "@cozystack/k8s-client"

interface NodeMetricsUsage {
  cpu: string
  memory: string
}

interface NodeMetricsFields {
  usage?: NodeMetricsUsage
  timestamp?: string
  window?: string
}

export type NodeMetricsFixture = K8sResource<unknown, unknown> & NodeMetricsFields

/**
 * NodeMetrics entries matching the node fixtures. metrics.k8s.io reports
 * actual usage as Kubernetes quantities just like capacity, so the same
 * parseQuantity path renders both numbers.
 */

export const metricsControlPlane: NodeMetricsFixture = {
  apiVersion: "metrics.k8s.io/v1beta1",
  kind: "NodeMetrics",
  metadata: { name: "cp-1" },
  usage: { cpu: "150m", memory: "1500Mi" },
  timestamp: "2026-05-26T17:00:00Z",
  window: "30s",
}

export const metricsWorker: NodeMetricsFixture = {
  apiVersion: "metrics.k8s.io/v1beta1",
  kind: "NodeMetrics",
  metadata: { name: "worker-1" },
  usage: { cpu: "400m", memory: "8Gi" },
  timestamp: "2026-05-26T17:00:00Z",
  window: "30s",
}

export const metricsGpuWorker: NodeMetricsFixture = {
  apiVersion: "metrics.k8s.io/v1beta1",
  kind: "NodeMetrics",
  metadata: { name: "worker-gpu-1" },
  usage: { cpu: "2", memory: "20Gi" },
  timestamp: "2026-05-26T17:00:00Z",
  window: "30s",
}

export const nodeMetricsListFixture: K8sList<NodeMetricsFixture> = {
  apiVersion: "metrics.k8s.io/v1beta1",
  kind: "NodeMetricsList",
  metadata: { resourceVersion: "300" },
  items: [metricsControlPlane, metricsWorker, metricsGpuWorker],
}
