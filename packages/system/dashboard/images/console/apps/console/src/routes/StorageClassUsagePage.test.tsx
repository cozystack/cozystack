import { describe, it, expect, vi, beforeAll } from "vitest"
import { screen, within } from "@testing-library/react"
import { Route, Routes } from "react-router"
import { K8sClient, type K8sList } from "@cozystack/k8s-client"
import { StorageClassUsagePage } from "./StorageClassUsagePage.tsx"
import { TenantProvider } from "../lib/tenant-context.tsx"
import { renderWithK8sProvider } from "../test-utils/render.tsx"

let seq = 0
function pvc(
  namespace: string,
  storageClassName: string,
  requested: string,
  labels: Record<string, string> = {},
) {
  return {
    apiVersion: "v1",
    kind: "PersistentVolumeClaim",
    metadata: { name: `pvc-${seq++}`, namespace, labels },
    spec: { storageClassName, resources: { requests: { storage: requested } } },
    status: { phase: "Bound" },
  }
}

function appDef(kind: string, plural: string) {
  return {
    apiVersion: "cozystack.io/v1alpha1",
    kind: "ApplicationDefinition",
    metadata: { name: plural },
    spec: { application: { kind, plural, singular: kind.toLowerCase() } },
  }
}

const VM_LABELS = {
  "apps.cozystack.io/application.kind": "VMInstance",
  "apps.cozystack.io/application.name": "vm1",
}

function makeClient(pvcs: unknown[], appDefs: unknown[] = []): K8sClient {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockImplementation(async (_g, _v, plural) => {
    const items =
      plural === "persistentvolumeclaims"
        ? pvcs
        : plural === "applicationdefinitions"
          ? appDefs
          : []
    return { apiVersion: "v1", kind: `${plural}List`, metadata: {}, items } as K8sList<unknown>
  })
  return client
}

function renderPage(client: K8sClient, sc: string) {
  return renderWithK8sProvider(
    <TenantProvider>
      <Routes>
        <Route path="/sc/*" element={<StorageClassUsagePage />} />
      </Routes>
    </TenantProvider>,
    { client, initialRoute: `/sc/${sc}` },
  )
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

describe("StorageClassUsagePage", () => {
  it("groups PVCs of the storage class by owning workload, summing requests", async () => {
    const client = makeClient(
      [
        pvc("tenant-foo", "replicated", "5Gi", VM_LABELS),
        pvc("tenant-foo", "replicated", "5Gi", VM_LABELS),
        // Different storage class — excluded.
        pvc("tenant-foo", "fast", "20Gi", VM_LABELS),
        // Non-tenant namespace — excluded.
        pvc("cozy-system", "replicated", "100Gi"),
      ],
      [appDef("VMInstance", "vminstances")],
    )
    const { container } = renderPage(client, "replicated")

    await screen.findByText("vm1")
    expect(screen.getByText("tenant-foo")).toBeInTheDocument()
    // 5Gi + 5Gi = 10Gi requested (the fast/system PVCs are excluded). The value
    // appears in both the row and the total footer.
    const tbody = container.querySelector("tbody") as HTMLElement
    expect(within(tbody).getByText(/10(\.0)?Gi/)).toBeInTheDocument()
    expect(screen.queryByText("cozy-system")).toBeNull()
  })

  it("links the workload to its application page", async () => {
    const client = makeClient(
      [pvc("tenant-root", "replicated", "5Gi", {
        "apps.cozystack.io/application.kind": "VMInstance",
        "apps.cozystack.io/application.name": "demo-vm",
      })],
      [appDef("VMInstance", "vminstances")],
    )
    renderPage(client, "replicated")
    const link = await screen.findByRole("link", { name: "demo-vm" })
    expect(link).toHaveAttribute("href", "/console/vminstances/demo-vm/workloads")
  })

  it("shows an empty state when nothing uses the class", async () => {
    const client = makeClient([pvc("tenant-foo", "fast", "5Gi", VM_LABELS)])
    renderPage(client, "replicated")
    expect(await screen.findByText(/no tenant workloads use/i)).toBeInTheDocument()
  })

  it("renders the storage class as the heading", async () => {
    const client = makeClient([])
    renderPage(client, "replicated")
    expect(await screen.findByRole("heading", { name: "replicated" })).toBeInTheDocument()
  })
})
