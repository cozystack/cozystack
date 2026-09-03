import { describe, it, expect, vi, beforeAll } from "vitest"
import { screen } from "@testing-library/react"
import {
  K8sClient,
  type K8sList,
  type APIGroupList,
} from "@cozystack/k8s-client"
import App from "./App.tsx"
import { renderWithK8sProvider } from "./test-utils/render.tsx"

function makeClient(): K8sClient {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockImplementation(async (_g, _v, plural) => {
    return {
      apiVersion: "v1",
      kind: `${plural}List`,
      metadata: {},
      items: [],
    } as K8sList<unknown>
  })
  vi.spyOn(client, "getApiGroups").mockResolvedValue({
    kind: "APIGroupList",
    apiVersion: "v1",
    groups: [],
  } as APIGroupList)
  vi.spyOn(client, "create").mockResolvedValue({
    apiVersion: "authorization.k8s.io/v1",
    kind: "SelfSubjectAccessReview",
    metadata: { name: "" },
    spec: {},
    status: { allowed: false },
  } as unknown)
  return client
}

// TenantProvider reads window.localStorage on mount; provide a minimal
// in-memory shim for the test environment when one is not present.
beforeAll(() => {
  if (typeof globalThis.localStorage?.getItem !== "function") {
    const store = new Map<string, string>()
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
      clear: () => store.clear(),
    })
  }
})

describe("default landing", () => {
  it("opens the Console when no path is given", async () => {
    const client = makeClient()
    renderWithK8sProvider(<App />, { client, initialRoute: "/" })

    // ConsoleOverview's no-tenant state: reached only under /console, and it
    // needs no cluster data, so it is a stable marker for "we landed here".
    expect(
      await screen.findByText(/Select a tenant to view its deployed applications/i),
    ).toBeTruthy()
    // The catalog must not be what greets the user. Matching on the heading
    // rather than the text keeps the Marketplace *tab* from satisfying this.
    expect(screen.queryByRole("heading", { name: "Marketplace", level: 1 })).toBeNull()
  })

  it("still serves the Marketplace when it is asked for directly", async () => {
    const client = makeClient()
    renderWithK8sProvider(<App />, { client, initialRoute: "/marketplace" })

    expect(
      await screen.findByRole("heading", { name: "Marketplace", level: 1 }),
    ).toBeTruthy()
  })
})
