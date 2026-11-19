import { describe, it, expect, vi } from "vitest"
import { screen, waitFor } from "@testing-library/react"
import {
  K8sClient,
  K8sApiError,
  type APIGroupList,
  type K8sList,
} from "@cozystack/k8s-client"
import { ClusterUsagePage } from "./ClusterUsagePage.tsx"
import { renderWithK8sProvider } from "../test-utils/render.tsx"
import { nodesListFixture } from "../test-utils/fixtures/nodes.ts"
import { podsListFixture } from "../test-utils/fixtures/pods.ts"
import { nodeMetricsListFixture } from "../test-utils/fixtures/node-metrics.ts"

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
  groups: [],
}

function makeClient(
  config: {
    nodes?: K8sList<unknown> | K8sApiError | "pending"
    pods?: K8sList<unknown> | K8sApiError
    metrics?: K8sList<unknown> | K8sApiError
    groups?: APIGroupList
  } = {},
): K8sClient {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockImplementation(async (g, _v, plural) => {
    if (g === "metrics.k8s.io") {
      if (config.metrics instanceof K8sApiError) throw config.metrics
      return (config.metrics ?? {
        apiVersion: "metrics.k8s.io/v1beta1",
        kind: "NodeMetricsList",
        metadata: {},
        items: [],
      }) as K8sList<unknown>
    }
    if (plural === "nodes") {
      if (config.nodes === "pending") return new Promise(() => ({})) as never
      if (config.nodes instanceof K8sApiError) throw config.nodes
      return (config.nodes ?? {
        apiVersion: "v1",
        kind: "NodeList",
        metadata: {},
        items: [],
      }) as K8sList<unknown>
    }
    if (plural === "pods") {
      if (config.pods instanceof K8sApiError) throw config.pods
      return (config.pods ?? {
        apiVersion: "v1",
        kind: "PodList",
        metadata: {},
        items: [],
      }) as K8sList<unknown>
    }
    return { apiVersion: "v1", kind: `${plural}List`, metadata: {}, items: [] }
  })
  vi.spyOn(client, "getApiGroups").mockResolvedValue(
    config.groups ?? groupsWithoutMetrics,
  )
  return client
}

describe("ClusterUsagePage", () => {
  it("renders a spinner while nodes are loading", () => {
    const client = makeClient({ nodes: "pending" })
    renderWithK8sProvider(<ClusterUsagePage />, { client })
    expect(screen.getByText(/loading/i)).toBeInTheDocument()
  })

  it("renders the aggregate resources table on a healthy cluster with metrics", async () => {
    const client = makeClient({
      nodes: nodesListFixture,
      pods: podsListFixture,
      metrics: nodeMetricsListFixture,
      groups: groupsWithMetrics,
    })
    const { container } = renderWithK8sProvider(<ClusterUsagePage />, { client })
    expect(await screen.findByText("Cluster")).toBeInTheDocument()
    expect(await screen.findAllByText(/allocatable/i)).not.toHaveLength(0)
    // The per-node table moved to its own Nodes page; this page now shows
    // only the cluster-wide resources table.
    await waitFor(() =>
      expect(container.querySelector('[data-resource-row="CPU"]')).not.toBeNull(),
    )
    expect(screen.queryByText("worker-gpu-1")).toBeNull()
  })

  it("renders the empty state when no nodes exist", async () => {
    const client = makeClient({
      nodes: {
        apiVersion: "v1",
        kind: "NodeList",
        metadata: {},
        items: [],
      } as K8sList<unknown>,
      pods: {
        apiVersion: "v1",
        kind: "PodList",
        metadata: {},
        items: [],
      } as K8sList<unknown>,
    })
    renderWithK8sProvider(<ClusterUsagePage />, { client })
    expect(await screen.findByText(/no nodes found/i)).toBeInTheDocument()
  })

  it("renders an error block when the nodes-list call fails", async () => {
    const client = makeClient({ nodes: new K8sApiError(500, "server error") })
    renderWithK8sProvider(<ClusterUsagePage />, { client })
    await waitFor(() => {
      expect(screen.getByText(/failed to load cluster nodes/i)).toBeInTheDocument()
    })
  })

  it("renders a permission-denied block with a back link on 403", async () => {
    const client = makeClient({ nodes: new K8sApiError(403, "forbidden") })
    renderWithK8sProvider(<ClusterUsagePage />, { client })
    expect(
      await screen.findByText(/you do not have permission to view cluster nodes/i),
    ).toBeInTheDocument()
    const back = screen.getByRole("link", { name: /back to console/i })
    expect(back.getAttribute("href")).toBe("/console")
  })

  it("propagates pods-unavailable to the aggregate panel and the table", async () => {
    const client = makeClient({
      nodes: nodesListFixture,
      pods: new K8sApiError(403, "no pod read"),
      groups: groupsWithoutMetrics,
    })
    renderWithK8sProvider(<ClusterUsagePage />, { client })
    await screen.findAllByText(/allocatable/i)
    expect(
      screen.getAllByTitle("Requires cluster-wide pod read access").length,
    ).toBeGreaterThan(0)
  })

  it("renders the node-summary line in the aggregates header", async () => {
    const client = makeClient({
      nodes: nodesListFixture,
      pods: podsListFixture,
      groups: groupsWithoutMetrics,
    })
    renderWithK8sProvider(<ClusterUsagePage />, { client })
    await screen.findByText("3 nodes")
    expect(
      screen.getByText(/3 Ready · 0 NotReady · 0 SchedulingDisabled/),
    ).toBeInTheDocument()
  })

  it("shows only em-dashes in the aggregate Used column when metrics-server is not registered", async () => {
    const client = makeClient({
      nodes: nodesListFixture,
      pods: podsListFixture,
      groups: groupsWithoutMetrics,
    })
    const { container } = renderWithK8sProvider(<ClusterUsagePage />, { client })
    // Wait for the page to settle by waiting on an aggregate label.
    await screen.findAllByText(/allocatable/i)
    // The aggregate resources table always renders a Used column; without
    // metrics every Used cell (last column of each resource row) is "—".
    const rows = container.querySelectorAll("[data-resource-row]")
    expect(rows.length).toBeGreaterThan(0)
    for (const row of rows) {
      const cells = row.querySelectorAll("td")
      expect(cells[cells.length - 1].textContent).toBe("—")
    }
  })
})
