import { describe, it, expect, vi } from "vitest"
import { renderHook, act } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { K8sClient, K8sProvider, useK8sSubresource } from "@cozystack/k8s-client"
import type { ReactNode } from "react"

const actionRef = {
  apiGroup: "subresources.kubevirt.io",
  apiVersion: "v1",
  plural: "virtualmachines",
  name: "vm-instance-demo",
  namespace: "tenant-root",
}

// The VirtualMachine (with its status) is served under kubevirt.io, not the
// subresources.kubevirt.io aggregated API the action endpoint lives under.
const statusRef = {
  apiGroup: "kubevirt.io",
  apiVersion: "v1",
  plural: "virtualmachines",
  namespace: "tenant-root",
}

function setup(gcTime = 0) {
  const client = new K8sClient()
  vi.spyOn(client, "subresource").mockResolvedValue(undefined)
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime } },
  })
  const invalidateSpy = vi.spyOn(queryClient, "invalidateQueries")
  function wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>
        <K8sProvider client={client} queryClient={queryClient}>
          {children}
        </K8sProvider>
      </QueryClientProvider>
    )
  }
  return { client, queryClient, invalidateSpy, wrapper }
}

describe("useK8sSubresource", () => {
  it("calls the action subresource with a default empty body", async () => {
    const { client, wrapper } = setup()
    const { result } = renderHook(() => useK8sSubresource(actionRef), { wrapper })

    await act(async () => {
      await result.current.mutateAsync({ subresource: "start" })
    })

    expect(client.subresource).toHaveBeenCalledWith(
      "subresources.kubevirt.io",
      "v1",
      "virtualmachines",
      "vm-instance-demo",
      "start",
      "tenant-root",
      {},
      undefined,
    )
  })

  it("invalidates the action ref's own resource key when no invalidate target is given", async () => {
    const { invalidateSpy, wrapper } = setup()
    const { result } = renderHook(() => useK8sSubresource(actionRef), { wrapper })

    await act(async () => {
      await result.current.mutateAsync({ subresource: "start" })
    })

    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: ["k8s", "subresources.kubevirt.io", "v1", "virtualmachines", "tenant-root"],
    })
  })

  it("invalidates the target resource (kubevirt.io) when invalidate is supplied", async () => {
    const { invalidateSpy, wrapper } = setup()
    const { result } = renderHook(
      () => useK8sSubresource(actionRef, { invalidate: statusRef }),
      { wrapper },
    )

    await act(async () => {
      await result.current.mutateAsync({ subresource: "start" })
    })

    // Regression guard: the key must be built from the kubevirt.io status ref,
    // otherwise it never matches the query that holds printableStatus.
    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: ["k8s", "kubevirt.io", "v1", "virtualmachines", "tenant-root"],
    })
    expect(invalidateSpy).not.toHaveBeenCalledWith({
      queryKey: ["k8s", "subresources.kubevirt.io", "v1", "virtualmachines", "tenant-root"],
    })
  })

  it("invalidates a metadata.name field-selected list of the target resource", async () => {
    // gcTime Infinity so the manually seeded (observer-less) query isn't
    // garbage-collected before we read its post-invalidation state.
    const { queryClient, wrapper } = setup(Infinity)
    // The watch-based status read keys its list query with a field-selector.
    // The resource-prefix invalidation must reach it (React Query prefix match),
    // not just an unfiltered list — otherwise the post-action refresh is dead.
    const fieldSelectedListKey = [
      "k8s",
      "kubevirt.io",
      "v1",
      "virtualmachines",
      "tenant-root",
      "",
      "metadata.name=vm-instance-demo",
    ]
    queryClient.setQueryData(fieldSelectedListKey, { items: [] })
    expect(queryClient.getQueryState(fieldSelectedListKey)?.isInvalidated).toBe(false)

    const { result } = renderHook(
      () => useK8sSubresource(actionRef, { invalidate: statusRef }),
      { wrapper },
    )
    await act(async () => {
      await result.current.mutateAsync({ subresource: "start" })
    })

    expect(queryClient.getQueryState(fieldSelectedListKey)?.isInvalidated).toBe(true)
  })
})
