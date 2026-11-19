import { describe, it, expect } from "vitest"
import { aggregateNodeResources } from "./aggregate.ts"
import type { Node, Pod, NodeMetrics } from "./types.ts"

function node(name: string, capacity: Record<string, string>): Node {
  return {
    apiVersion: "v1",
    kind: "Node",
    metadata: { name },
    status: { capacity, allocatable: capacity, conditions: [] },
  }
}

function pod(
  name: string,
  nodeName: string | undefined,
  requests: Record<string, string>,
): Pod {
  return {
    apiVersion: "v1",
    kind: "Pod",
    metadata: { name, namespace: "ns" },
    spec: { nodeName, containers: [{ name: "c", resources: { requests } }] },
  }
}

function metric(name: string, cpu: string, memory: string): NodeMetrics {
  return {
    apiVersion: "metrics.k8s.io/v1beta1",
    kind: "NodeMetrics",
    metadata: { name },
    usage: { cpu, memory },
  }
}

describe("aggregateNodeResources", () => {
  it("returns zeroed standard totals for an empty cluster", () => {
    const a = aggregateNodeResources([], [], undefined)
    expect(a.standard.cpu).toEqual({ capacity: 0, allocatable: 0, requested: 0 })
    expect(a.standard.memory).toEqual({ capacity: 0, allocatable: 0, requested: 0 })
    expect(a.standard["ephemeral-storage"]).toEqual({
      capacity: 0,
      allocatable: 0,
      requested: 0,
    })
    expect(a.standard.pods).toEqual({ capacity: 0, allocatable: 0, requested: 0 })
    expect(a.extended).toEqual({})
  })

  it("sums capacity and allocatable across nodes", () => {
    const a = aggregateNodeResources(
      [
        node("a", { cpu: "4", memory: "8Gi", "ephemeral-storage": "100Gi", pods: "110" }),
        node("b", { cpu: "8", memory: "32Gi", "ephemeral-storage": "500Gi", pods: "220" }),
      ],
      [],
      undefined,
    )
    expect(a.standard.cpu.capacity).toBe(12)
    expect(a.standard.cpu.allocatable).toBe(12)
    expect(a.standard.memory.capacity).toBe((8 + 32) * 1024 ** 3)
    expect(a.standard.pods.capacity).toBe(330)
  })

  it("groups extended resource keys verbatim", () => {
    const a = aggregateNodeResources(
      [
        node("a", { cpu: "4", "nvidia.com/gpu": "2" }),
        node("b", { cpu: "8", "amd.com/gpu": "1" }),
      ],
      [],
      undefined,
    )
    expect(a.extended["nvidia.com/gpu"].capacity).toBe(2)
    expect(a.extended["amd.com/gpu"].capacity).toBe(1)
  })

  it("sums pod requests scoped to scheduled pods only", () => {
    const a = aggregateNodeResources(
      [node("a", { cpu: "8", memory: "16Gi" })],
      [
        pod("p1", "a", { cpu: "500m", memory: "1Gi" }),
        pod("p2", "a", { cpu: "1", memory: "2Gi" }),
        pod("unscheduled", undefined, { cpu: "100m", memory: "256Mi" }),
      ],
      undefined,
    )
    expect(a.standard.cpu.requested).toBe(1.5)
    expect(a.standard.memory.requested).toBe(3 * 1024 ** 3)
  })

  it("skips pods scheduled on unknown nodes", () => {
    const a = aggregateNodeResources(
      [node("a", { cpu: "8" })],
      [pod("rogue", "ghost-node", { cpu: "500m" })],
      undefined,
    )
    expect(a.standard.cpu.requested).toBe(0)
  })

  it("excludes terminal (Succeeded/Failed) pods from requested totals", () => {
    const terminal = (name: string, phase: string): Pod => ({
      ...pod(name, "a", { cpu: "1", memory: "2Gi" }),
      status: { phase },
    })
    const a = aggregateNodeResources(
      [node("a", { cpu: "8", memory: "16Gi" })],
      [
        pod("running", "a", { cpu: "500m", memory: "1Gi" }),
        terminal("completed", "Succeeded"),
        terminal("crashed", "Failed"),
      ],
      undefined,
    )
    expect(a.standard.cpu.requested).toBe(0.5)
    expect(a.standard.memory.requested).toBe(1024 ** 3)
  })

  it("sums extended-resource requests under the extended bucket", () => {
    const a = aggregateNodeResources(
      [node("a", { cpu: "8", "nvidia.com/gpu": "2" })],
      [pod("p", "a", { cpu: "200m", "nvidia.com/gpu": "1" })],
      undefined,
    )
    expect(a.extended["nvidia.com/gpu"].requested).toBe(1)
  })

  it("populates used for cpu and memory when metrics are supplied", () => {
    const a = aggregateNodeResources(
      [node("a", { cpu: "8", memory: "16Gi" })],
      [],
      [metric("a", "1500m", "4Gi")],
    )
    expect(a.standard.cpu.used).toBe(1.5)
    expect(a.standard.memory.used).toBe(4 * 1024 ** 3)
  })

  it("reports cpu used in cores from nanocore metrics, not raw nanocores", () => {
    const a = aggregateNodeResources(
      [node("a", { cpu: "288", memory: "256Gi" })],
      [],
      [metric("a", "2785315627n", "14417112Ki")],
    )
    expect(a.standard.cpu.used).toBeCloseTo(2.785, 3)
    expect(a.standard.memory.used).toBe(14417112 * 1024)
  })

  it("leaves used undefined when metrics is undefined", () => {
    const a = aggregateNodeResources(
      [node("a", { cpu: "8", memory: "16Gi" })],
      [],
      undefined,
    )
    expect(a.standard.cpu.used).toBeUndefined()
    expect(a.standard.memory.used).toBeUndefined()
  })

  it("never reports used for ephemeral-storage or pods", () => {
    const a = aggregateNodeResources(
      [node("a", { cpu: "8", memory: "16Gi", "ephemeral-storage": "100Gi", pods: "110" })],
      [],
      [metric("a", "1", "2Gi")],
    )
    expect(a.standard["ephemeral-storage"].used).toBeUndefined()
    expect(a.standard.pods.used).toBeUndefined()
  })
})
