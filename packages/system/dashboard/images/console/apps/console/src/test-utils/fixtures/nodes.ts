import type { K8sList, K8sResource } from "@cozystack/k8s-client"

/**
 * Three node fixtures cover the three shapes the cluster-usage page must
 * render: control-plane (no extended resources), plain worker, worker
 * with a single NVIDIA GPU. Numbers are small but realistic and chosen
 * so hand-computed aggregates over the set stay obvious.
 *
 * creationTimestamp is fixed; tests that assert on Age must stub the
 * clock via vi.setSystemTime.
 */

interface NodeStatus {
  capacity?: Record<string, string>
  allocatable?: Record<string, string>
  conditions?: Array<{
    type: string
    status: "True" | "False" | "Unknown"
    reason?: string
    message?: string
  }>
}

export type NodeFixture = K8sResource<unknown, NodeStatus>

export const nodeControlPlane: NodeFixture = {
  apiVersion: "v1",
  kind: "Node",
  metadata: {
    name: "cp-1",
    creationTimestamp: "2026-01-01T00:00:00Z",
    labels: { "node-role.kubernetes.io/control-plane": "" },
  },
  status: {
    capacity: { cpu: "4", memory: "8Gi", "ephemeral-storage": "100Gi", pods: "110" },
    allocatable: { cpu: "4", memory: "8Gi", "ephemeral-storage": "100Gi", pods: "110" },
    conditions: [{ type: "Ready", status: "True" }],
  },
}

export const nodeWorker: NodeFixture = {
  apiVersion: "v1",
  kind: "Node",
  metadata: {
    name: "worker-1",
    creationTimestamp: "2026-02-01T00:00:00Z",
  },
  status: {
    capacity: { cpu: "8", memory: "32Gi", "ephemeral-storage": "500Gi", pods: "220" },
    allocatable: { cpu: "8", memory: "32Gi", "ephemeral-storage": "500Gi", pods: "220" },
    conditions: [{ type: "Ready", status: "True" }],
  },
}

export const nodeGpuWorker: NodeFixture = {
  apiVersion: "v1",
  kind: "Node",
  metadata: {
    name: "worker-gpu-1",
    creationTimestamp: "2026-03-01T00:00:00Z",
  },
  status: {
    capacity: {
      cpu: "16",
      memory: "64Gi",
      "ephemeral-storage": "1Ti",
      pods: "220",
      "nvidia.com/gpu": "1",
    },
    allocatable: {
      cpu: "16",
      memory: "64Gi",
      "ephemeral-storage": "1Ti",
      pods: "220",
      "nvidia.com/gpu": "1",
    },
    conditions: [{ type: "Ready", status: "True" }],
  },
}

export const nodesListFixture: K8sList<NodeFixture> = {
  apiVersion: "v1",
  kind: "NodeList",
  metadata: { resourceVersion: "100" },
  items: [nodeControlPlane, nodeWorker, nodeGpuWorker],
}
