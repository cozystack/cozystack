import { describe, it, expect, vi, beforeAll } from "vitest"
import { screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { useLocation } from "react-router"
import {
  K8sClient,
  type K8sList,
  type APIGroupList,
} from "@cozystack/k8s-client"
import { TenantsPage } from "./TenantsPage.tsx"
import { TenantProvider } from "../lib/tenant-provider.tsx"
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

function LocationProbe() {
  const { pathname } = useLocation()
  return <div data-testid="location">{pathname}</div>
}

function makeClient(tenants: unknown[] = VISIBLE): K8sClient {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockImplementation(async (_g, _v, plural) => {
    const items = plural === "tenantnamespaces" ? tenants : []
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

    // Edit is offered only where the node's REAL parent is readable, because
    // that is where its Tenant CR lives. Here that is tenant-whmcs-x alone:
    // the forest root's parent (tenant-root) is invisible, and so is
    // tenant-whmcs-a-b's real parent (tenant-whmcs-a) — the latter is the
    // bridged row, whose Edit used to be live and pointed at a CR named "a-b"
    // in tenant-whmcs, which does not exist.
    const editButtons = screen.getAllByRole("button", { name: /edit/i })
    expect(editButtons).toHaveLength(1)
    expect(screen.getByTitle("Edit tenant whmcs-x")).toBeInTheDocument()
    expect(screen.queryByTitle("Edit tenant whmcs-a-b")).not.toBeInTheDocument()
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

// Tenants ordered directly in the root are the common case on a normal
// cluster, and they are the one level the hierarchical namespace naming skips:
// `acme` created in `tenant-root` owns `tenant-acme`, not `tenant-root-acme`.
const ROOT_VISIBLE = [
  tn("tenant-root", []),
  tn("tenant-acme", ["tenant-root"]),
  tn("tenant-acme-eu", ["tenant-root", "tenant-acme"]),
  // A root-level tenant whose own name starts with `root`, which is the case
  // that makes "strip the parent's prefix" the wrong rule rather than merely
  // an incomplete one.
  tn("tenant-root-x", ["tenant-root"]),
]

function renderTenants(tenants: unknown[], selected: string) {
  localStorage.setItem(SELECTED_TENANT_KEY, selected)
  renderWithK8sProvider(
    <TenantProvider>
      <TenantsPage />
      <LocationProbe />
    </TenantProvider>,
    { client: makeClient(tenants), initialRoute: "/admin/tenants" },
  )
}

describe("TenantsPage under a visible root", () => {
  it("offers Edit on a tenant ordered directly in the root", async () => {
    renderTenants(ROOT_VISIBLE, "root")

    await userEvent.click(await screen.findByTitle("Edit tenant acme"))

    expect(screen.getByTestId("location")).toHaveTextContent("/admin/tenants/acme/edit")
  })

  it("names a nested tenant's CR relative to its parent", async () => {
    renderTenants(ROOT_VISIBLE, "root")

    await userEvent.click(await screen.findByTitle("Edit tenant acme-eu"))

    expect(screen.getByTestId("location")).toHaveTextContent("/admin/tenants/eu/edit")
  })

  // `tenant-root-x` is the tenant `root-x`, not the tenant `x`: the row label
  // and the CR the row opens have to agree on that.
  it("keeps a root-level tenant named root-x whole", async () => {
    renderTenants(ROOT_VISIBLE, "root")

    expect(await screen.findByText("root-x")).toBeInTheDocument()
    await userEvent.click(screen.getByTitle("Edit tenant root-x"))

    expect(screen.getByTestId("location")).toHaveTextContent("/admin/tenants/root-x/edit")
  })
})

// A forest root that carries no ancestor labels is self-referential: its CR
// lives in its own namespace, and deriving the CR name must not subtract the
// node's own name from itself and leave nothing.
describe("TenantsPage self-referential root", () => {
  it("names the CR of a root that is not tenant-root", async () => {
    renderTenants([tn("tenant-whmcs", [])], "whmcs")

    await userEvent.click(await screen.findByTitle("Edit tenant whmcs"))

    expect(screen.getByTestId("location")).toHaveTextContent("/admin/tenants/whmcs/edit")
  })
})
