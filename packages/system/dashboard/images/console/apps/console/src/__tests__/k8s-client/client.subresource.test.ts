import { describe, it, expect, vi, afterEach } from "vitest"
import { K8sClient } from "@cozystack/k8s-client"

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

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("K8sClient.subresource", () => {
  it("PUTs to /{plural}/{name}/{subresource} under the aggregated API group", async () => {
    const fetchMock = vi.fn(async () => fakeResponse({ body: "" }))
    vi.stubGlobal("fetch", fetchMock)
    const client = new K8sClient({ baseUrl: "/base" })

    await client.subresource(
      "subresources.kubevirt.io",
      "v1",
      "virtualmachines",
      "vm-instance-demo",
      "start",
      "tenant-root",
    )

    expect(fetchMock).toHaveBeenCalledWith(
      "/base/apis/subresources.kubevirt.io/v1/namespaces/tenant-root/virtualmachines/vm-instance-demo/start",
      expect.objectContaining({ method: "PUT" }),
    )
  })

  it("POSTs when method is POST", async () => {
    const fetchMock = vi.fn(async () => fakeResponse({ body: "" }))
    vi.stubGlobal("fetch", fetchMock)
    const client = new K8sClient()

    await client.subresource(
      "subresources.kubevirt.io",
      "v1",
      "virtualmachines",
      "vm",
      "restart",
      "ns",
      {},
      "POST",
    )

    expect(fetchMock).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({ method: "POST" }),
    )
  })
})

describe("K8sClient.request empty-body handling", () => {
  it("returns undefined for an empty 2xx body (KubeVirt action subresources)", async () => {
    // virtualmachines/{name}/start|stop|restart answer 2xx with no body;
    // JSON.parse("") would throw "Unexpected end of JSON input".
    vi.stubGlobal("fetch", vi.fn(async () => fakeResponse({ status: 200, body: "" })))
    const client = new K8sClient()

    const result = await client.subresource(
      "subresources.kubevirt.io",
      "v1",
      "virtualmachines",
      "vm",
      "stop",
    )

    expect(result).toBeUndefined()
  })

  it("parses the JSON body when a 2xx response is non-empty", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => fakeResponse({ status: 200, body: JSON.stringify({ kind: "VirtualMachine" }) })),
    )
    const client = new K8sClient()

    const result = await client.get("kubevirt.io", "v1", "virtualmachines", "vm", "ns")

    expect(result).toEqual({ kind: "VirtualMachine" })
  })

  it("short-circuits to undefined on 204 No Content without reading the body", async () => {
    const textSpy = vi.fn(async () => "should-not-be-read")
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          ({
            ok: true,
            status: 204,
            statusText: "No Content",
            text: textSpy,
          }) as unknown as Response,
      ),
    )
    const client = new K8sClient()

    const result = await client.delete("", "v1", "configmaps", "cm", "ns")

    expect(result).toBeUndefined()
    expect(textSpy).not.toHaveBeenCalled()
  })
})
