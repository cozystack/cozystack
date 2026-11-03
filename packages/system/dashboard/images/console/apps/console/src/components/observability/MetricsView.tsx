import { useState } from "react"
import { useQueries } from "@tanstack/react-query"
import { useK8sClient } from "@cozystack/k8s-client"
import { Section, Spinner } from "@cozystack/ui"
import {
  METRIC_PANELS,
  RANGES,
  rangeWindow,
  toChartData,
  type RangeKey,
} from "../../lib/observability/metrics.ts"
import { isScopeWide, metricsRoot, queryRange, type ObservabilityScope } from "../../lib/observability/proxy.ts"
import { ProxyNotice } from "./ProxyNotice.tsx"
import { TimeSeriesChart } from "./TimeSeriesChart.tsx"
import { RangePicker } from "./RangePicker.tsx"

const REFRESH_MS = 60_000

export function MetricsView({ scope }: { scope: ObservabilityScope }) {
  const client = useK8sClient()
  const root = metricsRoot(scope)
  const [range, setRange] = useState<RangeKey>("1h")

  const results = useQueries({
    queries: METRIC_PANELS.map((panel) => ({
      queryKey: ["observability", "metrics", root, panel.id, range],
      queryFn: async ({ signal }: { signal: AbortSignal }) => {
        const window = rangeWindow(range, Math.floor(Date.now() / 1000))
        const series = await Promise.all(
          panel.queries(window).map(async (q) => ({
            label: q.label,
            series: await queryRange(client, root, { query: q.expr, ...window }, signal),
          })),
        )
        return toChartData(series)
      },
      retry: false,
      refetchInterval: REFRESH_MS,
    })),
  })

  const wide = results.find((r) => isScopeWide(r.error))?.error

  return (
    <div className="space-y-4 p-6">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold text-slate-900">Resource consumption</h2>
        <RangePicker options={Object.keys(RANGES) as RangeKey[]} value={range} onChange={setRange} />
      </div>
      {wide ? (
        <ProxyNotice what="Metrics" error={wide} />
      ) : (
        METRIC_PANELS.map((panel, i) => {
          const { data, error, isLoading } = results[i]
          return (
            <Section key={panel.id} title={panel.title}>
              {error ? (
                <ProxyNotice what="Metrics" error={error} />
              ) : isLoading || !data ? (
                <Spinner />
              ) : data.series.length === 0 ? (
                <p className="text-sm text-slate-500">No data for this range.</p>
              ) : (
                <TimeSeriesChart data={data} format={panel.format} />
              )}
            </Section>
          )
        })
      )}
    </div>
  )
}
