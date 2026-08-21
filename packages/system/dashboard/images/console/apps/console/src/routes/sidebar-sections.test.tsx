import { describe, it, expect, vi } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  K8sClient,
  K8sProvider,
  type K8sList,
  type SelfSubjectAccessReview,
} from "@cozystack/k8s-client"
import type { ReactNode } from "react"
import {
  useAdminSidebarSections,
  useCanSeeAdmin,
  useConsoleSidebarSections,
} from "./sidebar-sections.tsx"

const emptyAppDefList: K8sList<unknown> = {
  apiVersion: "cozystack.io/v1alpha1",
  kind: "ApplicationDefinitionList",
  metadata: {},
  items: [],
}

// The admin gates issue two SSARs (nodes/list for Cluster Usage,
// backupclasses/update for Backup Classes); answer each by requested resource.
function makeClient(allow: Record<string, boolean | "pending">): K8sClient {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockResolvedValue(emptyAppDefList as K8sList<unknown>)
  vi.spyOn(client, "create").mockImplementation(async (_g, _v, _p, body) => {
    const resource =
      (body as SelfSubjectAccessReview).spec?.resourceAttributes?.resource ?? ""
    if (allow[resource] === "pending") return new Promise(() => ({})) as never
    return {
      ...(body as object),
      status: { allowed: allow[resource] === true },
    } as unknown
  })
  return client
}

function makeWrapper(client: K8sClient) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>
        <K8sProvider client={client} queryClient={queryClient}>
          {children}
        </K8sProvider>
      </QueryClientProvider>
    )
  }
}

function findItem(
  sections: { title: string; items: { label: string; to: string }[] }[],
  label: string,
) {
  for (const section of sections) {
    const found = section.items.find((i) => i.label === label)
    if (found) return found
  }
  return undefined
}

function hasItemTo(
  sections: { items: { to: string }[] }[],
  to: string,
) {
  return sections.some((s) => s.items.some((i) => i.to === to))
}

function sectionIndex(sections: { title: string }[], title: string) {
  return sections.findIndex((s) => s.title === title)
}

describe("useConsoleSidebarSections — admin areas moved out", () => {
  it("keeps the per-tenant Backups group but drops Cluster Usage and admin Backup Classes", async () => {
    const client = makeClient({ nodes: true, backupclasses: true })
    const { result } = renderHook(() => useConsoleSidebarSections(), {
      wrapper: makeWrapper(client),
    })
    await waitFor(() => expect(result.current.length).toBeGreaterThan(0))
    // Per-tenant backups stay in Console.
    expect(findItem(result.current, "Plans")?.to).toBe("/console/backups/plans")
    // Cluster-wide admin areas are gone from Console.
    expect(findItem(result.current, "Cluster")).toBeUndefined()
    expect(hasItemTo(result.current, "/console/backups/backupclasses")).toBe(false)
    // Administration moved to the Admin portal.
    expect(sectionIndex(result.current, "Administration")).toBe(-1)
    expect(findItem(result.current, "Info")).toBeUndefined()
    expect(findItem(result.current, "Tenants")).toBeUndefined()
  })
})

describe("useAdminSidebarSections", () => {
  it("always shows Administration, even with no operator permissions", async () => {
    const client = makeClient({ nodes: false, backupclasses: false })
    const { result } = renderHook(() => useAdminSidebarSections(), {
      wrapper: makeWrapper(client),
    })
    // Administration is present immediately — it needs no permission review.
    expect(findItem(result.current, "Tenants")?.to).toBe("/admin/tenants")
    expect(findItem(result.current, "Modules")?.to).toBe("/admin/modules")
    // Per-tenant Info moved into the Tenants tree rows — no sidebar entry.
    expect(findItem(result.current, "Info")).toBeUndefined()
    expect(findItem(result.current, "External IPs")?.to).toBe("/admin/external-ips")
    // No operator area leaks in while the gates deny.
    await waitFor(() => expect(client.create).toHaveBeenCalled())
    expect(findItem(result.current, "Cluster")).toBeUndefined()
    expect(findItem(result.current, "Backup Classes")).toBeUndefined()
  })

  it("shows Cluster Usage and Backup Classes when both gates allow", async () => {
    const client = makeClient({ nodes: true, backupclasses: true })
    const { result } = renderHook(() => useAdminSidebarSections(), {
      wrapper: makeWrapper(client),
    })
    await waitFor(() =>
      expect(findItem(result.current, "Cluster")).toBeDefined(),
    )
    expect(findItem(result.current, "Cluster")?.to).toBe("/admin/capacity/cluster")
    expect(findItem(result.current, "Backup Classes")?.to).toBe(
      "/admin/backups/backupclasses",
    )
    // Administration renders above the gated operator areas.
    const admin = sectionIndex(result.current, "Administration")
    expect(admin).toBe(0)
    expect(admin).toBeLessThan(sectionIndex(result.current, "Capacity"))
    expect(admin).toBeLessThan(sectionIndex(result.current, "Backups"))
  })

  it("shows only Backup Classes when the user lacks nodes/list", async () => {
    const client = makeClient({ nodes: false, backupclasses: true })
    const { result } = renderHook(() => useAdminSidebarSections(), {
      wrapper: makeWrapper(client),
    })
    await waitFor(() =>
      expect(findItem(result.current, "Backup Classes")).toBeDefined(),
    )
    expect(findItem(result.current, "Cluster")).toBeUndefined()
  })

  it("shows only Cluster Usage when the user cannot manage backup classes", async () => {
    const client = makeClient({ nodes: true, backupclasses: false })
    const { result } = renderHook(() => useAdminSidebarSections(), {
      wrapper: makeWrapper(client),
    })
    await waitFor(() =>
      expect(findItem(result.current, "Cluster")).toBeDefined(),
    )
    expect(findItem(result.current, "Backup Classes")).toBeUndefined()
  })
})

describe("useCanSeeAdmin", () => {
  // The Admin tab is always visible now: Administration needs no permission.
  it("is true regardless of operator permissions", () => {
    for (const perms of [
      { nodes: true, backupclasses: true },
      { nodes: true, backupclasses: false },
      { nodes: false, backupclasses: true },
      { nodes: false, backupclasses: false },
    ]) {
      const client = makeClient(perms)
      const { result } = renderHook(() => useCanSeeAdmin(), {
        wrapper: makeWrapper(client),
      })
      expect(result.current).toBe(true)
    }
  })
})
