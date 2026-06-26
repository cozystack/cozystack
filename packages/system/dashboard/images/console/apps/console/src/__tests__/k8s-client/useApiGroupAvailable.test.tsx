import { describe, it, expect, vi } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  K8sClient,
  K8sProvider,
  useApiGroupAvailable,
  type APIGroupList,
} from "@cozystack/k8s-client"
import type { ReactNode } from "react"

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

const sampleGroups: APIGroupList = {
  kind: "APIGroupList",
  apiVersion: "v1",
  groups: [
    {
      name: "metrics.k8s.io",
      versions: [{ groupVersion: "metrics.k8s.io/v1beta1", version: "v1beta1" }],
      preferredVersion: { groupVersion: "metrics.k8s.io/v1beta1", version: "v1beta1" },
    },
    {
      name: "apps",
      versions: [{ groupVersion: "apps/v1", version: "v1" }],
      preferredVersion: { groupVersion: "apps/v1", version: "v1" },
    },
  ],
}

describe("useApiGroupAvailable", () => {
  it("starts in loading state with available=false", () => {
    const client = new K8sClient()
    vi.spyOn(client, "getApiGroups").mockImplementation(
      () => new Promise(() => {}),
    )
    const { result } = renderHook(() => useApiGroupAvailable("metrics.k8s.io"), {
      wrapper: makeWrapper(client),
    })
    expect(result.current.isLoading).toBe(true)
    expect(result.current.available).toBe(false)
  })

  it("reports available=true when the group is present", async () => {
    const client = new K8sClient()
    vi.spyOn(client, "getApiGroups").mockResolvedValue(sampleGroups)
    const { result } = renderHook(() => useApiGroupAvailable("metrics.k8s.io"), {
      wrapper: makeWrapper(client),
    })
    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(result.current.available).toBe(true)
  })

  it("reports available=false when the group is missing", async () => {
    const client = new K8sClient()
    vi.spyOn(client, "getApiGroups").mockResolvedValue(sampleGroups)
    const { result } = renderHook(() => useApiGroupAvailable("custom.metrics.k8s.io"), {
      wrapper: makeWrapper(client),
    })
    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(result.current.available).toBe(false)
  })

  it("fetches /apis once for multiple consumers", async () => {
    const client = new K8sClient()
    const spy = vi.spyOn(client, "getApiGroups").mockResolvedValue(sampleGroups)
    const Wrapper = makeWrapper(client)

    function Twin() {
      const a = useApiGroupAvailable("metrics.k8s.io")
      const b = useApiGroupAvailable("apps")
      return (
        <p>
          {String(a.available)}-{String(b.available)}
        </p>
      )
    }

    const { result: hookA } = renderHook(
      () => useApiGroupAvailable("metrics.k8s.io"),
      { wrapper: Wrapper },
    )
    const { result: hookB } = renderHook(
      () => useApiGroupAvailable("apps"),
      { wrapper: Wrapper },
    )

    await waitFor(() => expect(hookA.current.isLoading).toBe(false))
    await waitFor(() => expect(hookB.current.isLoading).toBe(false))

    // Both hooks share the same provider and cache, so /apis is called
    // exactly once for the lifetime of this provider tree. Twin is unused
    // here but kept declared to document the multi-consumer shape we
    // protect against.
    expect(spy).toHaveBeenCalledTimes(1)
    void Twin
  })

  it("surfaces an error and reports available=false", async () => {
    const client = new K8sClient()
    vi.spyOn(client, "getApiGroups").mockRejectedValue(new Error("no /apis"))
    const { result } = renderHook(() => useApiGroupAvailable("metrics.k8s.io"), {
      wrapper: makeWrapper(client),
    })
    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(result.current.available).toBe(false)
  })
})
