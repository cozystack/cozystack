import { describe, it, expect, vi, beforeEach } from "vitest"
import { render } from "@testing-library/react"

type LegendValue = (u: unknown, v: number | null, sidx: number, idx: number | null) => string
type Opts = { series: { label?: string; value?: LegendValue }[] }

const created: { opts: Opts; data: unknown[]; destroy: ReturnType<typeof vi.fn> }[] = []

vi.mock("uplot", () => ({
  default: class {
    destroy = vi.fn()
    constructor(opts: Opts, data: unknown[]) {
      created.push({ opts, data, destroy: this.destroy })
    }
    setSize() {}
    static fmtDate = () => (d: Date) => d.toISOString()
  },
}))

const { TimeSeriesChart } = await import("./TimeSeriesChart.tsx")

beforeEach(() => {
  created.length = 0
})

describe("TimeSeriesChart", () => {
  const data = {
    timestamps: [1, 2],
    series: [
      { label: "db-0", values: [1, 2] },
      { label: "db-1", values: [null, 3] },
    ],
  }

  it("hands uPlot the timestamps first and one labelled series per input", () => {
    render(<TimeSeriesChart data={data} format={String} />)

    expect(created).toHaveLength(1)
    expect(created[0].data).toEqual([[1, 2], [1, 2], [null, 3]])
    expect(created[0].opts.series.slice(1).map((s) => s.label)).toEqual(["db-0", "db-1"])
  })

  it("shows each series' latest value in the legend while nothing is hovered", () => {
    render(<TimeSeriesChart data={{ ...data, timestamps: [1, 2, 3], series: [{ label: "db-0", values: [1, 2, null] }] }} format={(v) => `${v} B`} />)

    const legend = created[0].opts.series[1].value!

    expect(legend(null, null, 1, null)).toBe("2 B")
    expect(legend(null, 1, 1, 0)).toBe("1 B")
    expect(legend(null, null, 1, 2)).toBe("–")
  })

  it("labels the legend's time as latest while nothing is hovered", () => {
    render(<TimeSeriesChart data={data} format={String} />)

    const time = created[0].opts.series[0].value!

    expect(time(null, null, 0, null)).toBe("latest")
    expect(time(null, 2, 0, 1)).toBe(new Date(2000).toISOString())
  })

  it("destroys the chart on unmount", () => {
    const { unmount } = render(<TimeSeriesChart data={data} format={String} />)

    unmount()

    expect(created[0].destroy).toHaveBeenCalled()
  })
})
