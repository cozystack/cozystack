import { beforeEach, describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"
import { MemoryRouter, Routes, Route } from "react-router"

// Drive useK8sGet's result per test; the page's loading/error guards are the unit under test.
const h = vi.hoisted(() => ({
  get: { data: undefined as unknown, isLoading: true, error: undefined as unknown },
  appKind: "Postgres",
  appPlural: "postgreses",
  tabLabels: [] as string[],
  list: vi.fn((_ref: unknown, _options?: unknown) => {
    void _ref
    void _options
    return { data: undefined, isLoading: false }
  }),
}))

vi.mock("@cozystack/k8s-client", () => ({
  useK8sGet: () => h.get,
  useK8sDelete: () => ({ mutateAsync: vi.fn() }),
  // Presence probes (use-resource-presence.ts) — report empty lists.
  useK8sList: h.list,
}))
vi.mock("../../lib/app-definitions.ts", () => ({
  useApplicationDefinitions: () => ({
    data: {
      items: [
        {
          metadata: { name: "app" },
          spec: { application: { plural: h.appPlural, kind: h.appKind } },
        },
      ],
    },
  }),
  appDisplayName: () => h.appKind,
  iconDataUrl: () => null,
  releasePrefix: () => "",
  isTenantModule: () => false,
}))
vi.mock("../../lib/tenant-context.tsx", () => ({
  useTenantContext: () => ({ tenantNamespace: "tenant-test" }),
}))
// Stub the tab tree — the loading/error branches return before any tab renders,
// and these modules pull heavy deps (noVNC, Monaco) we don't want in jsdom.
// The tab bar is the unit under test for ordering, so it records what it is given.
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
vi.mock("./SerialTab.tsx", () => ({ SerialTab: () => null }))
vi.mock("./VMPowerControls.tsx", () => ({ VMPowerControls: () => null }))

vi.mock("./DiskUploadPanel.tsx", () => ({ DiskUploadPanel: () => <div>Disk upload panel</div> }))

const { ApplicationDetailPage } = await import("./ApplicationDetailPage.tsx")

function renderPage() {
  return render(
    <MemoryRouter initialEntries={[`/console/${h.appPlural}/demo`]}>
      <Routes>
        <Route path="/console/:plural/:name" element={<ApplicationDetailPage />} />
      </Routes>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  h.get = { data: undefined, isLoading: true, error: undefined }
  h.appKind = "Postgres"
  h.appPlural = "postgreses"
  h.tabLabels = []
  h.list.mockClear()
})

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

describe("ApplicationDetailPage tabs for a virtual machine", () => {
  it("leads with the consoles, serial first", () => {
    h.appKind = "VMInstance"
    h.appPlural = "vminstances"
    h.get = {
      data: { kind: "VMInstance", metadata: { name: "demo", namespace: "tenant-test" } },
      isLoading: false,
      error: undefined,
    }
    h.tabLabels = []

    renderPage()

    // Reaching a console is why people open a VM, and serial is the one that
    // carries text. VNC stays for the cases it cannot reach.
    expect(h.tabLabels).toEqual([
      "Overview",
      "Console",
      "VNC",
      "Workloads",
      "Services",
      "Events",
    ])
  })
})


describe("ApplicationDetailPage presence probes", () => {
  it.each([
    ["VMDisk", "vmdisks"],
    ["VMInstance", "vminstances"],
  ])("disables all six unused probes for %s", (kind, plural) => {
    h.appKind = kind
    h.appPlural = plural
    h.get = {
      data: {
        apiVersion: "apps.cozystack.io/v1alpha1",
        kind,
        metadata: { name: "demo", namespace: "tenant-test" },
        spec: {},
      },
      isLoading: false,
      error: undefined,
    }
    renderPage()
    if (kind === "VMDisk") expect(screen.getByText("Disk upload panel")).toBeInTheDocument()
    else expect(screen.queryByText("Disk upload panel")).not.toBeInTheDocument()
    expect(h.list).toHaveBeenCalledTimes(6)
    for (const call of h.list.mock.calls) {
      expect(call[1]).toEqual(expect.objectContaining({ enabled: false }))
    }
  })

  it("keeps presence probes enabled for ordinary applications", () => {
    h.get = {
      data: {
        apiVersion: "apps.cozystack.io/v1alpha1",
        kind: "Postgres",
        metadata: { name: "demo", namespace: "tenant-test" },
        spec: {},
      },
      isLoading: false,
      error: undefined,
    }
    renderPage()
    expect(screen.queryByText("Disk upload panel")).not.toBeInTheDocument()
    expect(h.list).toHaveBeenCalledTimes(6)
    for (const call of h.list.mock.calls) {
      expect(call[1]).toEqual(expect.objectContaining({ enabled: true }))
    }
  })
})
