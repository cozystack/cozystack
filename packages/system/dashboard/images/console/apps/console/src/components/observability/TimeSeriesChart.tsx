import { useEffect, useRef } from "react"
import uPlot from "uplot"
import "uplot/dist/uPlot.min.css"
import type { ChartData } from "../../lib/observability/metrics.ts"

const COLORS = ["#2563eb", "#16a34a", "#d97706", "#9333ea", "#dc2626", "#0891b2", "#4b5563", "#db2777"]
const HEIGHT = 200

interface TimeSeriesChartProps {
  data: ChartData
  format: (value: number) => string
}

export function TimeSeriesChart({ data, format }: TimeSeriesChartProps) {
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const el = ref.current
    if (!el) return
    const fmt = (v: number | null) => (v === null ? "–" : format(v))
    const chart = new uPlot(
      {
        width: el.clientWidth || 600,
        height: HEIGHT,
        scales: { x: { time: true } },
        legend: { live: true },
        series: [
          {},
          ...data.series.map((s, i) => ({
            label: s.label,
            stroke: COLORS[i % COLORS.length],
            width: 1.5,
            value: (_: uPlot, v: number | null) => fmt(v),
          })),
        ],
        axes: [{}, { size: 84, values: (_: uPlot, splits: number[]) => splits.map(format) }],
      },
      [data.timestamps, ...data.series.map((s) => s.values)],
      el,
    )
    if (typeof ResizeObserver === "undefined") return () => chart.destroy()
    const observer = new ResizeObserver(() => chart.setSize({ width: el.clientWidth, height: HEIGHT }))
    observer.observe(el)
    return () => {
      observer.disconnect()
      chart.destroy()
    }
  }, [data, format])

  return <div ref={ref} className="w-full" />
}
