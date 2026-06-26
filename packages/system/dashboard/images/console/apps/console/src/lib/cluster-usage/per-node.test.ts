import { describe, it, expect, vi, beforeAll, afterAll } from "vitest"
import { derivePerNodeRows } from "./per-node.ts"
import type { Node, Pod, NodeMetrics } from "./types.ts"

beforeAll(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date("2026-05-26T00:00:00Z"))
})

afterAll(() => {
  vi.useRealTimers()
})

function nodeWith(
  name: string,
  fields: {
    capacity?: Record<string, string>
    allocatable?: Record<string, string>
    labels?: Record<string, string>
    ready?: boolean
    pressure?: string[]
    unschedulable?: boolean
    taints?: Array<{ key: string; value?: string; effect: string }>
    creationTimestamp?: string
  } = {},
): Node {
  const capacity = fields.capacity ?? { cpu: "4", memory: "8Gi" }
  const allocatable = fields.allocatable ?? capacity
  const conditions: Node["status"] = { capacity, allocatable, conditions: [] }
  conditions.conditions?.push({
    type: "Ready",
    status: fields.ready === false ? "False" : "True",
  })
  for (const p of fields.pressure ?? []) {
    conditions.conditions?.push({ type: p, status: "True" })
  }
  return {
    apiVersion: "v1",
    kind: "Node",
    metadata: {
      name,
      labels: fields.labels,
      creationTimestamp: fields.creationTimestamp ?? "2026-05-25T00:00:00Z",
    },
    spec: { unschedulable: fields.unschedulable, taints: fields.taints },
    status: conditions,
  } as Node
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

describe("derivePerNodeRows", () => {
  it("returns one row per node, sorted by name", () => {
    const rows = derivePerNodeRows(
      [nodeWith("b"), nodeWith("a"), nodeWith("c")],
      [],
      undefined,
    )
    expect(rows.map((r) => r.name)).toEqual(["a", "b", "c"])
  })

  it("computes age relative to the stubbed clock", () => {
    const rows = derivePerNodeRows(
      [nodeWith("a", { creationTimestamp: "2026-05-25T03:00:00Z" })],
      [],
      undefined,
    )
    expect(rows[0].age).toBe("21h")
  })

  it("detects ready vs notready conditions", () => {
    const rows = derivePerNodeRows(
      [nodeWith("a", { ready: true }), nodeWith("b", { ready: false })],
      [],
      undefined,
    )
    expect(rows[0].ready).toBe(true)
    expect(rows[1].ready).toBe(false)
  })

  it("collects pressure conditions with status=True", () => {
    const rows = derivePerNodeRows(
      [nodeWith("a", { pressure: ["MemoryPressure", "DiskPressure"] })],
      [],
      undefined,
    )
    expect(rows[0].pressureConditions).toEqual(["MemoryPressure", "DiskPressure"])
  })

  it("derives roles from node-role.kubernetes.io labels", () => {
    const rows = derivePerNodeRows(
      [
        nodeWith("a", {
          labels: {
            "node-role.kubernetes.io/control-plane": "",
            "node-role.kubernetes.io/worker": "",
          },
        }),
      ],
      [],
      undefined,
    )
    expect(rows[0].roles.sort()).toEqual(["control-plane", "worker"])
  })

  it("falls back to kubernetes.io/role label when present", () => {
    const rows = derivePerNodeRows(
      [nodeWith("a", { labels: { "kubernetes.io/role": "ingress" } })],
      [],
      undefined,
    )
    expect(rows[0].roles).toEqual(["ingress"])
  })

  it("returns no roles for a label-less node", () => {
    const rows = derivePerNodeRows([nodeWith("a", { labels: {} })], [], undefined)
    expect(rows[0].roles).toEqual([])
  })

  it("filters out an empty role suffix (`node-role.kubernetes.io/=`)", () => {
    const rows = derivePerNodeRows(
      [nodeWith("a", { labels: { "node-role.kubernetes.io/": "" } })],
      [],
      undefined,
    )
    expect(rows[0].roles).toEqual([])
  })

  it("reports schedulable=false when spec.unschedulable=true", () => {
    const rows = derivePerNodeRows(
      [nodeWith("a", { unschedulable: true })],
      [],
      undefined,
    )
    expect(rows[0].schedulable).toBe(false)
  })

  it("collects taint keys when present", () => {
    const rows = derivePerNodeRows(
      [
        nodeWith("a", {
          taints: [{ key: "node.kubernetes.io/unschedulable", effect: "NoSchedule" }],
        }),
      ],
      [],
      undefined,
    )
    expect(rows[0].taints).toEqual(["node.kubernetes.io/unschedulable"])
  })

  it("scopes requested totals to pods scheduled on that node", () => {
    const rows = derivePerNodeRows(
      [
        nodeWith("a", { capacity: { cpu: "8", memory: "16Gi" } }),
        nodeWith("b", { capacity: { cpu: "8", memory: "16Gi" } }),
      ],
      [
        pod("p1", "a", { cpu: "500m", memory: "1Gi" }),
        pod("p2", "b", { cpu: "1", memory: "4Gi" }),
      ],
      undefined,
    )
    expect(rows[0].standard.cpu.requested).toBe(0.5)
    expect(rows[1].standard.cpu.requested).toBe(1)
  })

  it("populates used per node when metrics are supplied", () => {
    const rows = derivePerNodeRows(
      [nodeWith("a", { capacity: { cpu: "8", memory: "16Gi" } })],
      [],
      [metric("a", "1500m", "4Gi")],
    )
    expect(rows[0].standard.cpu.used).toBe(1.5)
    expect(rows[0].standard.memory.used).toBe(4 * 1024 ** 3)
  })

  it("reports cpu used in cores from nanocore metrics, not raw nanocores", () => {
    const rows = derivePerNodeRows(
      [nodeWith("a", { capacity: { cpu: "288", memory: "256Gi" } })],
      [],
      [metric("a", "2785315627n", "14417112Ki")],
    )
    expect(rows[0].standard.cpu.used).toBeCloseTo(2.785, 3)
    expect(rows[0].standard.memory.used).toBe(14417112 * 1024)
  })

  it("leaves used undefined per node when metrics are undefined", () => {
    const rows = derivePerNodeRows(
      [nodeWith("a", { capacity: { cpu: "8", memory: "16Gi" } })],
      [],
      undefined,
    )
    expect(rows[0].standard.cpu.used).toBeUndefined()
  })

  it("includes extended-resource columns per node", () => {
    const rows = derivePerNodeRows(
      [
        nodeWith("a", { capacity: { cpu: "8", "nvidia.com/gpu": "1" } }),
        nodeWith("b", { capacity: { cpu: "8" } }),
      ],
      [pod("p", "a", { "nvidia.com/gpu": "1" })],
      undefined,
    )
    expect(rows[0].extended["nvidia.com/gpu"].capacity).toBe(1)
    expect(rows[0].extended["nvidia.com/gpu"].requested).toBe(1)
    expect(rows[1].extended["nvidia.com/gpu"]).toBeUndefined()
  })
})
