import { describe, it, expect, vi, afterEach } from "vitest"
import { K8sClient, K8sApiError } from "@cozystack/k8s-client"

function fakeResponse(opts: {
  ok?: boolean
  status?: number
  statusText?: string
  body?: string
}): Response {
  return {
    ok: opts.ok ?? true,
    status: opts.status ?? 200,
    statusText: opts.statusText ?? "OK",
    text: async () => opts.body ?? "",
    json: async () => JSON.parse(opts.body ?? "null"),
  } as unknown as Response
}

const unauthorized = () =>
  fakeResponse({ ok: false, status: 401, statusText: "Unauthorized" })

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("K8sClient onUnauthorized (401) handling", () => {
  it("invokes onUnauthorized exactly once when a request returns 401", async () => {
    const onUnauthorized = vi.fn()
    vi.stubGlobal("fetch", vi.fn(async () => unauthorized()))
    const client = new K8sClient({ onUnauthorized })

    await expect(client.get("", "v1", "pods", "p", "ns")).rejects.toThrow(
      K8sApiError,
    )

    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it("invokes onUnauthorized only once across two concurrent 401s", async () => {
    const onUnauthorized = vi.fn()
    vi.stubGlobal("fetch", vi.fn(async () => unauthorized()))
    const client = new K8sClient({ onUnauthorized })

    // The unauthorizedHandled latch must collapse a redirect storm when many
    // in-flight calls 401 at once into a single re-auth navigation.
    await Promise.allSettled([
      client.get("", "v1", "pods", "a", "ns"),
      client.get("", "v1", "pods", "b", "ns"),
    ])

    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it("does not invoke onUnauthorized on a non-401 error", async () => {
    const onUnauthorized = vi.fn()
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        fakeResponse({
          ok: false,
          status: 403,
          statusText: "Forbidden",
          body: JSON.stringify({ message: "forbidden" }),
        }),
      ),
    )
    const client = new K8sClient({ onUnauthorized })

    await expect(client.get("", "v1", "pods", "p", "ns")).rejects.toThrow(
      K8sApiError,
    )

    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it("does not invoke onUnauthorized on a successful response", async () => {
    const onUnauthorized = vi.fn()
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => fakeResponse({ body: JSON.stringify({ kind: "Pod" }) })),
    )
    const client = new K8sClient({ onUnauthorized })

    await client.get("", "v1", "pods", "p", "ns")

    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it("invokes onUnauthorized when the watch stream returns 401", async () => {
    const onUnauthorized = vi.fn()
    vi.stubGlobal("fetch", vi.fn(async () => unauthorized()))
    const client = new K8sClient({ onUnauthorized })

    // watch() is fire-and-forget; resolve once its onError fires.
    await new Promise<void>((resolve) => {
      client.watch(
        "",
        "v1",
        "pods",
        "ns",
        "0",
        () => {},
        () => resolve(),
      )
    })

    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })
})

describe("K8sClient default unauthorized handler", () => {
  it("redirects to /oauth2/start with the current location preserved as rd", async () => {
    // jsdom's location.assign is non-configurable, so swap the whole object.
    const original = window.location
    const assign = vi.fn()
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { pathname: "/apps/postgres", search: "?tab=config", hash: "#x", assign },
    })
    vi.stubGlobal("fetch", vi.fn(async () => unauthorized()))
    // No onUnauthorized override -> the browser default redirect kicks in.
    const client = new K8sClient()

    try {
      await expect(client.get("", "v1", "pods", "p", "ns")).rejects.toThrow(
        K8sApiError,
      )

      // Relative rd (no origin) preserves the destination without tripping
      // oauth2-proxy's open-redirect rejection.
      expect(assign).toHaveBeenCalledTimes(1)
      expect(assign).toHaveBeenCalledWith(
        `/oauth2/start?rd=${encodeURIComponent("/apps/postgres?tab=config#x")}`,
      )
    } finally {
      Object.defineProperty(window, "location", {
        configurable: true,
        value: original,
      })
    }
  })
})
