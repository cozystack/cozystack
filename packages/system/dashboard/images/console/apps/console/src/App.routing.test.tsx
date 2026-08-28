import { describe, it, expect, vi, beforeAll } from "vitest"
import { screen, waitFor } from "@testing-library/react"
import {
  K8sClient,
  type K8sList,
  type APIGroupList,
} from "@cozystack/k8s-client"
import App from "./App.tsx"
import { renderWithK8sProvider } from "./test-utils/render.tsx"

function makeClient(tenants: unknown[] = []): K8sClient {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockImplementation(async (_g, _v, plural) => {
    return {
      apiVersion: "v1",
      kind: `${plural}List`,
      metadata: {},
      items: plural === "tenantnamespaces" ? tenants : [],
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

describe("shell subtitle", () => {
  // The picker only renders its "Tenant /" label once a tenant list has
  // arrived; asserting against an empty list passes whether or not the picker
  // is there, because its empty and loading branches say something else.
  // The picker labels itself "Tenant"; the admin route is Modules rather than
  // the tenant list, whose own "Tenant" create button would match too.
  const TENANTS = ["tenant-acme", "tenant-globex"].map((name) => ({
    apiVersion: "core.cozystack.io/v1alpha1",
    kind: "TenantNamespace",
    metadata: { name, labels: {} },
  }))

  it("hides the tenant picker in the admin portal", async () => {
    const client = makeClient(TENANTS)
    renderWithK8sProvider(<App />, { client, initialRoute: "/admin/modules" })

    // The picker's first render says "Loading tenants…"; wait that out, or the
    // absence below passes only because the list had not arrived yet.
    await waitFor(() => expect(screen.queryByText("Loading tenants…")).toBeNull())
    expect(screen.queryByText("Tenant", { exact: true })).toBeNull()
  })

  it("still shows the tenant picker outside the admin portal", async () => {
    const client = makeClient(TENANTS)
    renderWithK8sProvider(<App />, { client, initialRoute: "/console" })

    expect(await screen.findByText("Tenant", { exact: true })).toBeTruthy()
  })
})
