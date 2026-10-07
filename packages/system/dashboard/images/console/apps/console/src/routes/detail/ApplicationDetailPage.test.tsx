import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"
import { MemoryRouter, Routes, Route } from "react-router"
import { K8sApiError } from "@cozystack/k8s-client"

// Drive useK8sGet's result per test; the page's loading/error guards are the unit under test.
const h = vi.hoisted(() => ({
  get: { data: undefined as unknown, isLoading: true, error: undefined as unknown },
  configMaps: [] as unknown[],
  configMapsError: undefined as unknown,
  tabLabels: [] as string[],
}))

vi.mock("@cozystack/k8s-client", async (importOriginal) => ({
  K8sApiError: (await importOriginal<typeof import("@cozystack/k8s-client")>()).K8sApiError,
  useK8sGet: () => h.get,
  useK8sDelete: () => ({ mutateAsync: vi.fn() }),
  // Presence probes (use-resource-presence.ts) — report empty lists.
  useK8sList: (ref: { plural: string }) => ref.plural === "configmaps"
    ? { data: { items: h.configMaps }, error: h.configMapsError, isLoading: false }
    : { data: { items: [] }, isLoading: false },
}))
vi.mock("../../lib/app-definitions.ts", () => ({
  useApplicationDefinitions: () => ({
    data: { items: [{ metadata: { name: "postgres" }, spec: { application: { plural: "postgreses", kind: "Postgres" } } }] },
  }),
  appDisplayName: () => "Postgres",
  iconDataUrl: () => null,
  releasePrefix: () => "",
  isTenantModule: () => false,
}))
vi.mock("../../lib/tenant-context.tsx", () => ({
  useTenantContext: () => ({ tenantNamespace: "tenant-test" }),
}))
// Stub the tab tree — the loading/error branches return before any tab renders,
// and these modules pull heavy deps (noVNC, Monaco) we don't want in jsdom.
vi.mock("./tabs.tsx", () => ({
  TabBar: ({ tabs }: { tabs: { label: string }[] }) => {
    h.tabLabels = tabs.map((t) => t.label)
    return null
  },
}))
vi.mock("./OverviewTab.tsx", () => ({ OverviewTab: () => null }))
vi.mock("./WorkloadsTab.tsx", () => ({ WorkloadsTab: () => null }))
vi.mock("./ServicesTab.tsx", () => ({ ServicesTab: () => null }))
vi.mock("./IngressesTab.tsx", () => ({ IngressesTab: () => null }))
vi.mock("./SecretsTab.tsx", () => ({ SecretsTab: () => null }))
vi.mock("./EventsTab.tsx", () => ({ EventsTab: () => null }))
vi.mock("./VncTab.tsx", () => ({ VncTab: () => null }))
vi.mock("./VMPowerControls.tsx", () => ({ VMPowerControls: () => null }))

const { ApplicationDetailPage } = await import("./ApplicationDetailPage.tsx")

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/console/postgreses/demo"]}>
      <Routes>
        <Route path="/console/:plural/:name" element={<ApplicationDetailPage />} />
      </Routes>
    </MemoryRouter>,
  )
}

describe("ApplicationDetailPage guards", () => {
  it("renders the not-found message on a failed GET instead of an infinite spinner", () => {
    // Regression: previously `isLoading || !instance || !ad` ran before the
    // error branch, so a failed GET (isLoading=false, instance=undefined) spun
    // forever and the error branch was unreachable.
    h.get = { data: undefined, isLoading: false, error: new Error("403 Forbidden") }
    renderPage()
    expect(screen.getByText("not found.", { exact: false })).toBeInTheDocument()
    expect(screen.queryByText("Loading…")).not.toBeInTheDocument()
  })

  it("still shows the spinner while the instance is loading", () => {
    h.get = { data: undefined, isLoading: true, error: undefined }
    renderPage()
    expect(screen.getByText("Loading…")).toBeInTheDocument()
  })
})


describe("ApplicationDetailPage configuration", () => {
  it("offers ConfigMaps declared by the application's resource map", () => {
    h.get = {
      data: { kind: "Postgres", metadata: { name: "demo", namespace: "tenant-test" } },
      isLoading: false,
      error: undefined,
    }
    h.configMaps = [{ metadata: { name: "demo-resourcemap" }, data: {
      resources: "- apiVersion: v1\n  kind: ConfigMap\n  name: postgres-demo-config",
    } }]
    h.tabLabels = []
    renderPage()
    expect(h.tabLabels).toContain("ConfigMaps")
  })

  it("drops the ConfigMaps tab when the resource map stops being readable", () => {
    h.get = {
      data: { kind: "Postgres", metadata: { name: "demo", namespace: "tenant-test" } },
      isLoading: false,
      error: undefined,
    }
    h.configMaps = [{ metadata: { name: "demo-resourcemap" }, data: {
      resources: "- apiVersion: v1\n  kind: ConfigMap\n  name: postgres-demo-config",
    } }]
    h.configMapsError = new K8sApiError(403, "forbidden")
    h.tabLabels = []
    renderPage()
    expect(h.tabLabels).not.toContain("ConfigMaps")
  })
})
