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
  })
})

describe("useAdminSidebarSections", () => {
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
  it("is true when nodes/list is allowed", async () => {
    const client = makeClient({ nodes: true, backupclasses: false })
    const { result } = renderHook(() => useCanSeeAdmin(), {
      wrapper: makeWrapper(client),
    })
    await waitFor(() => expect(result.current).toBe(true))
  })

  it("is true when only backupclasses/update is allowed", async () => {
    const client = makeClient({ nodes: false, backupclasses: true })
    const { result } = renderHook(() => useCanSeeAdmin(), {
      wrapper: makeWrapper(client),
    })
    await waitFor(() => expect(result.current).toBe(true))
  })

  it("is false when neither admin area is allowed", async () => {
    const client = makeClient({ nodes: false, backupclasses: false })
    const { result } = renderHook(() => useCanSeeAdmin(), {
      wrapper: makeWrapper(client),
    })
    await waitFor(() => expect(client.create).toHaveBeenCalled())
    expect(result.current).toBe(false)
  })
})
