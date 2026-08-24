import { describe, it, expect, vi, beforeAll } from "vitest"
import { screen } from "@testing-library/react"
import { Route, Routes, useLocation } from "react-router"
import {
  K8sClient,
  type K8sList,
  type APIGroupList,
  type SelfSubjectAccessReview,
} from "@cozystack/k8s-client"
import { AdminPage } from "./AdminPage.tsx"
import { TenantProvider } from "../lib/tenant-context.tsx"
import { renderWithK8sProvider } from "../test-utils/render.tsx"

// Minimal Info ApplicationDefinition so InfoRedirect can resolve the default
// admin landing to its generic detail route.
const INFO_AD = {
  apiVersion: "cozystack.io/v1alpha1",
  kind: "ApplicationDefinition",
  metadata: { name: "info" },
  spec: {
    application: { kind: "Info", plural: "infos", singular: "info" },
    dashboard: { module: true, category: "Administration" },
  },
}

// Echoes the router location so a redirect chain can be asserted by URL.
function LocationProbe() {
  const { pathname } = useLocation()
  return <div>{pathname}</div>
}

/**
 * Answer each SelfSubjectAccessReview by its requested resource so the two
 * admin gates (nodes/list for Cluster Usage, backupclasses/update for Backup
 * Classes) can be exercised independently.
 */
function makeClient(allow: Record<string, boolean>): K8sClient {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockImplementation(async (_g, _v, plural) => {
    return {
      apiVersion: "v1",
      kind: `${plural}List`,
      metadata: {},
      items: plural === "applicationdefinitions" ? [INFO_AD] : [],
    } as K8sList<unknown>
  })
  vi.spyOn(client, "getApiGroups").mockResolvedValue({
    kind: "APIGroupList",
    apiVersion: "v1",
    groups: [],
  } as APIGroupList)
  vi.spyOn(client, "create").mockImplementation(async (_g, _v, _p, body) => {
    const resource =
      (body as SelfSubjectAccessReview).spec?.resourceAttributes?.resource ?? ""
    return {
      ...(body as object),
      status: { allowed: allow[resource] === true },
    } as unknown
  })
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

describe("AdminPage routing & access gate", () => {
  it("renders the Cluster Usage page at /capacity/cluster for an operator", async () => {
    renderWithK8sProvider(<AdminPage />, {
      client: makeClient({ nodes: true }),
      initialRoute: "/capacity/cluster",
    })
    expect(await screen.findByText("Cluster")).toBeInTheDocument()
  })

  it("redirects the index route to Administration (Tenants)", async () => {
    renderWithK8sProvider(
      <>
        <LocationProbe />
        <Routes>
          <Route
            path="/admin/*"
            element={
              <TenantProvider>
                <AdminPage />
              </TenantProvider>
            }
          />
        </Routes>
      </>,
      { client: makeClient({}), initialRoute: "/admin" },
    )
    expect(await screen.findByText("/admin/tenants")).toBeInTheDocument()
  })

  it("allows Administration (Tenants) even when the user has no operator area", async () => {
    // Administration needs no special permission, so the portal has no
    // top-level gate any more — a user with neither operator area still
    // reaches the Tenants page.
    renderWithK8sProvider(
      <TenantProvider>
        <AdminPage />
      </TenantProvider>,
      { client: makeClient({ nodes: false, backupclasses: false }), initialRoute: "/tenants" },
    )
    expect(
      await screen.findByRole("heading", { name: "Tenants" }),
    ).toBeInTheDocument()
  })

  it("guards capacity routes for a backup-only operator hitting a capacity URL", async () => {
    // The capacity area must stay closed without nodes/list, even though the
    // portal itself is now ungated.
    renderWithK8sProvider(<AdminPage />, {
      client: makeClient({ nodes: false, backupclasses: true }),
      initialRoute: "/capacity/cluster",
    })
    expect(
      await screen.findByText(/you do not have permission to view cluster capacity/i),
    ).toBeInTheDocument()
    expect(screen.queryByText("Cluster")).not.toBeInTheDocument()
  })

  it("guards backup-class routes for a capacity-only operator hitting a backups URL", async () => {
    renderWithK8sProvider(<AdminPage />, {
      client: makeClient({ nodes: true, backupclasses: false }),
      initialRoute: "/backups/backupclasses",
    })
    expect(
      await screen.findByText(/you do not have permission to manage backup classes/i),
    ).toBeInTheDocument()
  })
})
