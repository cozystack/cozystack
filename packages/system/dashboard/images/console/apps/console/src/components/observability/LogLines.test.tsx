import { describe, it, expect } from "vitest"
import { fireEvent, render, screen } from "@testing-library/react"
import { LogLines } from "./LogLines.tsx"

const entry = (n: number) => ({ _time: `2026-11-04T10:00:0${n}Z`, _msg: `line ${n}`, _stream_id: "s" })

function sized(list: HTMLElement, scrollHeight: number) {
  Object.defineProperty(list, "scrollHeight", { configurable: true, value: scrollHeight })
  Object.defineProperty(list, "clientHeight", { configurable: true, value: 100 })
}

describe("LogLines", () => {
  it("keeps the console frame and shows the placeholder inside it when there are no lines", () => {
    render(<LogLines entries={[]} placeholder="Waiting for new log lines…" />)

    const frame = screen.getByRole("region", { name: "Log lines" })
    expect(frame).toHaveTextContent("Waiting for new log lines…")
    expect(screen.queryByRole("list")).toBeNull()
  })

  it("puts the lines in the same frame once they arrive", () => {
    const { rerender } = render(<LogLines entries={[]} placeholder="Waiting for new log lines…" />)
    const frame = screen.getByRole("region", { name: "Log lines" })

    rerender(<LogLines entries={[entry(1)]} placeholder="Waiting for new log lines…" />)

    expect(screen.getByRole("region", { name: "Log lines" })).toBe(frame)
    expect(frame).toContainElement(screen.getByRole("list"))
    expect(frame).not.toHaveTextContent("Waiting")
  })

  it("keeps following the newest line while the reader is at the bottom", () => {
    const { rerender } = render(<LogLines placeholder="" entries={[entry(1)]} />)
    const list = screen.getByRole("list")
    sized(list, 500)

    rerender(<LogLines placeholder="" entries={[entry(1), entry(2)]} />)

    expect(list.scrollTop).toBe(500)
  })

  it("leaves the position alone once the reader scrolls up", () => {
    const { rerender } = render(<LogLines placeholder="" entries={[entry(1)]} />)
    const list = screen.getByRole("list")
    sized(list, 500)
    list.scrollTop = 120
    fireEvent.scroll(list)

    rerender(<LogLines placeholder="" entries={[entry(1), entry(2)]} />)

    expect(list.scrollTop).toBe(120)
  })

  it("follows again once the list is cleared for a new run", () => {
    const { rerender } = render(<LogLines placeholder="" entries={[entry(1)]} />)
    const first = screen.getByRole("list")
    sized(first, 500)
    first.scrollTop = 120
    fireEvent.scroll(first)

    rerender(<LogLines placeholder="" entries={[]} />)
    rerender(<LogLines placeholder="" entries={[entry(2)]} />)
    const second = screen.getByRole("list")
    sized(second, 300)
    rerender(<LogLines placeholder="" entries={[entry(2), entry(3)]} />)

    expect(second.scrollTop).toBe(300)
  })
})
