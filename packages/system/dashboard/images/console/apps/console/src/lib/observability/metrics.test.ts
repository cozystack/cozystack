import { describe, it, expect } from "vitest"
import {
  METRIC_PANELS,
  formatBytes,
  formatCores,
  rangeWindow,
  toChartData,
} from "./metrics.ts"

describe("rangeWindow", () => {
  it("ends now and spans the range in about 240 points", () => {
    const w = rangeWindow("6h", 1_000_000)

    expect(w.end).toBe(1_000_000)
    expect(w.start).toBe(1_000_000 - 6 * 3600)
    expect(w.step).toBe(90)
  })

  it("never steps finer than 15s", () => {
    expect(rangeWindow("1h", 0).step).toBe(15)
  })
})

describe("METRIC_PANELS", () => {
  const window = { start: 0, end: 3600, step: 15 }

  it("rates CPU over at least five minutes so sparse scrapes still draw", () => {
    const [query] = METRIC_PANELS.find((p) => p.id === "cpu")!.queries(window)

    expect(query.expr).toBe(
      'sum by (pod) (rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[300s]))',
    )
  })

  // Long ranges are served by the longterm store, deduplicated at 5m, where a
  // narrower window can hold no samples and the rate comes back empty.
  it.each([
    [15, "300s"],
    [75, "300s"],
    [90, "360s"],
    [2520, "10080s"],
  ])("rates over max(5m, 4 x step) for step %is", (step, window) => {
    for (const panel of METRIC_PANELS) {
      for (const q of panel.queries({ start: 0, end: 1, step })) {
        if (q.expr.includes("rate(")) expect(q.expr).toContain(`[${window}]`)
      }
    }
  })

  it("reads memory as working set per pod", () => {
    const [query] = METRIC_PANELS.find((p) => p.id === "memory")!.queries(window)

    expect(query.expr).toBe('sum by (pod) (container_memory_working_set_bytes{container!="",container!="POD"})')
  })

  it("draws network receive and transmit as two named series", () => {
    const queries = METRIC_PANELS.find((p) => p.id === "network")!.queries(window)

    expect(queries.map((q) => q.label)).toEqual(["receive", "transmit"])
    expect(queries[0].expr).toBe("sum(rate(container_network_receive_bytes_total[300s]))")
    expect(queries[1].expr).toBe("sum(rate(container_network_transmit_bytes_total[300s]))")
  })

  it("names no application kind in any query", () => {
    for (const panel of METRIC_PANELS) {
      for (const q of panel.queries(window)) expect(q.expr).not.toMatch(/apps_cozystack_io|namespace=/)
    }
  })
})

describe("toChartData", () => {
  it("aligns series on the union of timestamps and leaves gaps as null", () => {
    const data = toChartData([
      { label: "a", series: [{ metric: {}, values: [[10, "1"], [20, "2"]] }] },
      { label: "b", series: [{ metric: {}, values: [[20, "5"], [30, "6"]] }] },
    ])

    expect(data.timestamps).toEqual([10, 20, 30])
    expect(data.series).toEqual([
      { label: "a", values: [1, 2, null] },
      { label: "b", values: [null, 5, 6] },
    ])
  })

  it("labels each series by its pod, falling back to the query label", () => {
    const data = toChartData([
      {
        label: "cpu",
        series: [
          { metric: { pod: "db-1" }, values: [[1, "0.5"]] },
          { metric: {}, values: [[1, "0.1"]] },
        ],
      },
    ])

    expect(data.series.map((s) => s.label)).toEqual(["db-1", "cpu"])
  })

  it("drops NaN samples instead of drawing them as zero", () => {
    const data = toChartData([{ label: "x", series: [{ metric: {}, values: [[1, "NaN"]] }] }])

    expect(data.series[0].values).toEqual([null])
  })
})

describe("formatters", () => {
  it("formats CPU in millicores below one core", () => {
    expect(formatCores(0.25)).toBe("250m")
    expect(formatCores(2)).toBe("2")
  })

  it("formats bytes with binary units", () => {
    expect(formatBytes(512)).toBe("512 B")
    expect(formatBytes(3 * 1024 * 1024)).toBe("3.0 MiB")
  })
})
