import { describe, it, expect, vi, beforeAll } from "vitest"
import { screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import {
  K8sClient,
  type K8sList,
  type APIGroupList,
} from "@cozystack/k8s-client"
import { TenantsPage } from "./TenantsPage.tsx"
import { TenantProvider } from "../lib/tenant-context.tsx"
import { SELECTED_TENANT_KEY } from "../lib/constants.ts"
import { renderWithK8sProvider } from "../test-utils/render.tsx"

function tn(name: string, ancestors: string[]) {
  const labels: Record<string, string> = {}
  for (const a of [...ancestors, name]) labels[`tenant.cozystack.io/${a}`] = ""
  return {
    apiVersion: "core.cozystack.io/v1alpha1",
    kind: "TenantNamespace",
    metadata: { name, labels },
  }
}

// Selected tenant "whmcs". The user can see the subtree except
// tenant-whmcs-a — its child tenant-whmcs-a-b must still render, attached to
// the nearest visible ancestor.
const VISIBLE = [
  tn("tenant-whmcs", ["tenant-root"]),
  tn("tenant-whmcs-x", ["tenant-root", "tenant-whmcs"]),
  tn("tenant-whmcs-a-b", ["tenant-root", "tenant-whmcs", "tenant-whmcs-a"]),
]

function makeClient(): K8sClient {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockImplementation(async (_g, _v, plural) => {
    const items = plural === "tenantnamespaces" ? VISIBLE : []
    return {
      apiVersion: "v1",
      kind: `${plural}List`,
      metadata: {},
      items,
    } as K8sList<unknown>
  })
  vi.spyOn(client, "getApiGroups").mockResolvedValue({
    kind: "APIGroupList",
    apiVersion: "v1",
    groups: [],
  } as APIGroupList)
  return client
}

beforeAll(() => {
  const store = new Map<string, string>([[SELECTED_TENANT_KEY, "whmcs"]])
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  })
})

describe("TenantsPage tree", () => {
  it("renders the visible subtree and bridges over an invisible parent", async () => {
    renderWithK8sProvider(
      <TenantProvider>
        <TenantsPage />
      </TenantProvider>,
      { client: makeClient(), initialRoute: "/admin/tenants" },
    )

    // All visible nodes render with names relative to their visible parent,
    // including the one whose direct parent (tenant-whmcs-a) is not
    // accessible — it bridges to tenant-whmcs and shows the remaining path.
    expect(await screen.findByText("x")).toBeInTheDocument()
    expect(await screen.findByText("a-b")).toBeInTheDocument()

    // Every node offers sub-tenant creation at its own level.
    expect(
      screen.getByTitle("Create a sub-tenant under whmcs-a-b"),
    ).toBeInTheDocument()

    // The forest root (selected tenant, parent invisible) has no Edit button;
    // children do.
    const editButtons = screen.getAllByRole("button", { name: /edit/i })
    expect(editButtons).toHaveLength(2)
  })

  it("collapses a subtree via the row toggle", async () => {
    const user = userEvent.setup()
    renderWithK8sProvider(
      <TenantProvider>
        <TenantsPage />
      </TenantProvider>,
      { client: makeClient(), initialRoute: "/admin/tenants" },
    )

    expect(await screen.findByText("x")).toBeInTheDocument()
    // whmcs is the only node with children → the only collapse toggle.
    await user.click(screen.getByTitle("Collapse"))
    expect(screen.queryByText("x")).not.toBeInTheDocument()
    expect(screen.queryByText("a-b")).not.toBeInTheDocument()
    await user.click(screen.getByTitle("Expand"))
    expect(await screen.findByText("x")).toBeInTheDocument()
  })
})
