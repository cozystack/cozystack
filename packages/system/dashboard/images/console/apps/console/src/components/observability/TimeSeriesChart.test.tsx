import { describe, it, expect, vi, beforeEach } from "vitest"
import { render } from "@testing-library/react"

const created: { opts: { series: { label?: string }[] }; data: unknown[]; destroy: ReturnType<typeof vi.fn> }[] = []

vi.mock("uplot", () => ({
  default: class {
    destroy = vi.fn()
    constructor(opts: { series: { label?: string }[] }, data: unknown[]) {
      created.push({ opts, data, destroy: this.destroy })
    }
    setSize() {}
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

  it("destroys the chart on unmount", () => {
    const { unmount } = render(<TimeSeriesChart data={data} format={String} />)

    unmount()

    expect(created[0].destroy).toHaveBeenCalled()
  })
})
