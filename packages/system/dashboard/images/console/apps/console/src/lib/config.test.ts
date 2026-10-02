import { describe, it, expect, vi, afterEach } from "vitest"
import { loadConfig } from "./config.ts"

type Route = (url: string) => Response | undefined

function mockFetch(route: Route) {
  const spy = vi.fn(async (input: string | URL) => {
    const url = typeof input === "string" ? input : input.toString()
    const resp = route(url)
    if (!resp) throw new Error(`network error: ${url}`)
    return resp
  })
  vi.stubGlobal("fetch", spy)
  return spy
}

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })
}

const STATIC = "/branding/config.json"
const API = "/api/v1/namespaces/cozy-dashboard/configmaps/cozy-dashboard-console-config"

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("loadConfig", () => {
  it("reads branding from the static asset without touching the kube-api", async () => {
    const fetchSpy = mockFetch((url) =>
      url === STATIC ? json({ titleText: "Acme", logoText: "Acme Cloud" }) : undefined,
    )
    const cfg = await loadConfig()
    expect(cfg).toEqual({ titleText: "Acme", logoText: "Acme Cloud" })
    expect(fetchSpy).toHaveBeenCalledTimes(1)
    expect(fetchSpy.mock.calls[0][0]).toBe(STATIC)
  })

  it("uses the static asset even when branding is empty, never falling through to the kube-api", async () => {
    const fetchSpy = mockFetch((url) => (url === STATIC ? json({}) : json({ data: {} })))
    const cfg = await loadConfig()
    expect(cfg).toEqual({})
    expect(fetchSpy.mock.calls.every((c) => c[0] === STATIC)).toBe(true)
  })

  it("falls back to the kube-api ConfigMap when the static asset is missing", async () => {
    mockFetch((url) => {
      if (url === STATIC) return new Response("not found", { status: 404 })
      if (url === API) return json({ data: { "config.json": JSON.stringify({ titleText: "Fallback" }) } })
      return undefined
    })
    const cfg = await loadConfig()
    expect(cfg).toEqual({ titleText: "Fallback" })
  })

  it("falls back to the kube-api when the static path serves the SPA index.html", async () => {
    mockFetch((url) => {
      // A chart without the mount serves index.html (HTML, not JSON) at 200.
      if (url === STATIC) return new Response("<!doctype html><html></html>", { status: 200 })
      if (url === API) return json({ data: { "config.json": JSON.stringify({ logoText: "FromApi" }) } })
      return undefined
    })
    const cfg = await loadConfig()
    expect(cfg).toEqual({ logoText: "FromApi" })
  })

  it("ignores a JSON body served at a non-200 status and falls back", async () => {
    // A branded-looking error body must not be mistaken for config.
    mockFetch((url) => {
      if (url === STATIC) return new Response(JSON.stringify({ titleText: "Error" }), { status: 503 })
      if (url === API) return json({ data: { "config.json": JSON.stringify({ titleText: "FromApi" }) } })
      return undefined
    })
    const cfg = await loadConfig()
    expect(cfg).toEqual({ titleText: "FromApi" })
  })

  it("ignores a JSON scalar at 200 and falls back", async () => {
    // typeof 5 !== "object": a scalar body is not a config object.
    mockFetch((url) => {
      if (url === STATIC) return json(5)
      if (url === API) return json({ data: { "config.json": JSON.stringify({ titleText: "FromApi" }) } })
      return undefined
    })
    const cfg = await loadConfig()
    expect(cfg).toEqual({ titleText: "FromApi" })
  })

  it("returns an empty config when neither source is available", async () => {
    mockFetch(() => undefined)
    const cfg = await loadConfig()
    expect(cfg).toEqual({})
  })
})
