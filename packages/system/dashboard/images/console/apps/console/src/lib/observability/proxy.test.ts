import { describe, it, expect, vi } from "vitest"
import { K8sClient } from "@cozystack/k8s-client"
import {
  ProxyError,
  errorKindForStatus,
  fieldValues,
  isScopeWide,
  logsRoot,
  metricsRoot,
  queryLogs,
  queryRange,
  tailLogs,
} from "./proxy.ts"

const app = { namespace: "tenant-a", plural: "postgreses", name: "db" }
const ns = { namespace: "tenant-a" }

function clientReturning(...responses: Response[]) {
  const client = new K8sClient({ onUnauthorized: () => {} })
  const raw = vi.spyOn(client, "raw")
  for (const r of responses) raw.mockResolvedValueOnce(r)
  return { client, raw }
}

function calledUrl(raw: ReturnType<typeof clientReturning>["raw"], call = 0): URL {
  return new URL(raw.mock.calls[call][0], "http://x")
}

describe("roots", () => {
  it("builds the application roots under apps.cozystack.io", () => {
    expect(metricsRoot(app)).toBe("/apis/apps.cozystack.io/v1alpha1/namespaces/tenant-a/postgreses/db/metrics/")
    expect(logsRoot(app)).toBe("/apis/apps.cozystack.io/v1alpha1/namespaces/tenant-a/postgreses/db/log/")
  })

  it("builds the namespace roots under cozystack.io", () => {
    expect(metricsRoot(ns)).toBe("/apis/cozystack.io/v1alpha1/namespaces/tenant-a/metrics/")
    expect(logsRoot(ns)).toBe("/apis/cozystack.io/v1alpha1/namespaces/tenant-a/logs/")
  })
})

describe("errorKindForStatus", () => {
  it.each([
    [400, "bad-request"],
    [401, "unauthorized"],
    [403, "forbidden"],
    [404, "not-available"],
    [422, "limit"],
    [429, "rate-limited"],
    [502, "store-unreachable"],
    [503, "unavailable"],
    [504, "timeout"],
    [500, "unknown"],
  ])("maps %i to %s", (status, kind) => {
    expect(errorKindForStatus(status)).toBe(kind)
  })
})

describe("queryRange", () => {
  it("GETs query_range under the root and returns the matrix", async () => {
    const body = { status: "success", data: { resultType: "matrix", result: [{ metric: { pod: "p" }, values: [[1, "2"]] }] } }
    const { client, raw } = clientReturning(Response.json(body))

    const result = await queryRange(client, metricsRoot(app), { query: "up", start: 10, end: 20, step: 5 })

    const url = calledUrl(raw)
    expect(url.pathname).toBe(`${metricsRoot(app)}api/v1/query_range`)
    expect(Object.fromEntries(url.searchParams)).toEqual({ query: "up", start: "10", end: "20", step: "5" })
    expect(result).toEqual(body.data.result)
  })

  it("throws a ProxyError carrying the Prometheus error text", async () => {
    const { client } = clientReturning(
      Response.json({ status: "error", error: "range too long" }, { status: 422 }),
    )

    const err = await queryRange(client, metricsRoot(app), { query: "up", start: 0, end: 1, step: 1 }).catch((e) => e)

    expect(err).toBeInstanceOf(ProxyError)
    expect(err).toMatchObject({ kind: "limit", status: 422, message: "range too long" })
  })

  it("treats a Kubernetes Status 403 as a cluster without the proxy route", async () => {
    const status = { kind: "Status", apiVersion: "v1", status: "Failure", reason: "Forbidden", code: 403, message: "logs.cozystack.io \"select\" is forbidden" }
    const { client } = clientReturning(Response.json(status, { status: 403 }))

    const err = await queryRange(client, metricsRoot(ns), { query: "up", start: 0, end: 1, step: 1 }).catch((e) => e)

    expect(err).toMatchObject({ kind: "not-available", status: 403 })
  })

  it("keeps the proxy's own plain-text 403 as forbidden", async () => {
    const { client } = clientReturning(new Response("forbidden\n", { status: 403 }))

    const err = await queryRange(client, metricsRoot(ns), { query: "up", start: 0, end: 1, step: 1 }).catch((e) => e)

    expect(err).toMatchObject({ kind: "forbidden" })
  })

  it("reports a fetch failure as a network error", async () => {
    const client = new K8sClient()
    vi.spyOn(client, "raw").mockRejectedValue(new TypeError("offline"))

    const err = await queryRange(client, metricsRoot(app), { query: "up", start: 0, end: 1, step: 1 }).catch((e) => e)

    expect(err).toMatchObject({ kind: "network" })
  })
})

describe("queryLogs", () => {
  it("parses NDJSON lines and sorts them by _time ascending", async () => {
    const ndjson = [
      JSON.stringify({ _time: "2026-11-04T10:00:02Z", _msg: "b" }),
      "",
      JSON.stringify({ _time: "2026-11-04T10:00:01Z", _msg: "a" }),
    ].join("\n")
    const { client, raw } = clientReturning(new Response(ndjson))

    const entries = await queryLogs(client, logsRoot(ns), { query: "*", start: "s", end: "e", limit: 100 })

    expect(calledUrl(raw).pathname).toBe(`${logsRoot(ns)}select/logsql/query`)
    expect(Object.fromEntries(calledUrl(raw).searchParams)).toEqual({ query: "*", start: "s", end: "e", limit: "100" })
    expect(entries.map((e) => e._msg)).toEqual(["a", "b"])
  })

  it("keeps a plain-text error body as the message", async () => {
    const { client } = clientReturning(new Response("cannot parse query", { status: 400 }))

    const err = await queryLogs(client, logsRoot(ns), { query: "(", start: "s", end: "e", limit: 1 }).catch((e) => e)

    expect(err).toMatchObject({ kind: "bad-request", message: "cannot parse query" })
  })
})

describe("fieldValues", () => {
  it("uses stream_field_values for stream fields and returns the values", async () => {
    const { client, raw } = clientReturning(
      Response.json({ values: [{ value: "pod-b", hits: 1 }, { value: "pod-a", hits: 9 }] }),
    )

    const values = await fieldValues(client, logsRoot(app), {
      field: "kubernetes_pod_name",
      stream: true,
      query: "*",
      start: "s",
      end: "e",
    })

    expect(calledUrl(raw).pathname).toBe(`${logsRoot(app)}select/logsql/stream_field_values`)
    expect(calledUrl(raw).searchParams.get("field")).toBe("kubernetes_pod_name")
    expect(values).toEqual(["pod-a", "pod-b"])
  })

  it("uses field_values for other fields", async () => {
    const { client, raw } = clientReturning(Response.json({ values: [] }))

    await fieldValues(client, logsRoot(app), { field: "level", stream: false, query: "*", start: "s", end: "e" })

    expect(calledUrl(raw).pathname).toBe(`${logsRoot(app)}select/logsql/field_values`)
  })
})

describe("tailLogs", () => {
  it("delivers every line as it arrives, including one split across chunks", async () => {
    const enc = new TextEncoder()
    const chunks = ['{"_time":"t1","_msg":"a"}\n{"_ti', 'me":"t2","_msg":"b"}\n']
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        for (const ch of chunks) c.enqueue(enc.encode(ch))
        c.close()
      },
    })
    const { client, raw } = clientReturning(new Response(body))
    const seen: string[] = []

    await tailLogs(client, logsRoot(ns), { query: "*", since: "t0" }, (e) => seen.push(e._msg))

    expect(calledUrl(raw).pathname).toBe(`${logsRoot(ns)}select/logsql/tail`)
    expect(calledUrl(raw).searchParams.get("since")).toBe("t0")
    expect(seen).toEqual(["a", "b"])
  })

  it("omits since on the first connect", async () => {
    const { client, raw } = clientReturning(new Response(""))

    await tailLogs(client, logsRoot(ns), { query: "*" }, () => {})

    expect(calledUrl(raw).searchParams.has("since")).toBe(false)
  })

  it("throws a ProxyError when the tail is refused", async () => {
    const { client } = clientReturning(new Response("", { status: 403 }))

    const err = await tailLogs(client, logsRoot(ns), { query: "*" }, () => {}).catch((e) => e)

    expect(err).toMatchObject({ kind: "forbidden" })
  })
})

describe("isScopeWide", () => {
  it("is true for answers that do not depend on the query", () => {
    expect(isScopeWide(new ProxyError("not-available", 404, ""))).toBe(true)
    expect(isScopeWide(new ProxyError("forbidden", 403, ""))).toBe(true)
    expect(isScopeWide(new ProxyError("limit", 422, ""))).toBe(false)
    expect(isScopeWide(new Error("x"))).toBe(false)
  })
})
