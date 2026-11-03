import { ProxyError } from "../../lib/observability/proxy.ts"

interface ProxyNoticeProps {
  what: "Metrics" | "Logs"
  error: unknown
}

function describe(what: ProxyNoticeProps["what"], error: unknown): { quiet: boolean; text: string } {
  const lower = what.toLowerCase()
  if (!(error instanceof ProxyError)) {
    return { quiet: false, text: error instanceof Error ? error.message : String(error) }
  }
  switch (error.kind) {
    case "not-available":
      return { quiet: true, text: `${what} are not available on this cluster.` }
    case "store-unreachable":
      return { quiet: true, text: `The ${lower} store is unreachable right now.` }
    case "unauthorized":
      return { quiet: false, text: "Your session has expired. Sign in again." }
    case "forbidden":
      return { quiet: false, text: `You do not have permission to read ${lower} here.` }
    case "limit":
      return { quiet: false, text: `${error.message}. Choose a shorter range.` }
    case "rate-limited":
      return { quiet: false, text: "Too many requests. Try again in a moment." }
    case "timeout":
      return { quiet: false, text: "The store did not answer in time. Choose a shorter range or a narrower filter." }
    case "unavailable":
      return { quiet: false, text: "The service is temporarily unavailable. Try again in a moment." }
    default:
      return { quiet: false, text: error.message }
  }
}

export function ProxyNotice({ what, error }: ProxyNoticeProps) {
  const { quiet, text } = describe(what, error)
  if (quiet) {
    return (
      <p role="status" className="rounded-md border border-slate-200 bg-slate-50 px-4 py-3 text-sm text-slate-600">
        {text}
      </p>
    )
  }
  return (
    <p role="alert" className="rounded-md border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800">
      {text}
    </p>
  )
}
