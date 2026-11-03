import type { LogEntry } from "./proxy.ts"

export const LOG_RANGES = {
  "15m": 15 * 60,
  "1h": 3600,
  "6h": 6 * 3600,
  "24h": 24 * 3600,
} as const

export type LogRangeKey = keyof typeof LOG_RANGES

export const POD_FIELD = "kubernetes_pod_name"
export const CONTAINER_FIELD = "kubernetes_container_name"

function quote(value: string): string {
  return `"${value.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`
}

function splitAtFirstPipe(query: string): [string, string] {
  let quoteChar = ""
  for (let i = 0; i < query.length; i++) {
    const c = query[i]
    if (quoteChar) {
      if (c === "\\") i++
      else if (c === quoteChar) quoteChar = ""
    } else if (c === '"' || c === "'" || c === "`") {
      quoteChar = c
    } else if (c === "|") {
      return [query.slice(0, i), query.slice(i + 1)]
    }
  }
  return [query, ""]
}

// The picker narrows the filter part only: AND binds tighter than OR in
// LogsQL, and the user's pipes must still run after the narrowed filter.
export function buildLogsQuery({ filter, pod, container }: { filter: string; pod?: string; container?: string }): string {
  const trimmed = filter.trim() || "*"
  const matchers = [
    pod ? `${POD_FIELD}=${quote(pod)}` : "",
    container ? `${CONTAINER_FIELD}=${quote(container)}` : "",
  ].filter(Boolean)
  if (matchers.length === 0) return trimmed
  const [head, pipes] = splitAtFirstPipe(trimmed)
  const scoped = `{${matchers.join(",")}} (${head.trim() || "*"})`
  return pipes.trim() ? `${scoped} | ${pipes.trim()}` : scoped
}

// VictoriaLogs trims trailing zeros from the fraction, so the strings do not sort lexically.
function padFraction(time: string): string {
  const m = /^([^.Z]+)(?:\.(\d+))?Z$/.exec(time)
  return m ? `${m[1]}.${(m[2] ?? "").padEnd(9, "0")}Z` : time
}

export function compareTime(a: string, b: string): number {
  const x = padFraction(a)
  const y = padFraction(b)
  return x < y ? -1 : x > y ? 1 : 0
}

const entryKey = (e: LogEntry) => `${e._stream_id ?? ""}\u0000${e._time}`

// (_stream_id, _time) is not unique in VictoriaLogs, so dedup applies only to
// lines a resumed tail replays at or before the resume point.
export function mergeTail(buffer: LogEntry[], incoming: LogEntry[], cap: number, resumedFrom?: string): LogEntry[] {
  let fresh = incoming
  if (resumedFrom) {
    const seen = new Set(buffer.map(entryKey))
    fresh = incoming.filter((e) => compareTime(e._time, resumedFrom) > 0 || !seen.has(entryKey(e)))
  }
  const merged = buffer.concat(fresh)
  return merged.length > cap ? merged.slice(merged.length - cap) : merged
}
