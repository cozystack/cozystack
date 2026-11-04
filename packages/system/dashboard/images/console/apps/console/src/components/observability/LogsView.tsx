import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { useK8sClient } from "@cozystack/k8s-client"
import { Button } from "@cozystack/ui"
import { Input } from "../ui/input.tsx"
import { SelectItem, SelectLabel, SelectPopup, SelectRoot, SelectTrigger, SelectValue } from "../ui/select.tsx"
import {
  CONTAINER_FIELD,
  LOG_RANGES,
  POD_FIELD,
  buildLogsQuery,
  type LogRangeKey,
} from "../../lib/observability/logs.ts"
import {
  fieldValues,
  isScopeWide,
  logsRoot,
  queryLogs,
  type ObservabilityScope,
} from "../../lib/observability/proxy.ts"
import { useLogTail } from "../../lib/observability/use-log-tail.ts"
import { ProxyNotice } from "./ProxyNotice.tsx"
import { RangePicker } from "./RangePicker.tsx"
import { LogLines } from "./LogLines.tsx"
import { EmptyState } from "./EmptyState.tsx"

const HISTORY_LIMIT = 500
const FOLLOW_CAP = 1000
const TAIL_STATE_LABEL = { connecting: "Connecting…", live: "Live", reconnecting: "Reconnecting…", stopped: "Stopped" }

function windowFor(range: LogRangeKey) {
  const end = Date.now()
  return { start: new Date(end - LOG_RANGES[range] * 1000).toISOString(), end: new Date(end).toISOString() }
}

const ALL = ""

function Picker({ label, value, options, onChange }: { label: string; value: string; options: string[]; onChange: (v: string) => void }) {
  const items = [{ value: ALL, label: "All" }, ...options.filter((o) => o !== ALL).map((o) => ({ value: o, label: o }))]
  return (
    <SelectRoot items={items} value={value} onValueChange={(v) => onChange(v ?? ALL)}>
      <div className="flex min-w-0 items-center gap-1.5">
        <SelectLabel>{label}</SelectLabel>
        <SelectTrigger className="w-40" title={value || undefined}>
          <SelectValue />
        </SelectTrigger>
      </div>
      <SelectPopup>
        {items.map((item) => (
          <SelectItem key={item.value} value={item.value}>
            {item.label}
          </SelectItem>
        ))}
      </SelectPopup>
    </SelectRoot>
  )
}

function placeholderFor({ follow, stopped, loading, failed }: { follow: boolean; stopped: boolean; loading: boolean; failed: boolean }) {
  if (follow) {
    return stopped ? (
      <EmptyState tone="dark" title="Follow stopped before any line arrived." />
    ) : (
      <EmptyState tone="dark" busy title="Waiting for new log lines…" hint="Lines written from now on appear here." />
    )
  }
  if (loading) return <EmptyState tone="dark" busy title="Loading log lines…" />
  if (failed) return <EmptyState tone="dark" title="No log lines to show." />
  return <EmptyState tone="dark" title="No log lines for this range and filter." hint="Try a longer range or a broader filter." />
}

export function LogsView({ scope }: { scope: ObservabilityScope }) {
  const client = useK8sClient()
  const root = logsRoot(scope)
  const [range, setRange] = useState<LogRangeKey>("1h")
  const [draft, setDraft] = useState("")
  const [filter, setFilter] = useState("")
  const [pod, setPod] = useState("")
  const [container, setContainer] = useState("")
  const [followRun, setFollowRun] = useState<number | null>(null)
  const follow = followRun !== null
  const query = buildLogsQuery({ filter, pod, container })

  const pickerQuery = (field: string, narrowTo?: string) => ({
    queryKey: ["observability", "logs-fields", root, field, range, narrowTo ?? ""],
    queryFn: ({ signal }: { signal: AbortSignal }) =>
      fieldValues(client, root, {
        field,
        stream: true,
        query: buildLogsQuery({ filter: "", pod: narrowTo }),
        ...windowFor(range),
      }, signal),
    retry: false,
  })
  const pods = useQuery(pickerQuery(POD_FIELD))
  const containers = useQuery(pickerQuery(CONTAINER_FIELD, pod || undefined))

  const history = useQuery({
    queryKey: ["observability", "logs", root, query, range],
    queryFn: ({ signal }) => queryLogs(client, root, { query, limit: HISTORY_LIMIT, ...windowFor(range) }, signal),
    enabled: !follow,
    retry: false,
  })
  const tail = useLogTail(root, query, followRun, { cap: FOLLOW_CAP })

  const wide = [pods.error, history.error, tail.error].find(isScopeWide)
  if (wide) {
    return (
      <div className="p-6">
        <ProxyNotice what="Logs" error={wide} />
      </div>
    )
  }

  const error = follow ? tail.error : history.error
  const entries = follow ? tail.entries : (history.data ?? [])

  return (
    <div className="space-y-3 p-6">
      <div className="flex flex-wrap items-center gap-3">
        <form
          className="flex min-w-64 flex-1"
          onSubmit={(e) => {
            e.preventDefault()
            setFilter(draft)
          }}
        >
          <Input
            aria-label="LogsQL filter"
            value={draft}
            onValueChange={setDraft}
            placeholder='LogsQL filter, e.g. error or "connection refused"'
            className="font-mono"
          />
        </form>
        <Picker label="Pod" value={pod} options={pods.data ?? []} onChange={(v) => { setPod(v); setContainer("") }} />
        <Picker label="Container" value={container} options={containers.data ?? []} onChange={setContainer} />
        {!follow && <RangePicker options={Object.keys(LOG_RANGES) as LogRangeKey[]} value={range} onChange={setRange} />}
        <Button variant={follow ? "default" : "outline"} size="sm" aria-pressed={follow} onClick={() => setFollowRun(follow ? null : Date.now())}>
          Follow
        </Button>
      </div>
      {follow && (
        <p className="text-xs text-slate-500">
          {TAIL_STATE_LABEL[tail.state]}
          {` · newest ${FOLLOW_CAP} lines kept`}
        </p>
      )}
      {error ? <ProxyNotice what="Logs" error={error} /> : null}
      <LogLines
        key={followRun ?? "history"}
        entries={entries}
        placeholder={placeholderFor({ follow, stopped: tail.state === "stopped", loading: history.isLoading, failed: Boolean(error) })}
      />
    </div>
  )
}
