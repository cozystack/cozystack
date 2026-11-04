import type { ChartData } from "../../lib/observability/metrics.ts"

interface SeriesTableProps {
  id: string
  title: string
  data: ChartData
  format: (value: number) => string
}

const formatTime = (seconds: number) =>
  new Date(seconds * 1000).toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit" })

export function SeriesTable({ id, title, data, format }: SeriesTableProps) {
  const newestFirst = data.timestamps.map((t, i) => ({ t, i })).reverse()
  return (
    <div
      id={id}
      role="region"
      aria-label={`${title} data`}
      tabIndex={0}
      className="mt-3 max-h-64 overflow-auto rounded-md border border-slate-200 outline-none focus-visible:ring-2 focus-visible:ring-slate-400 focus-visible:ring-offset-2"
    >
      <table className="w-full text-xs tabular-nums">
        <caption className="sr-only">{`${title}, newest first`}</caption>
        <thead className="sticky top-0 bg-slate-50 text-slate-500">
          <tr>
            <th scope="col" className="px-3 py-1.5 text-left font-medium">Time</th>
            {data.series.map((s, j) => (
              <th key={j} scope="col" className="px-3 py-1.5 text-right font-medium">{s.label}</th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 text-slate-700">
          {newestFirst.map(({ t, i }) => (
            <tr key={t}>
              <th scope="row" className="px-3 py-1 text-left font-normal whitespace-nowrap">{formatTime(t)}</th>
              {data.series.map((s, j) => {
                const v = s.values[i]
                return <td key={j} className="px-3 py-1 text-right">{v === null ? "–" : format(v)}</td>
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
