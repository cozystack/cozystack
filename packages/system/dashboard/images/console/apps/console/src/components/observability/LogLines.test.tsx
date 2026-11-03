import { describe, it, expect } from "vitest"
import { fireEvent, render, screen } from "@testing-library/react"
import { LogLines } from "./LogLines.tsx"

const entry = (n: number) => ({ _time: `2026-11-04T10:00:0${n}Z`, _msg: `line ${n}`, _stream_id: "s" })

function sized(list: HTMLElement, scrollHeight: number) {
  Object.defineProperty(list, "scrollHeight", { configurable: true, value: scrollHeight })
  Object.defineProperty(list, "clientHeight", { configurable: true, value: 100 })
}

describe("LogLines", () => {
  it("keeps following the newest line while the reader is at the bottom", () => {
    const { rerender } = render(<LogLines entries={[entry(1)]} />)
    const list = screen.getByRole("list")
    sized(list, 500)

    rerender(<LogLines entries={[entry(1), entry(2)]} />)

    expect(list.scrollTop).toBe(500)
  })

  it("leaves the position alone once the reader scrolls up", () => {
    const { rerender } = render(<LogLines entries={[entry(1)]} />)
    const list = screen.getByRole("list")
    sized(list, 500)
    list.scrollTop = 120
    fireEvent.scroll(list)

    rerender(<LogLines entries={[entry(1), entry(2)]} />)

    expect(list.scrollTop).toBe(120)
  })

  it("follows again once the list is cleared for a new run", () => {
    const { rerender } = render(<LogLines entries={[entry(1)]} />)
    const first = screen.getByRole("list")
    sized(first, 500)
    first.scrollTop = 120
    fireEvent.scroll(first)

    rerender(<LogLines entries={[]} />)
    rerender(<LogLines entries={[entry(2)]} />)
    const second = screen.getByRole("list")
    sized(second, 300)
    rerender(<LogLines entries={[entry(2), entry(3)]} />)

    expect(second.scrollTop).toBe(300)
  })
})
