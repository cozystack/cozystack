import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, renderHook } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { K8sClient, K8sProvider, useK8sList } from "@cozystack/k8s-client"
import type { K8sResource } from "@cozystack/k8s-client"
import type { ReactNode } from "react"

const ref = { apiGroup: "cdi.kubevirt.io", apiVersion: "v1beta1", plural: "datavolumes", namespace: "tenant-demo" }
const selector = "metadata.name=vm-disk-demo"
const dv: K8sResource = { apiVersion: "cdi.kubevirt.io/v1beta1", kind: "DataVolume", metadata: { name: "vm-disk-demo", namespace: "tenant-demo" } }

function setup() {
  const client = new K8sClient()
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } })
  const streams: ReadableStreamDefaultController<Uint8Array>[] = []
  const requests: { url: URL; signal?: AbortSignal | null }[] = []
  const replies: { listError?: number; watchError?: number; watchPending?: Promise<Response> } = {}
  const fetchSpy = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = new URL(String(input), "https://console.example.org")
    requests.push({ url, signal: init?.signal })
    if (!url.searchParams.has("watch")) {
      if (replies.listError) return Response.json({ message: "list denied" }, { status: replies.listError })
      return Response.json({ apiVersion: dv.apiVersion, kind: "DataVolumeList", metadata: { resourceVersion: "10" }, items: [dv] })
    }
    if (replies.watchPending) return replies.watchPending
    if (replies.watchError) return Response.json({ message: "watch denied" }, { status: replies.watchError })
    return new Response(new ReadableStream<Uint8Array>({
      start(controller) {
        streams.push(controller)
        init?.signal?.addEventListener("abort", () => controller.error(new DOMException("Aborted", "AbortError")), { once: true })
      },
    }))
  })
  function wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={queryClient}><K8sProvider client={client} queryClient={queryClient}>{children}</K8sProvider></QueryClientProvider>
  }
  const counts = () => ({ list: requests.filter(r => !r.url.searchParams.has("watch")).length, watch: requests.filter(r => r.url.searchParams.has("watch")).length })
  return { wrapper, client, queryClient, streams, replies, requests, fetchSpy, counts }
}

async function advance(ms = 0) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms) })
}

beforeEach(() => vi.useFakeTimers())
afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe("useK8sList watch lifecycle", () => {
  it("encodes the exact name selector on the LIST and accepted WATCH", async () => {
    const h = setup()
    const { result } = renderHook(() => useK8sList(ref, { fieldSelector: selector }), { wrapper: h.wrapper })
    await advance()
    expect(h.counts()).toEqual({ list: 1, watch: 1 })
    expect(h.requests.every(r => r.url.searchParams.get("fieldSelector") === selector)).toBe(true)
    expect(h.requests[1].url.searchParams.get("resourceVersion")).toBe("10")
    expect(result.current.watchReady).toBe(true)
  })

  it("waits for accepted response headers before reporting readiness", async () => {
    const h = setup()
    let accept!: (response: Response) => void
    h.replies.watchPending = new Promise(resolve => { accept = resolve })
    const { result } = renderHook(() => useK8sList(ref), { wrapper: h.wrapper })
    await advance()
    expect(h.counts().watch).toBe(1)
    expect(result.current.watchReady).toBe(false)
    accept(new Response(new ReadableStream()))
    await advance()
    expect(result.current.watchReady).toBe(true)
  })

  it("backs off repeated EOFs through 30 seconds even with immediate bookmarks and unchanged RV", async () => {
    const h = setup()
    const { result } = renderHook(() => useK8sList(ref, { fieldSelector: selector }), { wrapper: h.wrapper })
    await advance()
    for (const delay of [1000, 2000, 4000, 8000, 16000, 30000, 30000]) {
      const count = h.counts().watch
      h.streams.at(-1)!.enqueue(new TextEncoder().encode(JSON.stringify({ type: "BOOKMARK", object: { metadata: { resourceVersion: "10" } } }) + "\n"))
      h.streams.at(-1)!.close()
      await advance()
      expect(result.current.watchReady).toBe(false)
      expect(result.current.watchError).toBeDefined()
      await advance(delay - 1)
      expect(h.counts().watch).toBe(count)
      await advance(1)
      expect(h.counts()).toEqual({ list: count + 1, watch: count + 1 })
      expect(result.current.watchReady).toBe(true)
    }
    h.streams.at(-1)!.enqueue(new TextEncoder().encode(JSON.stringify({ type: "MODIFIED", object: { ...dv, status: { phase: "Succeeded" } } }) + "\n"))
    await advance()
    expect(result.current.data?.items[0].status).toEqual({ phase: "Succeeded" })
  })

  it("resets backoff only after the stream has stayed open for 30 seconds", async () => {
    const h = setup()
    renderHook(() => useK8sList(ref), { wrapper: h.wrapper })
    await advance()
    h.streams.at(-1)!.close()
    await advance(1000)
    await advance(30000)
    h.streams.at(-1)!.close()
    await advance(999)
    expect(h.counts().watch).toBe(2)
    await advance(1)
    expect(h.counts().watch).toBe(3)
  })

  it("does not open a stale watch after a failed relist and automatically recovers", async () => {
    const h = setup()
    const { result } = renderHook(() => useK8sList(ref), { wrapper: h.wrapper })
    await advance()
    h.replies.listError = 503
    h.streams[0].close()
    await advance(1000)
    expect(h.counts()).toEqual({ list: 2, watch: 1 })
    expect(result.current.watchReady).toBe(false)
    h.replies.listError = undefined
    await advance(2000)
    expect(h.counts()).toEqual({ list: 3, watch: 2 })
    expect(result.current.watchReady).toBe(true)
  })

  it.each([401, 403, 400])("stops automatic retries on HTTP %s and permits an explicit restart", async (status) => {
    const h = setup()
    h.replies.watchError = status
    const { result } = renderHook(() => useK8sList(ref), { wrapper: h.wrapper })
    await advance()
    expect(result.current.watchReady).toBe(false)
    expect(result.current.watchError).toMatchObject({ status })
    await advance(120000)
    expect(h.counts()).toEqual({ list: 1, watch: 1 })
    h.replies.watchError = undefined
    act(() => { void result.current.restartWatch() })
    await advance()
    expect(h.counts()).toEqual({ list: 2, watch: 2 })
    expect(result.current.watchReady).toBe(true)
  })

  it.each([401, 403, 410, 429, 500])("reads code %s from an ERROR Status object", async (code) => {
    const h = setup()
    const { result } = renderHook(() => useK8sList(ref), { wrapper: h.wrapper })
    await advance()
    h.streams[0].enqueue(new TextEncoder().encode(JSON.stringify({ type: "ERROR", object: { apiVersion: "v1", kind: "Status", code, message: "stream rejected" } }) + "\n"))
    await advance()
    expect(result.current.watchError).toMatchObject({ status: code })
    expect(result.current.watchReady).toBe(false)
    await advance(1000)
    expect(h.counts().watch).toBe(code === 401 || code === 403 ? 1 : 2)
  })

  it("cancels old timers and streams on selector changes, disable and unmount", async () => {
    const h = setup()
    const { result, rerender, unmount } = renderHook(({ name, enabled }) => useK8sList(ref, { fieldSelector: `metadata.name=${name}`, enabled }), { wrapper: h.wrapper, initialProps: { name: "first", enabled: true } })
    await advance()
    h.streams[0].close()
    await advance()
    rerender({ name: "second", enabled: true })
    await advance()
    expect(h.requests[1].signal?.aborted).toBe(true)
    await advance(1000)
    expect(h.counts()).toEqual({ list: 2, watch: 2 })
    expect(h.requests.at(-1)?.url.searchParams.get("fieldSelector")).toBe("metadata.name=second")
    rerender({ name: "second", enabled: false })
    await advance()
    expect(result.current.watchReady).toBe(false)
    expect(h.requests.at(-1)?.signal?.aborted).toBe(true)
    unmount()
    await advance(120000)
    expect(h.counts()).toEqual({ list: 2, watch: 2 })
  })

  it("does not reuse readiness after disabling and enabling a pending watch", async () => {
    const h = setup()
    const { result, rerender } = renderHook(({ enabled }) => useK8sList(ref, { enabled }), { wrapper: h.wrapper, initialProps: { enabled: true } })
    await advance()
    expect(result.current.watchReady).toBe(true)
    rerender({ enabled: false })
    let accept!: (response: Response) => void
    h.replies.watchPending = new Promise(resolve => { accept = resolve })
    rerender({ enabled: true })
    await advance()
    expect(result.current.watchReady).toBe(false)
    expect(h.counts().watch).toBe(2)
    accept(new Response(new ReadableStream()))
    await advance()
    expect(result.current.watchReady).toBe(true)
  })

  it("ignores late acceptance of a watch for the previous namespace", async () => {
    const h = setup()
    let accept!: (response: Response) => void
    h.replies.watchPending = new Promise(resolve => { accept = resolve })
    const { result, rerender } = renderHook(({ namespace }) => useK8sList({ ...ref, namespace }), { wrapper: h.wrapper, initialProps: { namespace: "tenant-first" } })
    await advance()
    h.replies.watchPending = undefined
    h.replies.watchError = 403
    rerender({ namespace: "tenant-second" })
    await advance()
    expect(h.requests[1].signal?.aborted).toBe(true)
    expect(result.current.watchError).toMatchObject({ status: 403 })
    accept(new Response(new ReadableStream()))
    await advance()
    expect(result.current.watchReady).toBe(false)
    expect(result.current.watchError).toMatchObject({ status: 403 })
    expect(h.requests.at(-1)?.url.pathname).toContain("/namespaces/tenant-second/")
  })

  it.each([401, 403])("stops automatic recovery when the relist returns %s", async (status) => {
    const h = setup()
    const { result } = renderHook(() => useK8sList(ref), { wrapper: h.wrapper })
    await advance()
    h.replies.listError = status
    h.streams[0].close()
    await advance(1000)
    expect(result.current.watchError).toMatchObject({ status })
    await advance(120000)
    expect(h.counts()).toEqual({ list: 2, watch: 1 })
  })

  it("does not open a stream from a relist finishing after unmount", async () => {
    const h = setup()
    const { result, unmount } = renderHook(() => useK8sList(ref), { wrapper: h.wrapper })
    await advance()
    let finish!: (response: Response) => void
    h.fetchSpy.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    act(() => { void result.current.restartWatch() })
    await advance()
    unmount()
    finish(Response.json({ metadata: { resourceVersion: "20" }, items: [] }))
    await advance(120000)
    expect(h.counts().watch).toBe(1)
  })

  it("preserves React Query property tracking when exposing watch status", async () => {
    const h = setup()
    let renders = 0
    const { result } = renderHook(() => {
      const query = useK8sList(ref, { watch: false })
      renders++
      return { data: query.data, refetch: query.refetch }
    }, { wrapper: h.wrapper })
    await advance()
    const settledRenders = renders
    await act(async () => { await result.current.refetch() })
    await advance()
    expect(renders).toBe(settledRenders)
  })
})
