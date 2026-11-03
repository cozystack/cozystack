import { useEffect, useState } from "react"
import { useK8sClient } from "@cozystack/k8s-client"
import { compareTime, mergeTail } from "./logs.ts"
import { ProxyError, tailLogs, type LogEntry } from "./proxy.ts"

export type TailState = "connecting" | "live" | "reconnecting" | "stopped"

interface TailSnapshot {
  key: string
  entries: LogEntry[]
  state: TailState
  error: unknown
}

const FLUSH_MS = 100
const MAX_RETRY_MS = 30_000
// Load refusals retry: the proxy caps rate and concurrent tails with 429 (422 in
// its first builds), and a reconnect can race the previous tail's freed slot.
const FATAL: ReadonlySet<string> = new Set(["bad-request", "unauthorized", "forbidden", "not-available"])

const sleep = (ms: number, signal: AbortSignal) =>
  new Promise<void>((resolve) => {
    const onAbort = () => {
      clearTimeout(t)
      resolve()
    }
    const t = setTimeout(() => {
      signal.removeEventListener("abort", onAbort)
      resolve()
    }, ms)
    signal.addEventListener("abort", onAbort, { once: true })
  })

// Each non-null session is one follow run with its own buffer; null stops following.
export function useLogTail(
  root: string,
  query: string,
  session: number | null,
  { cap = 1000, retryDelayMs = 2000 }: { cap?: number; retryDelayMs?: number } = {},
): Omit<TailSnapshot, "key"> {
  const client = useK8sClient()
  const key = `${root}\n${query}\n${session}`
  const [snap, setSnap] = useState<TailSnapshot>({ key, entries: [], state: "connecting", error: null })

  useEffect(() => {
    if (session === null) return
    const controller = new AbortController()
    const { signal } = controller
    let pending: LogEntry[] = []
    let timer: ReturnType<typeof setTimeout> | undefined
    let resumedFrom: string | undefined
    let lastTime: string | undefined

    const update = (patch: Partial<TailSnapshot>, batch: LogEntry[] = [], from = resumedFrom) =>
      setSnap((prev) => {
        const base = prev.key === key ? prev : { key, entries: [], state: "connecting" as const, error: null }
        return { ...base, ...patch, entries: batch.length ? mergeTail(base.entries, batch, cap, from) : base.entries }
      })
    const flush = () => {
      clearTimeout(timer)
      timer = undefined
      if (pending.length === 0 || signal.aborted) return
      const batch = pending
      pending = []
      update({ state: "live", error: null }, batch)
    }

    const run = async () => {
      for (let attempt = 0; !signal.aborted; attempt++) {
        resumedFrom = lastTime
        try {
          await tailLogs(client, root, { query, since: lastTime }, (entry) => {
            attempt = 0
            if (!lastTime || compareTime(entry._time, lastTime) > 0) lastTime = entry._time
            pending.push(entry)
            timer ??= setTimeout(flush, FLUSH_MS)
          }, signal)
          flush()
        } catch (err) {
          flush()
          if (signal.aborted) return
          if (err instanceof ProxyError && FATAL.has(err.kind)) {
            update({ state: "stopped", error: err })
            return
          }
          update({ error: err })
        }
        if (signal.aborted) return
        update({ state: "reconnecting" })
        await sleep(Math.min(retryDelayMs * 2 ** attempt, MAX_RETRY_MS), signal)
      }
    }
    void run()
    return () => {
      controller.abort()
      clearTimeout(timer)
    }
  }, [client, root, query, session, key, cap, retryDelayMs])

  const current = snap.key === key ? snap : { key, entries: [], state: "connecting" as const, error: null }
  return { entries: current.entries, state: current.state, error: current.error }
}
