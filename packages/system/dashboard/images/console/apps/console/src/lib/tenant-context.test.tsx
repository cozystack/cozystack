import { describe, it, expect, vi, beforeEach } from "vitest"
import { screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { useNavigate } from "react-router"
import { K8sClient, type K8sList } from "@cozystack/k8s-client"
import { TenantProvider, useTenantContext, useTenantFromUrl } from "./tenant-context.tsx"
import { SELECTED_TENANT_KEY } from "./constants.ts"
import { renderWithK8sProvider } from "../test-utils/render.tsx"

function tn(name: string) {
  return {
    apiVersion: "core.cozystack.io/v1alpha1",
    kind: "TenantNamespace",
    metadata: { name, labels: {} },
  }
}

function makeClient(): K8sClient {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockImplementation(async (_g, _v, plural) => {
    const items =
      plural === "tenantnamespaces"
        ? [tn("tenant-acme"), tn("tenant-globex")]
        : []
    return {
      apiVersion: "v1",
      kind: `${plural}List`,
      metadata: {},
      items,
    } as K8sList<unknown>
  })
  return client
}

function Probe() {
  useTenantFromUrl()
  const { tenantNamespace, selectTenant } = useTenantContext()
  const navigate = useNavigate()
  return (
    <>
      <output>{tenantNamespace ?? "none"}</output>
      <button type="button" onClick={() => selectTenant("globex")}>
        pick globex
      </button>
      <button type="button" onClick={() => navigate("/console")}>
        leave
      </button>
      <button type="button" onClick={() => navigate(-1)}>
        back
      </button>
    </>
  )
}

function renderAt(route: string) {
  return renderWithK8sProvider(
    <TenantProvider>
      <Probe />
    </TenantProvider>,
    { client: makeClient(), initialRoute: route },
  )
}

describe("useTenantFromUrl", () => {
  beforeEach(() => {
    window.localStorage.clear()
  })

  // The detail routes take the resource name from the URL and the namespace
  // from the tenant context, so a cross-tenant link has to name its tenant in
  // the URL. Middle click and open-in-new-tab never run onClick, which is the
  // only other thing that aligns the context.
  it("selects the tenant named in the query string with no click", async () => {
    window.localStorage.setItem(SELECTED_TENANT_KEY, "globex")

    renderAt("/admin/tenants/x?tenant=acme")

    expect(await screen.findByText("tenant-acme")).toBeInTheDocument()
  })

  it("persists the tenant from the URL so the rest of the session agrees", async () => {
    window.localStorage.setItem(SELECTED_TENANT_KEY, "globex")

    renderAt("/admin/tenants/x?tenant=acme")

    await screen.findByText("tenant-acme")
    expect(window.localStorage.getItem(SELECTED_TENANT_KEY)).toBe("acme")
  })

  // The picker switches tenant without leaving the page, so re-applying the
  // param on every render would drag the user back to it.
  it("does not drag the picker back to the tenant in the URL", async () => {
    renderAt("/console/vminstances/demo?tenant=acme")
    await screen.findByText("tenant-acme")

    await userEvent.click(screen.getByRole("button", { name: "pick globex" }))

    expect(await screen.findByText("tenant-globex")).toBeInTheDocument()
  })

  it("asserts the URL tenant again when the entry is revisited", async () => {
    renderAt("/console/vminstances/demo?tenant=acme")
    await screen.findByText("tenant-acme")
    await userEvent.click(screen.getByRole("button", { name: "pick globex" }))
    await screen.findByText("tenant-globex")

    await userEvent.click(screen.getByRole("button", { name: "leave" }))
    await userEvent.click(screen.getByRole("button", { name: "back" }))

    expect(await screen.findByText("tenant-acme")).toBeInTheDocument()
  })

  // A pasted or shared URL can name a tenant this user cannot see. The
  // provider already refuses to select it; the point here is that the refusal
  // reaches storage too, so the next load does not open on it and correct
  // itself again.
  it("does not leave an unreachable tenant from the URL in storage", async () => {
    window.localStorage.setItem(SELECTED_TENANT_KEY, "acme")

    renderAt("/admin/tenants/x?tenant=initech")

    // "acme" is the first tenant in the list and there is no "root" here, so
    // the fallback lands on it — the assertion is about storage, not the pick.
    expect(await screen.findByText("tenant-acme")).toBeInTheDocument()
    expect(window.localStorage.getItem(SELECTED_TENANT_KEY)).toBe("acme")
  })

  it("leaves the stored tenant alone when the URL names none", async () => {
    window.localStorage.setItem(SELECTED_TENANT_KEY, "globex")

    renderAt("/admin/tenants/x")

    expect(await screen.findByText("tenant-globex")).toBeInTheDocument()
  })
})
