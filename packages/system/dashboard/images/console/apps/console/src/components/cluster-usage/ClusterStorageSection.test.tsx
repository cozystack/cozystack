import { describe, it, expect, vi } from "vitest"
import { screen, within, waitFor } from "@testing-library/react"
import { K8sClient, K8sApiError, type K8sList } from "@cozystack/k8s-client"
import { ClusterStorageSection } from "./ClusterStorageSection.tsx"
import { renderWithK8sProvider } from "../../test-utils/render.tsx"

let pvcSeq = 0
function pvc(namespace: string, storageClassName: string, requested: string, capacity?: string) {
  return {
    apiVersion: "v1",
    kind: "PersistentVolumeClaim",
    metadata: { name: `pvc-${pvcSeq++}`, namespace },
    spec: { storageClassName, resources: { requests: { storage: requested } } },
    status: { phase: "Bound", capacity: capacity ? { storage: capacity } : undefined },
  }
}

function makeClient(pvcs: unknown[]): K8sClient {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockImplementation(async (_g, _v, plural) => {
    return {
      apiVersion: "v1",
      kind: `${plural}List`,
      metadata: {},
      items: plural === "persistentvolumeclaims" ? pvcs : [],
    } as K8sList<unknown>
  })
  return client
}

function makeFailingClient(error: Error): K8sClient {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockRejectedValue(error)
  return client
}

describe("ClusterStorageSection", () => {
  it("aggregates tenant PVCs by storage class and excludes non-tenant namespaces", async () => {
    const client = makeClient([
      pvc("tenant-foo", "replicated", "5Gi"),
      pvc("tenant-bar", "replicated", "10Gi"),
      // System namespace must be excluded.
      pvc("cozy-system", "replicated", "100Gi"),
    ])
    const { container } = renderWithK8sProvider(<ClusterStorageSection />, { client })
    const row = await waitForRow(container, "replicated")
    // Two tenant PVCs (the cozy-system one is excluded).
    expect(within(row).getByText("2")).toBeInTheDocument()
  })

  it("links a storage class to its per-class drill-down", async () => {
    const client = makeClient([pvc("tenant-foo", "replicated", "5Gi")])
    renderWithK8sProvider(<ClusterStorageSection />, { client })
    const link = await screen.findByRole("link", { name: "replicated" })
    expect(link).toHaveAttribute("href", "/admin/capacity/cluster/sc/replicated")
  })

  it("shows an empty state when no tenant PVCs exist", async () => {
    const client = makeClient([pvc("cozy-system", "replicated", "100Gi")])
    renderWithK8sProvider(<ClusterStorageSection />, { client })
    expect(
      await screen.findByText(/no persistent volume claims found/i),
    ).toBeInTheDocument()
  })

  it("shows a permission notice when the PVC list is forbidden", async () => {
    // A 403 must not be silently rendered as an empty "no PVCs found" state.
    const client = makeFailingClient(new K8sApiError(403, { message: "forbidden" }))
    renderWithK8sProvider(<ClusterStorageSection />, { client })
    expect(
      await screen.findByText(/you do not have permission to view persistent volume claims/i),
    ).toBeInTheDocument()
    expect(screen.queryByText(/no persistent volume claims found/i)).not.toBeInTheDocument()
  })

  it("shows a failure notice when the PVC list errors", async () => {
    const client = makeFailingClient(new K8sApiError(500, { message: "boom" }))
    renderWithK8sProvider(<ClusterStorageSection />, { client })
    expect(
      await screen.findByText(/failed to load persistent volume claims/i),
    ).toBeInTheDocument()
  })
})

async function waitForRow(container: HTMLElement, sc: string): Promise<HTMLElement> {
  await waitFor(() =>
    expect(container.querySelector(`[data-storageclass-row="${sc}"]`)).not.toBeNull(),
  )
  return container.querySelector(`[data-storageclass-row="${sc}"]`) as HTMLElement
}
