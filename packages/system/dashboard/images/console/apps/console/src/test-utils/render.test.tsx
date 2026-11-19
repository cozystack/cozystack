import { describe, it, expect } from "vitest"
import { waitFor } from "@testing-library/react"
import { useK8sList } from "@cozystack/k8s-client"
import { createMockK8sClient } from "./mock-k8s-client.ts"
import { renderWithK8sProvider } from "./render.tsx"
import { nodesListFixture, type NodeFixture } from "./fixtures/nodes.ts"

function NodeNameList() {
  const { data, isLoading } = useK8sList<NodeFixture>(
    { apiGroup: "", apiVersion: "v1", plural: "nodes" },
    { watch: false },
  )
  if (isLoading) return <p>loading</p>
  return (
    <ul>
      {data?.items.map((n) => <li key={n.metadata.name}>{n.metadata.name}</li>)}
    </ul>
  )
}

describe("renderWithK8sProvider", () => {
  it("renders a component that consumes useK8sList against an injected mock", async () => {
    const client = createMockK8sClient({
      lists: [
        {
          apiGroup: "",
          apiVersion: "v1",
          plural: "nodes",
          result: nodesListFixture,
        },
      ],
    })

    const { findByText } = renderWithK8sProvider(<NodeNameList />, { client })

    expect(await findByText("cp-1")).toBeInTheDocument()
    expect(await findByText("worker-1")).toBeInTheDocument()
    expect(await findByText("worker-gpu-1")).toBeInTheDocument()
  })

  it("routes the list call through the mock with the requested resource", async () => {
    const client = createMockK8sClient({
      lists: [
        {
          apiGroup: "",
          apiVersion: "v1",
          plural: "nodes",
          result: nodesListFixture,
        },
      ],
    })

    renderWithK8sProvider(<NodeNameList />, { client })

    await waitFor(() => {
      expect(client.list).toHaveBeenCalledWith(
        "",
        "v1",
        "nodes",
        undefined,
        expect.any(Object),
      )
    })
  })

  it("returns the queryClient so tests can clear or inspect the cache", () => {
    const client = createMockK8sClient()
    const { queryClient } = renderWithK8sProvider(<p>hello</p>, { client })
    expect(queryClient).toBeDefined()
    expect(queryClient.getQueryCache).toBeInstanceOf(Function)
  })
})
