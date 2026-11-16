import { useEffect, useRef, type ReactNode } from "react"
import { CONTAINER_FIELD, POD_FIELD } from "../../lib/observability/logs.ts"
import type { LogEntry } from "../../lib/observability/proxy.ts"

const BOTTOM_SLACK_PX = 24
const SHOWN_APART = new Set(["_time", "_msg", "_stream_id", POD_FIELD, CONTAINER_FIELD])

function source(e: LogEntry): string | null {
  const pod = e[POD_FIELD]
  const container = e[CONTAINER_FIELD]
  return pod || container ? `${pod ?? ""}/${container ?? ""}` : null
}

// A `fields` or `stats` pipe can leave a row without _msg; its other fields are then the content.
function message(e: LogEntry): string {
  if (e._msg !== undefined) return e._msg
  return Object.entries(e)
    .filter(([k, v]) => v !== undefined && !SHOWN_APART.has(k))
    .map(([k, v]) => `${k}=${v}`)
    .join(" ")
}

export function LogLines({ entries, placeholder }: { entries: LogEntry[]; placeholder: ReactNode }) {
  const ref = useRef<HTMLOListElement>(null)
  const pinned = useRef(true)

  useEffect(() => {
    if (entries.length === 0) pinned.current = true
    else if (ref.current && pinned.current) ref.current.scrollTop = ref.current.scrollHeight
  }, [entries])

  return (
    <section
      aria-label="Log lines"
      className="h-[60vh] min-h-72 overflow-hidden rounded-lg bg-slate-950 font-mono text-xs leading-5 text-slate-100 shadow-xs"
    >
      {entries.length === 0 ? (
        placeholder
      ) : (
        <ol
          ref={ref}
          onScroll={(e) => {
            const el = e.currentTarget
            pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight <= BOTTOM_SLACK_PX
          }}
          className="h-full overflow-auto p-3"
        >
          {entries.map((e, i) => {
            const from = source(e)
            return (
              <li key={`${e._stream_id}-${e._time}-${i}`} className="whitespace-pre-wrap break-all">
                {e._time !== undefined && <><span className="text-slate-400">{new Date(e._time).toLocaleTimeString()}</span>{" "}</>}
                {from !== null && <><span className="text-sky-300">{from}</span>{" "}</>}
                {message(e)}
              </li>
            )
          })}
        </ol>
      )}
    </section>
  )
}
