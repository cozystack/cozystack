import type { K8sClient } from "@cozystack/k8s-client"
import { compareTime } from "./logs.ts"

export type ObservabilityScope =
  | { namespace: string }
  | { namespace: string; plural: string; name: string }

function appBase(scope: ObservabilityScope): string | null {
  if (!("plural" in scope)) return null
  return `/apis/apps.cozystack.io/v1alpha1/namespaces/${scope.namespace}/${scope.plural}/${scope.name}`
}

export function metricsRoot(scope: ObservabilityScope): string {
  const app = appBase(scope)
  return app ? `${app}/metrics/` : `/apis/cozystack.io/v1alpha1/namespaces/${scope.namespace}/metrics/`
}

export function logsRoot(scope: ObservabilityScope): string {
  const app = appBase(scope)
  return app ? `${app}/log/` : `/apis/cozystack.io/v1alpha1/namespaces/${scope.namespace}/logs/`
}

export type ProxyErrorKind =
  | "bad-request"
  | "unauthorized"
  | "forbidden"
  | "not-available"
  | "limit"
  | "rate-limited"
  | "store-unreachable"
  | "unavailable"
  | "timeout"
  | "network"
  | "unknown"

const KIND_BY_STATUS: Record<number, ProxyErrorKind> = {
  400: "bad-request",
  401: "unauthorized",
  403: "forbidden",
  404: "not-available",
  422: "limit",
  429: "rate-limited",
  502: "store-unreachable",
  503: "unavailable",
  504: "timeout",
}

export function errorKindForStatus(status: number): ProxyErrorKind {
  return KIND_BY_STATUS[status] ?? "unknown"
}

export class ProxyError extends Error {
  kind: ProxyErrorKind
  status: number

  constructor(kind: ProxyErrorKind, status: number, message: string) {
    super(message)
    this.name = "ProxyError"
    this.kind = kind
    this.status = status
  }
}

// These answers hold for every query in the scope, so one notice replaces the whole view.
export function isScopeWide(error: unknown): boolean {
  return (
    error instanceof ProxyError &&
    (error.kind === "not-available" || error.kind === "forbidden" || error.kind === "unauthorized")
  )
}

// The proxy answers in plain text; a Kubernetes Status body means nginx sent
// the path to kube-apiserver, which RBAC-denies or 404s a route it does not serve.
function isKubernetesStatus(text: string): boolean {
  try {
    const parsed: unknown = JSON.parse(text)
    return !!parsed && typeof parsed === "object" && "kind" in parsed && parsed.kind === "Status"
  } catch {
    return false
  }
}

// Prometheus answers {"error": "..."}, VictoriaLogs and nginx answer plain text.
function errorMessage(text: string, status: number): string {
  try {
    const parsed: unknown = JSON.parse(text)
    if (parsed && typeof parsed === "object" && "error" in parsed && typeof parsed.error === "string") {
      return parsed.error
    }
  } catch {
    // not JSON
  }
  const trimmed = text.trim()
  return trimmed && trimmed.length < 300 && !trimmed.startsWith("<") ? trimmed : `Server returned ${status}`
}

async function proxyGet(
  client: K8sClient,
  path: string,
  params: Record<string, string | number | undefined>,
  signal?: AbortSignal,
): Promise<Response> {
  const search = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) if (v !== undefined) search.set(k, String(v))
  let res: Response
  try {
    res = await client.raw(`${path}?${search}`, { signal })
  } catch (err) {
    if (signal?.aborted) throw err
    throw new ProxyError("network", 0, "Unable to connect to the server")
  }
  if (!res.ok) {
    const text = await res.text().catch(() => "")
    const kind = isKubernetesStatus(text) ? "not-available" : errorKindForStatus(res.status)
    throw new ProxyError(kind, res.status, errorMessage(text, res.status))
  }
  return res
}

export interface PromSeries {
  metric: Record<string, string>
  values: [number, string][]
}

export interface RangeParams {
  query: string
  start: number
  end: number
  step: number
}

export async function queryRange(
  client: K8sClient,
  root: string,
  params: RangeParams,
  signal?: AbortSignal,
): Promise<PromSeries[]> {
  const res = await proxyGet(client, `${root}api/v1/query_range`, { ...params }, signal)
  const body = (await res.json()) as { data?: { result?: PromSeries[] } }
  return body.data?.result ?? []
}

export interface LogEntry {
  _time: string
  _msg: string
  _stream_id?: string
  [field: string]: string | undefined
}

function parseLine(line: string): LogEntry | null {
  if (!line.trim()) return null
  try {
    const value: unknown = JSON.parse(line)
    if (value && typeof value === "object" && "_time" in value) return value as LogEntry
  } catch {
    // a torn or non-JSON line carries nothing to show
  }
  return null
}

export interface LogsQueryParams {
  query: string
  start: string
  end: string
  limit: number
}

export async function queryLogs(
  client: K8sClient,
  root: string,
  params: LogsQueryParams,
  signal?: AbortSignal,
): Promise<LogEntry[]> {
  const res = await proxyGet(client, `${root}select/logsql/query`, { ...params }, signal)
  const entries = (await res.text()).split("\n").map(parseLine).filter((e) => e !== null)
  return entries.sort((a, b) => compareTime(a._time, b._time))
}

export interface FieldValuesParams {
  field: string
  stream: boolean
  query: string
  start: string
  end: string
}

export async function fieldValues(
  client: K8sClient,
  root: string,
  { stream, ...params }: FieldValuesParams,
  signal?: AbortSignal,
): Promise<string[]> {
  const endpoint = stream ? "stream_field_values" : "field_values"
  const res = await proxyGet(client, `${root}select/logsql/${endpoint}`, { ...params }, signal)
  const body = (await res.json()) as { values?: { value: string }[] }
  return (body.values ?? []).map((v) => v.value).sort()
}

export async function tailLogs(
  client: K8sClient,
  root: string,
  params: { query: string; since?: string },
  onEntry: (entry: LogEntry) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await proxyGet(client, `${root}select/logsql/tail`, params, signal)
  if (!res.body) return
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ""
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    const lines = buffer.split("\n")
    buffer = lines.pop() ?? ""
    for (const line of lines) {
      const entry = parseLine(line)
      if (entry) onEntry(entry)
    }
  }
  const last = parseLine(buffer)
  if (last) onEntry(last)
}
