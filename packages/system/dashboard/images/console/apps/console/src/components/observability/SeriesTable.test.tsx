import { describe, it, expect } from "vitest"
import { render, screen, within } from "@testing-library/react"
import { SeriesTable } from "./SeriesTable.tsx"

const data = {
  timestamps: [1_700_000_000, 1_700_000_060],
  series: [
    { label: "db-0", values: [1, 2] },
    { label: "db-1", values: [null, 3] },
  ],
}

describe("SeriesTable", () => {
  it("is a named region the keyboard can reach", () => {
    render(<SeriesTable id="t" title="Memory" data={data} format={String} />)

    const region = screen.getByRole("region", { name: "Memory data" })
    expect(region).toHaveAttribute("id", "t")
    expect(region).toHaveAttribute("tabindex", "0")
  })

  it("has a time column and one column per series", () => {
    render(<SeriesTable id="t" title="Memory" data={data} format={String} />)

    expect(screen.getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Time", "db-0", "db-1"])
  })

  it("lists every sample newest first in the chart's units", () => {
    render(<SeriesTable id="t" title="Memory" data={data} format={(v) => `${v} B`} />)

    const rows = screen.getAllByRole("row").slice(1)
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getAllByRole("cell").map((c) => c.textContent)).toEqual(["2 B", "3 B"])
    expect(within(rows[1]).getAllByRole("cell").map((c) => c.textContent)).toEqual(["1 B", "–"])
    expect(within(rows[0]).getByRole("rowheader")).toHaveTextContent(new Date(1_700_000_060_000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" }))
  })
})
