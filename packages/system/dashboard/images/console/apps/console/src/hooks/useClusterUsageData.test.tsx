import { describe, it, expect, vi } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  K8sClient,
  K8sProvider,
  K8sApiError,
  type APIGroupList,
  type K8sList,
} from "@cozystack/k8s-client"
import type { ReactNode } from "react"
import { useClusterUsageData } from "./useClusterUsageData.tsx"
import { nodesListFixture } from "../test-utils/fixtures/nodes.ts"
import { podsListFixture } from "../test-utils/fixtures/pods.ts"
import { nodeMetricsListFixture } from "../test-utils/fixtures/node-metrics.ts"

function makeWrapper(client: K8sClient) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>
        <K8sProvider client={client} queryClient={queryClient}>
          {children}
        </K8sProvider>
      </QueryClientProvider>
    )
  }
}

const groupsWithMetrics: APIGroupList = {
  kind: "APIGroupList",
  apiVersion: "v1",
  groups: [
    {
      name: "metrics.k8s.io",
      versions: [{ groupVersion: "metrics.k8s.io/v1beta1", version: "v1beta1" }],
      preferredVersion: { groupVersion: "metrics.k8s.io/v1beta1", version: "v1beta1" },
    },
  ],
}

const groupsWithoutMetrics: APIGroupList = {
  kind: "APIGroupList",
  apiVersion: "v1",
  groups: [
    {
      name: "apps",
      versions: [{ groupVersion: "apps/v1", version: "v1" }],
      preferredVersion: { groupVersion: "apps/v1", version: "v1" },
    },
  ],
}

function stubList(
  client: K8sClient,
  responses: Partial<Record<string, K8sList<unknown> | K8sApiError>>,
) {
  vi.spyOn(client, "list").mockImplementation(async (apiGroup, _v, plural) => {
    // Key by (apiGroup|plural). The metrics.k8s.io node listing uses
    // plural=nodes too, so we can't disambiguate on plural alone.
    const key = `${apiGroup}|${plural}`
    const r = responses[key]
    if (r instanceof K8sApiError) throw r
    return (r ?? { apiVersion: "v1", kind: `${plural}List`, metadata: {}, items: [] }) as K8sList<
      unknown
    >
  })
}

describe("useClusterUsageData", () => {
  it("reports isLoading=true on first render", () => {
    const client = new K8sClient()
    stubList(client, {})
    vi.spyOn(client, "getApiGroups").mockImplementation(() => new Promise(() => {}))
    const { result } = renderHook(() => useClusterUsageData(), {
      wrapper: makeWrapper(client),
    })
    expect(result.current.isLoading).toBe(true)
  })

  it("returns aggregates and per-node rows derived from nodes + pods + metrics", async () => {
    const client = new K8sClient()
    stubList(client, {
      "|nodes": nodesListFixture,
      "|pods": podsListFixture,
      "metrics.k8s.io|nodes": nodeMetricsListFixture,
    })
    vi.spyOn(client, "getApiGroups").mockResolvedValue(groupsWithMetrics)
    const { result } = renderHook(() => useClusterUsageData(), {
      wrapper: makeWrapper(client),
    })
    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(result.current.metricsAvailable).toBe(true)
    expect(result.current.perNode.map((r) => r.name)).toEqual([
      "cp-1",
      "worker-1",
      "worker-gpu-1",
    ])
    expect(result.current.aggregates.extended["nvidia.com/gpu"].capacity).toBe(1)
    // Used overlay must be populated from the metrics fixture.
    expect(result.current.aggregates.standard.cpu.used).toBeGreaterThan(0)
  })

  it("never lists NodeMetrics when metrics.k8s.io is not registered", async () => {
    const client = new K8sClient()
    const listSpy = vi.spyOn(client, "list").mockImplementation(
      async (_g, _v, plural) => {
        if (plural === "nodes")
          return nodesListFixture as unknown as K8sList<unknown>
        if (plural === "pods")
          return podsListFixture as unknown as K8sList<unknown>
        return { apiVersion: "v1", kind: `${plural}List`, metadata: {}, items: [] }
      },
    )
    vi.spyOn(client, "getApiGroups").mockResolvedValue(groupsWithoutMetrics)
    const { result } = renderHook(() => useClusterUsageData(), {
      wrapper: makeWrapper(client),
    })
    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(result.current.metricsAvailable).toBe(false)
    const metricsCalls = listSpy.mock.calls.filter(
      (call) => call[0] === "metrics.k8s.io",
    )
    expect(metricsCalls).toHaveLength(0)
    expect(result.current.aggregates.standard.cpu.used).toBeUndefined()
  })

  it("treats a metrics-API 403 as 'no usage data' without crashing", async () => {
    const client = new K8sClient()
    vi.spyOn(client, "list").mockImplementation(async (g, _v, plural) => {
      if (g === "metrics.k8s.io") throw new K8sApiError(403, "forbidden")
      if (plural === "nodes") return nodesListFixture as unknown as K8sList<unknown>
      if (plural === "pods") return podsListFixture as unknown as K8sList<unknown>
      return { apiVersion: "v1", kind: `${plural}List`, metadata: {}, items: [] }
    })
    vi.spyOn(client, "getApiGroups").mockResolvedValue(groupsWithMetrics)
    const { result } = renderHook(() => useClusterUsageData(), {
      wrapper: makeWrapper(client),
    })
    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(result.current.metricsAvailable).toBe(true)
    expect(result.current.aggregates.standard.cpu.used).toBeUndefined()
    expect(result.current.error).toBeNull()
  })

  it("surfaces a nodes-list error as the hook error", async () => {
    const client = new K8sClient()
    vi.spyOn(client, "list").mockImplementation(async (_g, _v, plural) => {
      if (plural === "nodes") throw new K8sApiError(500, "boom")
      return { apiVersion: "v1", kind: `${plural}List`, metadata: {}, items: [] }
    })
    vi.spyOn(client, "getApiGroups").mockResolvedValue(groupsWithoutMetrics)
    const { result } = renderHook(() => useClusterUsageData(), {
      wrapper: makeWrapper(client),
    })
    await waitFor(() => expect(result.current.error).toBeTruthy())
    expect(result.current.error?.message).toContain("boom")
  })
})
