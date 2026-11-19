import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { EmptyState } from "./EmptyState.tsx"

describe("EmptyState", () => {
  it("announces its message as a status", () => {
    render(<EmptyState title="No data for this range." hint="Pick a longer range." />)

    expect(screen.getByRole("status")).toHaveTextContent("No data for this range.Pick a longer range.")
  })

  it("marks itself busy while something is loading", () => {
    render(<EmptyState busy title="Loading…" />)

    expect(screen.getByRole("status")).toHaveAttribute("aria-busy", "true")
  })
})
