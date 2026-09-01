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

    // A reachable Tenant CR links directly to its detail page, where Delete is
    // available. A bridged row with an unreadable real parent stays plain text.
    // The CR name is relative to the parent, so the link has to name the tenant
    // too: middle click and open-in-new-tab never run onClick, and the detail
    // page would otherwise resolve "x" against the previously selected tenant.
    expect(screen.getByRole("link", { name: "x" })).toHaveAttribute(
      "href",
      "/admin/tenants/x?tenant=whmcs",
    )
    expect(screen.queryByRole("link", { name: "a-b" })).toBeNull()
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

describe("TenantsPage under a visible root", () => {
  it("reaches the Tenant CR of a tenant ordered directly in the root", async () => {
    // Start on the tenant this fixture actually contains: landing on one it
    // does not makes the provider fall back and refetch, and the page blinks
    // back through its spinner mid-assertion.
    localStorage.setItem(SELECTED_TENANT_KEY, "root")
    renderWithK8sProvider(
      <TenantProvider>
        <TenantsPage />
      </TenantProvider>,
      { client: makeClient(ROOT_VISIBLE), initialRoute: "/admin/tenants" },
    )

    // The root's CR is self-referential: `root` inside its own namespace.
    expect(await screen.findByRole("link", { name: "root" })).toHaveAttribute(
      "href",
      "/admin/tenants/root?tenant=root",
    )

    // The row that the prefix-only parent rule used to leave as plain text.
    expect(screen.getByRole("link", { name: "acme" })).toHaveAttribute(
      "href",
      "/admin/tenants/acme?tenant=root",
    )
    expect(screen.getByTitle("Edit tenant acme")).toBeInTheDocument()

    // A level down the rule applies as before, and the link names the parent
    // rather than the root.
    expect(screen.getByRole("link", { name: "eu" })).toHaveAttribute(
      "href",
      "/admin/tenants/eu?tenant=acme",
    )

    // `tenant-root-x` is the tenant `root-x`, not the tenant `x` — the row
    // label and the CR in the link have to agree on that.
    expect(screen.getByRole("link", { name: "root-x" })).toHaveAttribute(
      "href",
      "/admin/tenants/root-x?tenant=root",
    )
  })
})

// A forest root that carries no ancestor labels is self-referential: its CR
// lives in its own namespace. The list reaches it the same way it reaches any
// other row, and deriving the CR name must not subtract the node's own name
// from itself and leave nothing.
const SELF_ROOT = [tn("tenant-whmcs", [])]

describe("TenantsPage self-referential root", () => {
  it("names the CR of a root that is not tenant-root", async () => {
    localStorage.setItem(SELECTED_TENANT_KEY, "whmcs")

    renderWithK8sProvider(
      <TenantProvider>
        <TenantsPage />
      </TenantProvider>,
      { client: makeClient(SELF_ROOT), initialRoute: "/admin/tenants" },
    )

    expect(await screen.findByRole("link", { name: "whmcs" })).toHaveAttribute(
      "href",
      "/admin/tenants/whmcs?tenant=whmcs",
    )
  })
})
