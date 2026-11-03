import type { PromSeries } from "./proxy.ts"

export const RANGES = {
  "1h": 3600,
  "6h": 6 * 3600,
  "24h": 24 * 3600,
  "7d": 7 * 24 * 3600,
} as const

export type RangeKey = keyof typeof RANGES

export interface RangeWindow {
  start: number
  end: number
  step: number
}

const POINTS = 240
const MIN_STEP = 15

export function rangeWindow(range: RangeKey, nowSeconds: number): RangeWindow {
  const span = RANGES[range]
  return { start: nowSeconds - span, end: nowSeconds, step: Math.max(MIN_STEP, Math.round(span / POINTS)) }
}

// The longterm store keeps one sample per 5m, so a narrower window can be empty;
// 4 x step also keeps samples between points from being skipped.
function rateWindow(step: number): string {
  return `${Math.max(300, step * 4)}s`
}

const CONTAINERS = 'container!="",container!="POD"'

export interface MetricQuery {
  label: string
  expr: string
}

export interface MetricPanel {
  id: "cpu" | "memory" | "network"
  title: string
  format: (value: number) => string
  queries: (window: RangeWindow) => MetricQuery[]
}

export function formatCores(value: number): string {
  if (value === 0) return "0"
  if (Math.abs(value) < 1) return `${Math.round(value * 1000)}m`
  return `${Number(value.toFixed(2))}`
}

const BYTE_UNITS = ["B", "KiB", "MiB", "GiB", "TiB"]

export function formatBytes(value: number): string {
  let v = value
  let unit = 0
  while (Math.abs(v) >= 1024 && unit < BYTE_UNITS.length - 1) {
    v /= 1024
    unit++
  }
  return unit === 0 ? `${Math.round(v)} B` : `${v.toFixed(1)} ${BYTE_UNITS[unit]}`
}

// The proxy injects the namespace or application scope, so these stay kind-agnostic.
export const METRIC_PANELS: MetricPanel[] = [
  {
    id: "cpu",
    title: "CPU (cores)",
    format: formatCores,
    queries: ({ step }) => [
      {
        label: "cpu",
        expr: `sum by (pod) (rate(container_cpu_usage_seconds_total{${CONTAINERS}}[${rateWindow(step)}]))`,
      },
    ],
  },
  {
    id: "memory",
    title: "Memory (working set)",
    format: formatBytes,
    queries: () => [
      { label: "memory", expr: `sum by (pod) (container_memory_working_set_bytes{${CONTAINERS}})` },
    ],
  },
  {
    id: "network",
    title: "Network",
    format: (v) => `${formatBytes(v)}/s`,
    queries: ({ step }) => [
      { label: "receive", expr: `sum(rate(container_network_receive_bytes_total[${rateWindow(step)}]))` },
      { label: "transmit", expr: `sum(rate(container_network_transmit_bytes_total[${rateWindow(step)}]))` },
    ],
  },
]

export interface ChartSeries {
  label: string
  values: (number | null)[]
}

export interface ChartData {
  timestamps: number[]
  series: ChartSeries[]
}

export function toChartData(results: { label: string; series: PromSeries[] }[]): ChartData {
  const flat = results.flatMap(({ label, series }) =>
    series.map((s) => ({ label: s.metric.pod ?? label, points: new Map(s.values.map(([t, v]) => [t, Number(v)])) })),
  )
  const timestamps = [...new Set(flat.flatMap((s) => [...s.points.keys()]))].sort((a, b) => a - b)
  return {
    timestamps,
    series: flat.map(({ label, points }) => ({
      label,
      values: timestamps.map((t) => {
        const v = points.get(t)
        return v === undefined || Number.isNaN(v) ? null : v
      }),
    })),
  }
}
