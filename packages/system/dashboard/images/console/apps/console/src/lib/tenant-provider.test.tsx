import { describe, it, expect, vi, beforeAll, beforeEach } from "vitest"
import { screen, waitFor } from "@testing-library/react"
import { K8sClient, type K8sList } from "@cozystack/k8s-client"
import { renderWithK8sProvider } from "../test-utils/render.tsx"
import { SELECTED_TENANT_KEY } from "./constants.ts"
import { TenantProvider } from "./tenant-provider.tsx"
import { useTenantContext } from "./tenant-context.tsx"

function SelectedTenantProbe() {
  const { selectedTenant } = useTenantContext()
  return <div data-testid="selected">{selectedTenant ?? ""}</div>
}

function tenantNamespace(name: string) {
  return { apiVersion: "core.cozystack.io/v1alpha1", kind: "TenantNamespace", metadata: { name } }
}

function makeClient(names: string[]): K8sClient {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockImplementation(async (_g, _v, plural) => {
    return {
      apiVersion: "v1",
      kind: `${plural}List`,
      metadata: {},
      items: plural === "tenantnamespaces" ? names.map(tenantNamespace) : [],
    } as K8sList<unknown>
  })
  return client
}

function renderAt(route: string, names: string[]) {
  renderWithK8sProvider(
    <TenantProvider>
      <SelectedTenantProbe />
    </TenantProvider>,
    { client: makeClient(names), initialRoute: route },
  )
}

async function selected(): Promise<string> {
  await waitFor(() => expect(screen.getByTestId("selected").textContent).not.toBe(""))
  return screen.getByTestId("selected").textContent ?? ""
}

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

beforeEach(() => {
  window.localStorage.clear()
})

const tenants = ["tenant-root", "tenant-whmcs-abc", "tenant-whmcs-xyz"]

describe("TenantProvider tenant from a link", () => {
  it("selects the tenant named by ?namespace=", async () => {
    window.localStorage.setItem(SELECTED_TENANT_KEY, "whmcs-xyz")
    renderAt("/?namespace=tenant-whmcs-abc", tenants)

    expect(await selected()).toBe("whmcs-abc")
  })

  it("remembers the linked tenant for the next visit", async () => {
    renderAt("/?namespace=tenant-whmcs-abc", tenants)
    await selected()

    expect(window.localStorage.getItem(SELECTED_TENANT_KEY)).toBe("whmcs-abc")
  })

  it("falls back to root when the linked tenant is not visible", async () => {
    renderAt("/?namespace=tenant-someone-else", tenants)

    await waitFor(() => expect(screen.getByTestId("selected").textContent).toBe("root"))
  })

  it("keeps the stored tenant when there is no link", async () => {
    window.localStorage.setItem(SELECTED_TENANT_KEY, "whmcs-xyz")
    renderAt("/console", tenants)

    expect(await selected()).toBe("whmcs-xyz")
  })

  it("ignores a namespace that is not a tenant namespace", async () => {
    window.localStorage.setItem(SELECTED_TENANT_KEY, "whmcs-xyz")
    renderAt("/?namespace=cozy-system", tenants)

    expect(await selected()).toBe("whmcs-xyz")
  })
})
