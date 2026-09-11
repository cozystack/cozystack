import { describe, it, expect } from "vitest"
import type { MouseEvent } from "react"
import { navigatesThisTab } from "./links.ts"

function click(overrides: Partial<MouseEvent> = {}): MouseEvent {
  return {
    button: 0,
    metaKey: false,
    altKey: false,
    ctrlKey: false,
    shiftKey: false,
    ...overrides,
  } as MouseEvent
}

describe("navigatesThisTab", () => {
  it("accepts a plain primary click", () => {
    expect(navigatesThisTab(click())).toBe(true)
  })

  // Each of these hands the navigation to a new tab or window, so the tab the
  // click happened in keeps whatever tenant it was showing.
  it.each(["metaKey", "ctrlKey", "shiftKey", "altKey"] as const)(
    "refuses a click modified with %s",
    (modifier) => {
      expect(navigatesThisTab(click({ [modifier]: true }))).toBe(false)
    },
  )

  it("refuses a non-primary button", () => {
    expect(navigatesThisTab(click({ button: 1 }))).toBe(false)
  })
})
