import { describe, it, expect, vi } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"
import type { ReactNode } from "react"
import { K8sClient, K8sProvider } from "@cozystack/k8s-client"
import { useLogTail } from "./use-log-tail.ts"

const line = (time: string, msg: string) => JSON.stringify({ _time: time, _msg: msg, _stream_id: "s" })

function ndjson(...lines: string[]): Response {
  return new Response(lines.map((l) => `${l}\n`).join(""))
}

function setup(...responses: Response[]) {
  const client = new K8sClient()
  const raw = vi.spyOn(client, "raw")
  for (const r of responses) raw.mockResolvedValueOnce(r)
  raw.mockImplementation(() => new Promise(() => {}))
  const wrapper = ({ children }: { children: ReactNode }) => <K8sProvider client={client}>{children}</K8sProvider>
  return { raw, wrapper }
}

const T1 = "2026-11-04T10:00:01Z"
const T2 = "2026-11-04T10:00:02Z"
const T3 = "2026-11-04T10:00:03Z"

describe("useLogTail", () => {
  it("streams lines and resumes after a disconnect from the last _time without duplicates", async () => {
    const { raw, wrapper } = setup(ndjson(line(T1, "a"), line(T2, "b")), ndjson(line(T2, "b"), line(T3, "c")))

    const { result } = renderHook(() => useLogTail("/root/", "*", true, { cap: 100, retryDelayMs: 1 }), { wrapper })

    await waitFor(() => expect(result.current.entries.map((e) => e._msg)).toEqual(["a", "b", "c"]))
    expect(new URL(raw.mock.calls[0][0], "http://x").searchParams.has("since")).toBe(false)
    expect(new URL(raw.mock.calls[1][0], "http://x").searchParams.get("since")).toBe(T2)
  })

  it("stops and reports an error the proxy will repeat on every retry", async () => {
    const { raw, wrapper } = setup(new Response("", { status: 403 }))

    const { result } = renderHook(() => useLogTail("/root/", "*", true, { cap: 100, retryDelayMs: 1 }), { wrapper })

    await waitFor(() => expect(result.current.state).toBe("stopped"))
    expect(result.current.error).toMatchObject({ kind: "forbidden" })
    expect(raw).toHaveBeenCalledTimes(1)
  })

  it("does not connect while disabled", () => {
    const { raw, wrapper } = setup()

    renderHook(() => useLogTail("/root/", "*", false), { wrapper })

    expect(raw).not.toHaveBeenCalled()
  })

  it("aborts the stream on unmount", async () => {
    const { raw, wrapper } = setup()
    const { unmount } = renderHook(() => useLogTail("/root/", "*", true), { wrapper })
    await waitFor(() => expect(raw).toHaveBeenCalled())

    unmount()

    const init = raw.mock.calls[0][1] as RequestInit
    expect(init.signal?.aborted).toBe(true)
  })
})
