import type { K8sList, K8sResource } from "@cozystack/k8s-client"

interface PodSpec {
  nodeName?: string
  containers: Array<{
    name: string
    resources?: {
      requests?: Record<string, string>
      limits?: Record<string, string>
    }
  }>
}

interface PodStatus {
  phase?: string
}

export type PodFixture = K8sResource<PodSpec, PodStatus>

/**
 * Pod fixtures aligned with the node fixtures: one bound to each node,
 * one bound to the GPU worker requesting one nvidia.com/gpu, and one
 * unscheduled pod (no spec.nodeName) so the per-node aggregator can be
 * verified to skip it correctly. Requests are deliberately small so the
 * sums are obvious by inspection.
 */

export const podOnControlPlane: PodFixture = {
  apiVersion: "v1",
  kind: "Pod",
  metadata: { name: "system-pod", namespace: "kube-system" },
  spec: {
    nodeName: "cp-1",
    containers: [
      {
        name: "main",
        resources: { requests: { cpu: "200m", memory: "256Mi" } },
      },
    ],
  },
  status: { phase: "Running" },
}

export const podOnWorker: PodFixture = {
  apiVersion: "v1",
  kind: "Pod",
  metadata: { name: "tenant-pod", namespace: "tenant-root" },
  spec: {
    nodeName: "worker-1",
    containers: [
      {
        name: "app",
        resources: { requests: { cpu: "500m", memory: "1Gi" } },
      },
    ],
  },
  status: { phase: "Running" },
}

export const podOnGpuWorker: PodFixture = {
  apiVersion: "v1",
  kind: "Pod",
  metadata: { name: "gpu-pod", namespace: "tenant-ml" },
  spec: {
    nodeName: "worker-gpu-1",
    containers: [
      {
        name: "model",
        resources: {
          requests: { cpu: "100m", memory: "128Mi", "nvidia.com/gpu": "1" },
        },
      },
    ],
  },
  status: { phase: "Running" },
}

export const podUnscheduled: PodFixture = {
  apiVersion: "v1",
  kind: "Pod",
  metadata: { name: "pending-pod", namespace: "default" },
  spec: {
    containers: [
      {
        name: "main",
        resources: { requests: { cpu: "1", memory: "2Gi" } },
      },
    ],
  },
  status: { phase: "Pending" },
}

export const podsListFixture: K8sList<PodFixture> = {
  apiVersion: "v1",
  kind: "PodList",
  metadata: { resourceVersion: "200" },
  items: [podOnControlPlane, podOnWorker, podOnGpuWorker, podUnscheduled],
}
