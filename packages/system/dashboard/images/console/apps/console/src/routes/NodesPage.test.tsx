import { describe, it, expect, vi } from "vitest"
import { screen, waitFor } from "@testing-library/react"
import {
  K8sClient,
  K8sApiError,
  type K8sList,
  type APIGroupList,
} from "@cozystack/k8s-client"
import { NodesPage } from "./NodesPage.tsx"
import { renderWithK8sProvider } from "../test-utils/render.tsx"
import { nodesListFixture } from "../test-utils/fixtures/nodes.ts"
import { podsListFixture } from "../test-utils/fixtures/pods.ts"

function makeClient(
  config: { nodes?: K8sList<unknown> | K8sApiError; pods?: K8sList<unknown> } = {},
): K8sClient {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockImplementation(async (g, _v, plural) => {
    if (g === "metrics.k8s.io") {
      return {
        apiVersion: "metrics.k8s.io/v1beta1",
        kind: "NodeMetricsList",
        metadata: {},
        items: [],
      } as K8sList<unknown>
    }
    if (plural === "nodes") {
      if (config.nodes instanceof K8sApiError) throw config.nodes
      return (config.nodes ?? nodesListFixture) as K8sList<unknown>
    }
    if (plural === "pods") {
      return (config.pods ?? podsListFixture) as K8sList<unknown>
    }
    return { apiVersion: "v1", kind: `${plural}List`, metadata: {}, items: [] }
  })
  vi.spyOn(client, "getApiGroups").mockResolvedValue({
    kind: "APIGroupList",
    apiVersion: "v1",
    groups: [],
  } as APIGroupList)
  return client
}

describe("NodesPage", () => {
  it("renders the transposed node table with nodes as columns", async () => {
    const { container } = renderWithK8sProvider(<NodesPage />, { client: makeClient() })
    expect(await screen.findByText("Nodes")).toBeInTheDocument()
    // Node names appear as column headers, attributes as rows.
    expect(await screen.findByText("worker-gpu-1")).toBeInTheDocument()
    await waitFor(() =>
      expect(container.querySelector('[data-attribute-row="cpu"]')).not.toBeNull(),
    )
    expect(container.querySelector('[data-attribute-row="memory"]')).not.toBeNull()
  })

  it("renders a permission-denied block with a back link on 403", async () => {
    renderWithK8sProvider(<NodesPage />, {
      client: makeClient({ nodes: new K8sApiError(403, "forbidden") }),
    })
    expect(
      await screen.findByText(/you do not have permission to view cluster nodes/i),
    ).toBeInTheDocument()
    expect(screen.getByRole("link", { name: /back to console/i }).getAttribute("href")).toBe(
      "/console",
    )
  })

  it("renders the empty state when no nodes exist", async () => {
    renderWithK8sProvider(<NodesPage />, {
      client: makeClient({
        nodes: { apiVersion: "v1", kind: "NodeList", metadata: {}, items: [] } as K8sList<unknown>,
        pods: { apiVersion: "v1", kind: "PodList", metadata: {}, items: [] } as K8sList<unknown>,
      }),
    })
    expect(await screen.findByText(/no nodes found/i)).toBeInTheDocument()
  })
})
