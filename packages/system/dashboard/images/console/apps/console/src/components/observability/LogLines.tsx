import { useEffect, useRef } from "react"
import { CONTAINER_FIELD, POD_FIELD } from "../../lib/observability/logs.ts"
import type { LogEntry } from "../../lib/observability/proxy.ts"

const BOTTOM_SLACK_PX = 24

export function LogLines({ entries }: { entries: LogEntry[] }) {
  const ref = useRef<HTMLOListElement>(null)
  const pinned = useRef(true)

  useEffect(() => {
    if (entries.length === 0) pinned.current = true
    else if (ref.current && pinned.current) ref.current.scrollTop = ref.current.scrollHeight
  }, [entries])

  if (entries.length === 0) return <p className="text-sm text-slate-500">No log lines for this range and filter.</p>
  return (
    <ol
      ref={ref}
      onScroll={(e) => {
        const el = e.currentTarget
        pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight <= BOTTOM_SLACK_PX
      }}
      className="max-h-[60vh] overflow-auto rounded-md bg-slate-950 p-3 font-mono text-xs leading-5 text-slate-100"
    >
      {entries.map((e, i) => (
        <li key={`${e._stream_id}-${e._time}-${i}`} className="whitespace-pre-wrap break-all">
          <span className="text-slate-400">{new Date(e._time).toLocaleTimeString()}</span>{" "}
          <span className="text-sky-300">{`${e[POD_FIELD] ?? ""}/${e[CONTAINER_FIELD] ?? ""}`}</span>{" "}
          {e._msg}
        </li>
      ))}
    </ol>
  )
}
