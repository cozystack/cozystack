import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { Input } from "./input.tsx"

describe("Input", () => {
  it("is a labelled text box that reports what is typed", async () => {
    const onValueChange = vi.fn()
    render(<Input aria-label="Filter" defaultValue="" onValueChange={onValueChange} />)

    await userEvent.type(screen.getByRole("textbox", { name: "Filter" }), "x")

    expect(onValueChange).toHaveBeenCalledWith("x", expect.anything())
  })

  it("keeps the caller's classes next to its own", () => {
    render(<Input aria-label="Filter" className="font-mono" />)

    expect(screen.getByRole("textbox", { name: "Filter" })).toHaveClass("font-mono", "rounded-lg")
  })
})
