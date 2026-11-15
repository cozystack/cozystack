import { describe, it, expect, vi, afterEach } from "vitest"
import { K8sClient } from "@cozystack/k8s-client"

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("K8sClient request headers", () => {
  it("lets a method's own content type replace the JSON default", async () => {
    const fetchMock = vi.fn(async () => new Response("{}"))
    vi.stubGlobal("fetch", fetchMock)
    const client = new K8sClient({ getToken: async () => "t0k" })

    await client.patch("apps", "v1", "deployments", "web", { spec: {} }, "default", "merge")

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    const headers = new Headers(init.headers)
    expect(headers.get("Content-Type")).toBe("application/merge-patch+json")
    expect(headers.get("Authorization")).toBe("Bearer t0k")
  })

  it("sends JSON by default", async () => {
    const fetchMock = vi.fn(async () => new Response("{}"))
    vi.stubGlobal("fetch", fetchMock)

    await new K8sClient().getApiGroups()

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(new Headers(init.headers).get("Content-Type")).toBe("application/json")
  })
})
