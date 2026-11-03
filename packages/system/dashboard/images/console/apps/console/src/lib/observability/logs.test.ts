import { describe, it, expect } from "vitest"
import { buildLogsQuery, compareTime, mergeTail } from "./logs.ts"

describe("buildLogsQuery", () => {
  it("passes the filter through when nothing is picked", () => {
    expect(buildLogsQuery({ filter: "error or warn" })).toBe("error or warn")
    expect(buildLogsQuery({ filter: "  " })).toBe("*")
  })

  it("ANDs the picked pod and container as a stream filter around the whole filter", () => {
    expect(buildLogsQuery({ filter: "error or warn", pod: "db-0", container: "postgres" })).toBe(
      '{kubernetes_pod_name="db-0",kubernetes_container_name="postgres"} (error or warn)',
    )
  })

  it("keeps the user's pipes after the scoped filter", () => {
    expect(buildLogsQuery({ filter: 'a or "x|y" | fields _msg', pod: "db-0" })).toBe(
      '{kubernetes_pod_name="db-0"} (a or "x|y") | fields _msg',
    )
  })

  it("escapes quotes in picked values", () => {
    expect(buildLogsQuery({ filter: "", pod: 'a"b' })).toBe('{kubernetes_pod_name="a\\"b"} (*)')
  })
})

describe("compareTime", () => {
  it("orders RFC3339 times with differing fraction lengths", () => {
    expect(compareTime("2026-11-04T10:00:01Z", "2026-11-04T10:00:01.1Z")).toBeLessThan(0)
    expect(compareTime("2026-11-04T10:00:01.5Z", "2026-11-04T10:00:01.123Z")).toBeGreaterThan(0)
    expect(compareTime("2026-11-04T10:00:01.10Z", "2026-11-04T10:00:01.1Z")).toBe(0)
  })
})

const e = (time: string, msg: string, stream = "s1") => ({ _time: time, _msg: msg, _stream_id: stream })

describe("mergeTail", () => {
  it("appends and keeps only the newest lines over the cap", () => {
    const out = mergeTail([e("t1", "a"), e("t2", "b")], [e("t3", "c")], 2)

    expect(out.map((x) => x._msg)).toEqual(["b", "c"])
  })

  it("drops replayed lines at or before the resume point", () => {
    const buffer = [e("2026-01-01T00:00:01Z", "a"), e("2026-01-01T00:00:02Z", "b")]

    const out = mergeTail(buffer, [e("2026-01-01T00:00:02Z", "b"), e("2026-01-01T00:00:03Z", "c")], 10, "2026-01-01T00:00:02Z")

    expect(out.map((x) => x._msg)).toEqual(["a", "b", "c"])
  })

  it("keeps a line at the resume point from another stream", () => {
    const buffer = [e("2026-01-01T00:00:02Z", "b", "s1")]

    const out = mergeTail(buffer, [e("2026-01-01T00:00:02Z", "other", "s2")], 10, "2026-01-01T00:00:02Z")

    expect(out).toHaveLength(2)
  })

  it("does not deduplicate outside a resume", () => {
    const out = mergeTail([e("t1", "a")], [e("t1", "a")], 10)

    expect(out).toHaveLength(2)
  })
})
