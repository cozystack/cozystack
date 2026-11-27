import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, renderHook } from "@testing-library/react"
import { QueryClient, QueryClientProvider, useQueryClient, type DefaultOptions } from "@tanstack/react-query"
import { K8sClient, K8sProvider, useK8sList } from "@cozystack/k8s-client"
import type { K8sResource } from "@cozystack/k8s-client"
import type { ReactNode } from "react"

const ref = { apiGroup: "cdi.kubevirt.io", apiVersion: "v1beta1", plural: "datavolumes", namespace: "tenant-demo" }
const selector = "metadata.name=vm-disk-demo"
const dv: K8sResource = { apiVersion: "cdi.kubevirt.io/v1beta1", kind: "DataVolume", metadata: { name: "vm-disk-demo", namespace: "tenant-demo" } }

function productionQueryDefaults() {
  const { result, unmount } = renderHook(() => useQueryClient().getDefaultOptions(), { wrapper: K8sProvider })
  const defaults = result.current
  unmount()
  return defaults
}

function setup(defaultOptions: DefaultOptions = { queries: { retry: false, gcTime: Infinity } }) {
  const client = new K8sClient()
  const queryClient = new QueryClient({ defaultOptions })
  const streams: ReadableStreamDefaultController<Uint8Array>[] = []
  const requests: { url: URL; signal?: AbortSignal | null }[] = []
  const replies: { listPending?: Promise<Response>; listError?: number; watchError?: number; watchPending?: Promise<Response> } = {}
  const fetchSpy = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = new URL(String(input), "https://console.example.org")
    requests.push({ url, signal: init?.signal })
    if (!url.searchParams.has("watch")) {
      if (replies.listPending) return replies.listPending
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

  it.each([
    { change: "namespace", resourceVersion: "10" },
    { change: "namespace", resourceVersion: "20" },
    { change: "selector", resourceVersion: "10" },
    { change: "selector", resourceVersion: "20" },
  ])("starts the $change watch after production placeholder data is replaced at RV $resourceVersion", async ({ change, resourceVersion }) => {
    const h = setup(productionQueryDefaults())
    const watchSpy = vi.spyOn(h.client, "watch")
    const initialProps = { namespace: ref.namespace, fieldSelector: selector }
    const { result, rerender } = renderHook(
      ({ namespace, fieldSelector }) => useK8sList({ ...ref, namespace }, { fieldSelector }),
      { wrapper: h.wrapper, initialProps },
    )
    await advance()
    expect(h.counts()).toEqual({ list: 1, watch: 1 })
    const previousCallback = watchSpy.mock.calls[0][5]
    let finish!: (response: Response) => void
    h.replies.listPending = new Promise(resolve => { finish = resolve })
    const next = change === "namespace"
      ? { namespace: "tenant-second", fieldSelector: selector }
      : { namespace: ref.namespace, fieldSelector: "metadata.name=vm-disk-second" }
    const nextDisk = {
      ...dv,
      metadata: { name: change === "namespace" ? dv.metadata.name : "vm-disk-second", namespace: next.namespace },
    }
    rerender(next)
    await advance()
    expect(result.current.isPlaceholderData).toBe(true)
    expect(result.current.data?.items).toEqual([dv])
    expect(result.current.watchReady).toBe(false)
    expect(h.requests[1].signal?.aborted).toBe(true)
    expect(h.counts()).toEqual({ list: 2, watch: 1 })

    finish(Response.json({ metadata: { resourceVersion }, items: [nextDisk] }))
    await advance()
    expect(result.current.isPlaceholderData).toBe(false)
    expect(result.current.data?.items).toEqual([nextDisk])
    expect(h.counts()).toEqual({ list: 2, watch: 2 })
    expect(h.requests.at(-1)?.url.pathname).toContain(`/namespaces/${next.namespace}/`)
    expect(h.requests.at(-1)?.url.searchParams.get("fieldSelector")).toBe(next.fieldSelector)
    expect(h.requests.at(-1)?.url.searchParams.get("resourceVersion")).toBe(resourceVersion)
    expect(result.current.watchReady).toBe(true)

    h.streams[1].enqueue(new TextEncoder().encode(JSON.stringify({ type: "MODIFIED", object: { ...nextDisk, status: { phase: "Succeeded" } } }) + "\n"))
    await advance()
    expect(result.current.data?.items[0].status).toEqual({ phase: "Succeeded" })
    act(() => { previousCallback({ type: "MODIFIED", object: { ...nextDisk, status: { phase: "Failed" } } }) })
    await advance()
    expect(result.current.data?.items[0].status).toEqual({ phase: "Succeeded" })
    expect(h.counts()).toEqual({ list: 2, watch: 2 })
  })

  it("keeps same-name objects in different namespaces separate during watch updates", async () => {
    const h = setup()
    const { result } = renderHook(() => useK8sList({ ...ref, namespace: undefined }), { wrapper: h.wrapper })
    await advance()
    const otherTenant = { ...dv, metadata: { ...dv.metadata, namespace: "tenant-second" } }
    const otherName = { ...dv, metadata: { ...dv.metadata, name: "vm-disk-other" } }
    async function emit(type: string, object: K8sResource) {
      h.streams[0].enqueue(new TextEncoder().encode(JSON.stringify({ type, object }) + "\n"))
      await advance()
    }
    await emit("ADDED", otherTenant)
    await emit("ADDED", otherName)
    expect(result.current.data?.items).toEqual([dv, otherTenant, otherName])
    const completed = { ...otherTenant, status: { phase: "Succeeded" } }
    await emit("MODIFIED", completed)
    expect(result.current.data?.items).toEqual([dv, completed, otherName])
    await emit("DELETED", otherName)
    expect(result.current.data?.items).toEqual([dv, completed])
    await emit("DELETED", otherName)
    expect(result.current.data?.items).toEqual([dv, completed])
  })

  it("ignores callbacks from a retired stream after reconnecting the same query", async () => {
    const h = setup()
    const watchSpy = vi.spyOn(h.client, "watch")
    const { result } = renderHook(() => useK8sList(ref), { wrapper: h.wrapper })
    await advance()
    const retired = watchSpy.mock.calls[0]
    h.streams[0].close()
    await advance(1000)
    const completed = { ...dv, status: { phase: "Succeeded" } }
    h.streams[1].enqueue(new TextEncoder().encode(JSON.stringify({ type: "MODIFIED", object: completed }) + "\n"))
    await advance()
    act(() => {
      retired[5]({ type: "MODIFIED", object: { ...dv, status: { phase: "UploadReady" } } })
      retired[6]?.(new Error("retired stream failed"))
      retired[7]?.onOpen?.()
    })
    await advance()
    expect(result.current.data?.items).toEqual([completed])
    expect(result.current.watchReady).toBe(true)
    expect(result.current.watchError).toBeUndefined()
    expect(h.counts()).toEqual({ list: 2, watch: 2 })
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

  it("opens no stream and never reports readiness when watch is false", async () => {
    const h = setup()
    const { result } = renderHook(() => useK8sList(ref, { watch: false }), { wrapper: h.wrapper })
    await advance(120000)
    expect(h.counts()).toEqual({ list: 1, watch: 0 })
    expect(result.current.watchReady).toBe(false)
    expect(result.current.data?.items).toHaveLength(1)
  })

  it("does not open a stream from a relist that carries no resourceVersion", async () => {
    const h = setup()
    const { result } = renderHook(() => useK8sList(ref), { wrapper: h.wrapper })
    await advance()
    h.streams[0].close()
    h.fetchSpy.mockImplementationOnce(async () => Response.json({ metadata: {}, items: [] }))
    await advance(1000)
    expect(result.current.watchReady).toBe(false)
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

describe("useK8sList refetch and watch ordering", () => {
  const encode = (type: string, object: K8sResource) =>
    new TextEncoder().encode(JSON.stringify({ type, object }) + "\n")

  async function staleListAfter(type: "MODIFIED" | "DELETED") {
    const h = setup()
    const { result } = renderHook(() => useK8sList(ref, { fieldSelector: selector }), { wrapper: h.wrapper })
    await advance()
    expect(h.counts()).toEqual({ list: 1, watch: 1 })

    let finish!: (response: Response) => void
    h.replies.listPending = new Promise(resolve => { finish = resolve })
    act(() => { void result.current.refetch() })
    await advance()
    const newer = { ...dv, metadata: { ...dv.metadata, resourceVersion: "12" }, status: { phase: "Succeeded" } }
    h.streams[0].enqueue(encode(type, newer))
    await advance()
    expect(result.current.data?.items).toEqual(type === "DELETED" ? [] : [newer])

    h.replies.listPending = undefined
    finish(Response.json({ metadata: { resourceVersion: "11" }, items: [{ ...dv, status: { phase: "UploadReady" } }] }))
    await advance()
    expect(h.counts()).toEqual({ list: 2, watch: 2 })
    expect(h.requests[1].signal?.aborted).toBe(true)
    expect(h.requests.at(-1)?.url.searchParams.get("resourceVersion")).toBe("11")
    expect(h.requests.at(-1)?.url.searchParams.get("fieldSelector")).toBe(selector)
    expect(result.current.watchReady).toBe(true)

    h.streams[1].enqueue(encode(type, newer))
    await advance()
    return result.current.data?.items
  }

  it("resumes from a stale LIST's resourceVersion so a newer update is replayed", async () => {
    const items = await staleListAfter("MODIFIED")
    expect(items?.map(i => i.status)).toEqual([{ phase: "Succeeded" }])
  })

  it("resumes from a stale LIST's resourceVersion so a deleted object does not come back", async () => {
    expect(await staleListAfter("DELETED")).toEqual([])
  })

  it("opens one stream at the resourceVersion a relist returns after a disconnect", async () => {
    const h = setup()
    renderHook(() => useK8sList(ref, { fieldSelector: selector }), { wrapper: h.wrapper })
    await advance()
    h.replies.listPending = Promise.resolve(Response.json({ metadata: { resourceVersion: "20" }, items: [dv] }))
    h.streams[0].close()
    await advance(1000)
    h.replies.listPending = undefined
    await advance(60000)
    expect(h.counts()).toEqual({ list: 2, watch: 2 })
    expect(h.requests.at(-1)?.url.searchParams.get("resourceVersion")).toBe("20")
    expect(h.requests.at(-1)?.signal?.aborted).toBe(false)
  })

  it("replays from the stream's own resourceVersion when a LIST at it lands after newer events", async () => {
    const h = setup()
    const { result } = renderHook(() => useK8sList(ref, { fieldSelector: selector }), { wrapper: h.wrapper })
    await advance()
    const succeeded = { ...dv, metadata: { ...dv.metadata, resourceVersion: "11" }, status: { phase: "Succeeded" } }
    h.streams[0].enqueue(encode("MODIFIED", succeeded))
    await advance()
    h.replies.listPending = Promise.resolve(Response.json({ metadata: { resourceVersion: "10" }, items: [{ ...dv, status: { phase: "UploadReady" } }] }))
    await act(async () => { await result.current.refetch() })
    await advance()
    expect(h.counts()).toEqual({ list: 2, watch: 2 })
    expect(h.requests[1].signal?.aborted).toBe(true)
    expect(h.requests.at(-1)?.url.searchParams.get("resourceVersion")).toBe("10")
    h.streams[1].enqueue(encode("MODIFIED", succeeded))
    await advance()
    expect(result.current.data?.items.map(i => i.status)).toEqual([{ phase: "Succeeded" }])
  })

  it("keeps the stream when only watch events arrive", async () => {
    const h = setup()
    const { result } = renderHook(() => useK8sList(ref, { fieldSelector: selector }), { wrapper: h.wrapper })
    await advance()
    h.streams[0].enqueue(encode("MODIFIED", { ...dv, status: { phase: "UploadReady" } }))
    h.streams[0].enqueue(encode("DELETED", dv))
    await advance()
    expect(result.current.data?.items).toEqual([])
    expect(h.counts()).toEqual({ list: 1, watch: 1 })
    expect(h.requests[1].signal?.aborted).toBe(false)
  })

  it("keeps the stream when a refetch returns the stream's resourceVersion before any event", async () => {
    const h = setup()
    const { result } = renderHook(() => useK8sList(ref, { fieldSelector: selector }), { wrapper: h.wrapper })
    await advance()
    h.replies.listPending = Promise.resolve(Response.json({ metadata: { resourceVersion: "10" }, items: [{ ...dv, status: { phase: "UploadReady" } }] }))
    await act(async () => { await result.current.refetch() })
    await advance()
    expect(h.counts()).toEqual({ list: 2, watch: 1 })
    expect(h.requests[1].signal?.aborted).toBe(false)
    expect(result.current.watchReady).toBe(true)
  })

  it("reports no readiness until the reopened stream is accepted", async () => {
    const h = setup()
    const { result } = renderHook(() => useK8sList(ref, { fieldSelector: selector }), { wrapper: h.wrapper })
    await advance()
    expect(result.current.watchReady).toBe(true)
    let accept!: (response: Response) => void
    h.replies.watchPending = new Promise(resolve => { accept = resolve })
    h.replies.listPending = Promise.resolve(Response.json({ metadata: { resourceVersion: "11" }, items: [dv] }))
    await act(async () => { await result.current.refetch() })
    await advance(120000)
    expect(h.counts()).toEqual({ list: 2, watch: 2 })
    expect(h.requests[1].signal?.aborted).toBe(true)
    expect(result.current.watchReady).toBe(false)
    expect(result.current.watchError).toBeUndefined()
    accept(new Response(new ReadableStream<Uint8Array>()))
    await advance()
    expect(result.current.watchReady).toBe(true)
  })

  it("does not reopen either stream when two lists on one key receive the same events at different times", async () => {
    const h = setup()
    const { result } = renderHook(() => ({
      first: useK8sList(ref, { fieldSelector: selector }),
      second: useK8sList(ref, { fieldSelector: selector }),
    }), { wrapper: h.wrapper })
    await advance()
    expect(h.counts()).toEqual({ list: 1, watch: 2 })
    for (const [rv, phase] of [["11", "ImportInProgress"], ["12", "Succeeded"]]) {
      const event = encode("MODIFIED", { ...dv, metadata: { ...dv.metadata, resourceVersion: rv }, status: { phase } })
      h.streams[0].enqueue(event)
      await advance()
      h.streams[1].enqueue(event)
      await advance(1000)
    }
    expect(h.counts()).toEqual({ list: 1, watch: 2 })
    expect(result.current.first.data?.items.map(i => i.status)).toEqual([{ phase: "Succeeded" }])
    expect(result.current.first.watchReady && result.current.second.watchReady).toBe(true)
  })

  it.each([1, 2])("replays a stale LIST's missed update when another event lands before a render, with %i lists on the key", async (instances) => {
    const h = setup()
    const { result } = renderHook(
      () => Array.from({ length: instances }, () => useK8sList(ref, { fieldSelector: selector })),
      { wrapper: h.wrapper },
    )
    await advance()
    expect(h.counts()).toEqual({ list: 1, watch: instances })
    const watches = () => h.requests.filter(r => r.url.searchParams.has("watch"))
    const succeeded = { ...dv, metadata: { ...dv.metadata, resourceVersion: "12" }, status: { phase: "Succeeded" } }
    const other = { ...dv, metadata: { name: "vm-disk-other", namespace: "tenant-demo", resourceVersion: "13" } }

    let finish!: (response: Response) => void
    h.replies.listPending = new Promise(resolve => { finish = resolve })
    act(() => { void result.current[0].refetch() })
    await advance()
    for (const stream of h.streams) stream.enqueue(encode("MODIFIED", succeeded))
    await advance()
    expect(result.current[0].data?.items).toEqual([succeeded])

    const unsubscribe = h.queryClient.getQueryCache().subscribe((event) => {
      if (event.type !== "updated" || event.action.type !== "success" || event.action.manual) return
      h.streams.forEach((stream, i) => { if (!watches()[i].signal?.aborted) stream.enqueue(encode("ADDED", other)) })
    })
    h.replies.listPending = undefined
    finish(Response.json({ metadata: { resourceVersion: "11" }, items: [{ ...dv, status: { phase: "UploadReady" } }] }))
    await advance()
    unsubscribe()

    expect(h.counts()).toEqual({ list: 2, watch: 2 * instances })
    expect(watches().slice(instances).map(r => r.url.searchParams.get("resourceVersion"))).toEqual(Array(instances).fill("11"))
    for (const stream of h.streams.slice(instances)) {
      stream.enqueue(encode("MODIFIED", succeeded))
      stream.enqueue(encode("ADDED", other))
    }
    await advance()
    expect(result.current[0].data?.items.map(i => [i.metadata.name, i.status])).toEqual([
      ["vm-disk-demo", { phase: "Succeeded" }],
      ["vm-disk-other", undefined],
    ])
  })

  it("ignores a LIST landing on another key and stops listening once unmounted", async () => {
    const h = setup()
    const cache = h.queryClient.getQueryCache()
    const { unmount } = renderHook(() => useK8sList(ref, { fieldSelector: selector }), { wrapper: h.wrapper })
    await advance()
    h.streams[0].enqueue(encode("MODIFIED", { ...dv, status: { phase: "Succeeded" } }))
    await advance()
    await act(async () => {
      await h.queryClient.fetchQuery({ queryKey: ["unrelated"], queryFn: async () => ({ metadata: { resourceVersion: "99" }, items: [] }) })
    })
    await advance()
    expect(h.counts()).toEqual({ list: 1, watch: 1 })
    expect(h.requests[1].signal?.aborted).toBe(false)
    expect(cache.hasListeners()).toBe(true)
    unmount()
    expect(cache.hasListeners()).toBe(false)
  })

  it("keeps a reopened stream when a later LIST at its resourceVersion lands before any new event", async () => {
    const h = setup()
    const { result } = renderHook(() => useK8sList(ref, { fieldSelector: selector }), { wrapper: h.wrapper })
    await advance()
    h.streams[0].enqueue(encode("MODIFIED", { ...dv, status: { phase: "Succeeded" } }))
    await advance()
    h.replies.listPending = Promise.resolve(Response.json({ metadata: { resourceVersion: "10" }, items: [{ ...dv, status: { phase: "UploadReady" } }] }))
    await act(async () => { await result.current.refetch() })
    await advance()
    expect(h.counts()).toEqual({ list: 2, watch: 2 })
    h.replies.listPending = Promise.resolve(Response.json({ metadata: { resourceVersion: "10" }, items: [] }))
    await act(async () => { await result.current.refetch() })
    await advance()
    expect(h.counts()).toEqual({ list: 3, watch: 2 })
    expect(h.requests.filter(r => r.url.searchParams.has("watch")).at(-1)?.signal?.aborted).toBe(false)
  })

  it("does not carry the previous key's watch error into a stream a refetch reopens", async () => {
    const h = setup()
    h.replies.watchError = 403
    const { result, rerender } = renderHook(
      ({ namespace }) => useK8sList({ ...ref, namespace }, { fieldSelector: selector }),
      { wrapper: h.wrapper, initialProps: { namespace: ref.namespace } },
    )
    await advance()
    expect(result.current.watchError).toBeInstanceOf(Error)
    h.replies.watchError = undefined
    h.replies.watchPending = new Promise(() => {})
    rerender({ namespace: "tenant-second" })
    await advance()
    expect(result.current.watchError).toBeUndefined()
    h.replies.listPending = Promise.resolve(Response.json({ metadata: { resourceVersion: "11" }, items: [dv] }))
    await act(async () => { await result.current.refetch() })
    await advance()
    expect(h.counts().watch).toBe(3)
    expect(result.current.watchReady).toBe(false)
    expect(result.current.watchError).toBeUndefined()
  })

  it("keeps a terminal watch error until a stream reopened by a refetch is accepted", async () => {
    const h = setup()
    h.replies.watchError = 403
    const { result } = renderHook(() => useK8sList(ref, { fieldSelector: selector }), { wrapper: h.wrapper })
    await advance()
    expect(result.current.watchError).toBeInstanceOf(Error)
    h.replies.watchError = undefined
    let accept!: (response: Response) => void
    h.replies.watchPending = new Promise(resolve => { accept = resolve })
    h.replies.listPending = Promise.resolve(Response.json({ metadata: { resourceVersion: "11" }, items: [dv] }))
    await act(async () => { await result.current.refetch() })
    await advance()
    expect(h.counts()).toEqual({ list: 2, watch: 2 })
    expect(result.current.watchReady).toBe(false)
    expect(result.current.watchError).toBeInstanceOf(Error)
    accept(new Response(new ReadableStream<Uint8Array>()))
    await advance()
    expect(result.current.watchReady).toBe(true)
    expect(result.current.watchError).toBeUndefined()
  })
})
