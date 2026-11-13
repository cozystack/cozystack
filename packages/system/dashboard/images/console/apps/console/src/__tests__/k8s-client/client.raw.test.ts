import { describe, it, expect, vi, afterEach } from "vitest"
import { K8sClient } from "@cozystack/k8s-client"

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("K8sClient.raw", () => {
  it("returns the response untouched, including a non-2xx one", async () => {
    const res = new Response("not found", { status: 404 })
    vi.stubGlobal("fetch", vi.fn(async () => res))
    const client = new K8sClient()

    await expect(client.raw("/x")).resolves.toBe(res)
  })

  it("prefixes the base URL and attaches the bearer token", async () => {
    const fetchMock = vi.fn(async () => new Response("{}"))
    vi.stubGlobal("fetch", fetchMock)
    const client = new K8sClient({ baseUrl: "/base", getToken: async () => "t0k" })

    await client.raw("/apis/x", { headers: { Accept: "application/json" } })

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe("/base/apis/x")
    const headers = new Headers(init.headers)
    expect(headers.get("Accept")).toBe("application/json")
    expect(headers.get("Authorization")).toBe("Bearer t0k")
  })

  it.each([
    ["a Headers instance", new Headers({ Accept: "application/x-ndjson" })],
    ["a tuple list", [["Accept", "application/x-ndjson"]] as [string, string][]],
  ])("keeps caller headers passed as %s", async (_, given) => {
    const fetchMock = vi.fn(async () => new Response("{}"))
    vi.stubGlobal("fetch", fetchMock)
    const client = new K8sClient({ getToken: async () => "t0k" })

    await client.raw("/x", { headers: given })

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    const headers = new Headers(init.headers)
    expect(headers.get("Accept")).toBe("application/x-ndjson")
    expect(headers.get("Authorization")).toBe("Bearer t0k")
  })

  it("routes a 401 through the client's unauthorized handler", async () => {
    const onUnauthorized = vi.fn()
    vi.stubGlobal("fetch", vi.fn(async () => new Response("", { status: 401 })))
    const client = new K8sClient({ onUnauthorized })

    const res = await client.raw("/x")

    expect(res.status).toBe(401)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it("does not swallow a network failure", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => { throw new TypeError("offline") }))
    const client = new K8sClient()

    await expect(client.raw("/x")).rejects.toThrow("offline")
  })
})
