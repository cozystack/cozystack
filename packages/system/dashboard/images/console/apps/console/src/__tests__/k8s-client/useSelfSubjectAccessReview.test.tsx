import { describe, it, expect, vi } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  K8sClient,
  K8sProvider,
  useSelfSubjectAccessReview,
  type SelfSubjectAccessReview,
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

function ssarResult(allowed: boolean): SelfSubjectAccessReview {
  return {
    apiVersion: "authorization.k8s.io/v1",
    kind: "SelfSubjectAccessReview",
    metadata: { name: "" },
    spec: { resourceAttributes: { resource: "nodes", verb: "list" } },
    status: { allowed },
  }
}

describe("useSelfSubjectAccessReview", () => {
  it("starts in loading state with allowed=false", () => {
    const client = new K8sClient()
    vi.spyOn(client, "create").mockImplementation(() => new Promise(() => {}))
    const { result } = renderHook(
      () =>
        useSelfSubjectAccessReview({
          resourceAttributes: { resource: "nodes", verb: "list" },
        }),
      { wrapper: makeWrapper(client) },
    )
    expect(result.current.isLoading).toBe(true)
    expect(result.current.allowed).toBe(false)
  })

  it("reports allowed=true when the API responds with status.allowed=true", async () => {
    const client = new K8sClient()
    vi.spyOn(client, "create").mockResolvedValue(ssarResult(true))
    const { result } = renderHook(
      () =>
        useSelfSubjectAccessReview({
          resourceAttributes: { resource: "nodes", verb: "list" },
        }),
      { wrapper: makeWrapper(client) },
    )
    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(result.current.allowed).toBe(true)
  })

  it("reports allowed=false explicitly when status.allowed=false", async () => {
    const client = new K8sClient()
    vi.spyOn(client, "create").mockResolvedValue(ssarResult(false))
    const { result } = renderHook(
      () =>
        useSelfSubjectAccessReview({
          resourceAttributes: { resource: "nodes", verb: "list" },
        }),
      { wrapper: makeWrapper(client) },
    )
    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(result.current.allowed).toBe(false)
  })

  it("POSTs once for two consumers asking the same question", async () => {
    const client = new K8sClient()
    const spy = vi.spyOn(client, "create").mockResolvedValue(ssarResult(true))
    const Wrapper = makeWrapper(client)
    const { result: a } = renderHook(
      () =>
        useSelfSubjectAccessReview({
          resourceAttributes: { resource: "nodes", verb: "list" },
        }),
      { wrapper: Wrapper },
    )
    const { result: b } = renderHook(
      () =>
        useSelfSubjectAccessReview({
          resourceAttributes: { resource: "nodes", verb: "list" },
        }),
      { wrapper: Wrapper },
    )
    await waitFor(() => expect(a.current.isLoading).toBe(false))
    await waitFor(() => expect(b.current.isLoading).toBe(false))
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it("POSTs twice when two consumers ask different questions", async () => {
    const client = new K8sClient()
    const spy = vi.spyOn(client, "create").mockResolvedValue(ssarResult(true))
    const Wrapper = makeWrapper(client)
    const { result: a } = renderHook(
      () =>
        useSelfSubjectAccessReview({
          resourceAttributes: { resource: "nodes", verb: "list" },
        }),
      { wrapper: Wrapper },
    )
    const { result: b } = renderHook(
      () =>
        useSelfSubjectAccessReview({
          resourceAttributes: { resource: "pods", verb: "list" },
        }),
      { wrapper: Wrapper },
    )
    await waitFor(() => expect(a.current.isLoading).toBe(false))
    await waitFor(() => expect(b.current.isLoading).toBe(false))
    expect(spy).toHaveBeenCalledTimes(2)
  })

  it("surfaces the error and reports allowed=false on API failure", async () => {
    const client = new K8sClient()
    const err = new Error("server error")
    vi.spyOn(client, "create").mockRejectedValue(err)
    const { result } = renderHook(
      () =>
        useSelfSubjectAccessReview({
          resourceAttributes: { resource: "nodes", verb: "list" },
        }),
      { wrapper: makeWrapper(client) },
    )
    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(result.current.allowed).toBe(false)
    expect(result.current.error).toBeTruthy()
  })

  it("sends the spec verbatim in the POST body", async () => {
    const client = new K8sClient()
    const spy = vi.spyOn(client, "create").mockResolvedValue(ssarResult(true))
    const { result } = renderHook(
      () =>
        useSelfSubjectAccessReview({
          resourceAttributes: {
            group: "metrics.k8s.io",
            resource: "nodes",
            verb: "list",
          },
        }),
      { wrapper: makeWrapper(client) },
    )
    await waitFor(() => expect(result.current.isLoading).toBe(false))
    expect(spy).toHaveBeenCalledWith(
      "authorization.k8s.io",
      "v1",
      "selfsubjectaccessreviews",
      expect.objectContaining({
        kind: "SelfSubjectAccessReview",
        apiVersion: "authorization.k8s.io/v1",
        spec: {
          resourceAttributes: {
            group: "metrics.k8s.io",
            resource: "nodes",
            verb: "list",
          },
        },
      }),
    )
  })
})
