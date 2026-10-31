import { describe, it, expect } from "vitest"
import { render } from "@testing-library/react"
import { MemoryRouter } from "react-router"
import { Header, AppShell } from "@cozystack/ui"

function renderHeader(props: Record<string, unknown> = {}) {
  return render(
    <MemoryRouter>
      <Header {...props} />
    </MemoryRouter>,
  )
}

// The branding logo is rendered with an explicit pixel height so a square logo
// is not stuck at the horizontal logo's height. The height is configurable
// (AppConfig.logoHeight) and clamped so a bad value can't deform the header.
describe("Header logo sizing", () => {
  it("renders the default logo at the default height", () => {
    const { container } = renderHeader()
    expect(container.querySelector("img")?.style.height).toBe("36px")
  })

  it("applies a configured logoHeight so a square logo is not rendered tiny", () => {
    const { container } = renderHeader({ logoHeight: 48 })
    expect(container.querySelector("img")?.style.height).toBe("48px")
  })

  it("clamps an unreasonable logoHeight instead of blowing up the header", () => {
    const { container } = renderHeader({ logoHeight: 999 })
    expect(container.querySelector("img")?.style.height).toBe("80px")
  })

  it("ignores a non-numeric logoHeight and falls back to the default", () => {
    const { container } = renderHeader({ logoHeight: Number.NaN })
    expect(container.querySelector("img")?.style.height).toBe("36px")
  })

  it("coerces a numeric-string logoHeight (branding config.json ships it as a string)", () => {
    const { container } = renderHeader({ logoHeight: "48" })
    expect(container.querySelector("img")?.style.height).toBe("48px")
  })

  it("treats a blank-string logoHeight as unset, not zero", () => {
    // `pick` keeps an empty branding.logoHeight, so config.json may carry "".
    // Number("") is 0, which would clamp to the 16px minimum — fall back to 36.
    for (const blank of ["", "   "]) {
      const { container } = renderHeader({ logoHeight: blank })
      expect(container.querySelector("img")?.style.height).toBe("36px")
    }
  })

  it("passes logoHeight through AppShell to the header logo", () => {
    const { container } = render(
      <MemoryRouter>
        <AppShell sections={[]} logoHeight={64} />
      </MemoryRouter>,
    )
    expect(container.querySelector("img")?.style.height).toBe("64px")
  })
})
