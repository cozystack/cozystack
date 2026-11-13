import { describe, it, expect, vi } from "vitest"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { RangePicker } from "./RangePicker.tsx"

describe("RangePicker", () => {
  it("groups the ranges under one name and marks the chosen one pressed", () => {
    render(<RangePicker options={["1h", "6h"]} value="6h" onChange={() => {}} />)

    const group = screen.getByRole("group", { name: "Time range" })
    expect(within(group).getByRole("button", { name: "6h" })).toHaveAttribute("aria-pressed", "true")
    expect(within(group).getByRole("button", { name: "1h" })).toHaveAttribute("aria-pressed", "false")
  })

  it("reports the range clicked", async () => {
    const onChange = vi.fn()
    render(<RangePicker options={["1h", "6h"]} value="6h" onChange={onChange} />)

    await userEvent.click(screen.getByRole("button", { name: "1h" }))

    expect(onChange).toHaveBeenCalledWith("1h")
  })
})
