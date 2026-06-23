import { describe, it, expect } from "vitest"
import { getExtendedResourceKeys } from "./extended-resources.ts"
import type { Node } from "./types.ts"

function makeNode(name: string, capacity: Record<string, string>): Node {
  return {
    apiVersion: "v1",
    kind: "Node",
    metadata: { name },
    status: { capacity },
  }
}

describe("getExtendedResourceKeys", () => {
  it("returns an empty array for no nodes", () => {
    expect(getExtendedResourceKeys([])).toEqual([])
  })

  it("strips out standard resources cpu, memory, ephemeral-storage, pods", () => {
    const nodes = [
      makeNode("a", {
        cpu: "8",
        memory: "32Gi",
        "ephemeral-storage": "500Gi",
        pods: "110",
      }),
    ]
    expect(getExtendedResourceKeys(nodes)).toEqual([])
  })

  it("strips hugepages-* in any variant", () => {
    const nodes = [
      makeNode("a", {
        cpu: "8",
        "hugepages-2Mi": "0",
        "hugepages-1Gi": "0",
      }),
    ]
    expect(getExtendedResourceKeys(nodes)).toEqual([])
  })

  it("collects extended keys verbatim", () => {
    const nodes = [makeNode("a", { cpu: "8", "nvidia.com/gpu": "1" })]
    expect(getExtendedResourceKeys(nodes)).toEqual(["nvidia.com/gpu"])
  })

  it("dedupes keys appearing on multiple nodes", () => {
    const nodes = [
      makeNode("a", { "nvidia.com/gpu": "1" }),
      makeNode("b", { "nvidia.com/gpu": "2" }),
    ]
    expect(getExtendedResourceKeys(nodes)).toEqual(["nvidia.com/gpu"])
  })

  it("sorts keys alphabetically for stable rendering", () => {
    const nodes = [
      makeNode("a", { "nvidia.com/gpu": "1" }),
      makeNode("b", { "amd.com/gpu": "1", "hami.io/vgpu": "4" }),
    ]
    expect(getExtendedResourceKeys(nodes)).toEqual([
      "amd.com/gpu",
      "hami.io/vgpu",
      "nvidia.com/gpu",
    ])
  })

  it("ignores nodes without status.capacity", () => {
    const node: Node = {
      apiVersion: "v1",
      kind: "Node",
      metadata: { name: "drained" },
    }
    expect(getExtendedResourceKeys([node])).toEqual([])
  })
})

