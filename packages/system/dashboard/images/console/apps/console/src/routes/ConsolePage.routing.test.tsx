import { describe, it, expect, vi, beforeAll } from "vitest"
import { screen, waitFor } from "@testing-library/react"
import {
  K8sClient,
  type K8sList,
  type APIGroupList,
} from "@cozystack/k8s-client"
import { ConsolePage } from "./ConsolePage.tsx"
import { TenantProvider } from "../lib/tenant-context.tsx"
import { renderWithK8sProvider } from "../test-utils/render.tsx"

function makeClient(): K8sClient {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockImplementation(async (_g, _v, plural) => {
    if (plural === "tenantnamespaces") {
      return {
        apiVersion: "core.cozystack.io/v1alpha1",
        kind: "TenantNamespaceList",
        metadata: {},
        items: [],
      } as K8sList<unknown>
    }
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

describe("ConsolePage routing", () => {
  it("no longer serves the Cluster Usage page under console (moved to /admin)", async () => {
    const client = makeClient()
    renderWithK8sProvider(
      <TenantProvider>
        <ConsolePage />
      </TenantProvider>,
      { client, initialRoute: "/cluster-usage" },
    )
    // "cluster-usage" now falls through to the generic :plural list route, so
    // the Cluster Usage page's unique subtitle must not appear.
    await waitFor(() => expect(client.list).toHaveBeenCalled())
    expect(screen.queryByText(/Cluster-scoped capacity/i)).toBeNull()
  })
})
