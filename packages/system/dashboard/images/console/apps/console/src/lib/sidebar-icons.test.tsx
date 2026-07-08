import { describe, it, expect } from "vitest"
import { render } from "@testing-library/react"
import { simpleIconSlug, lucideIcon, simpleIconComponent } from "./sidebar-icons.tsx"

describe("simpleIconSlug", () => {
  it("returns a Simple Icons slug for a known brand kind", () => {
    expect(simpleIconSlug("Postgres")).toBe("postgresql")
  })

  it("returns undefined for a kind with no brand logo", () => {
    expect(simpleIconSlug("Tenant")).toBeUndefined()
  })

  it("returns undefined for Bucket — Amazon S3 was removed from Simple Icons", () => {
    expect(simpleIconSlug("Bucket")).toBeUndefined()
  })
})

describe("lucideIcon", () => {
  it("provides a Lucide fallback for Bucket", () => {
    expect(lucideIcon("Bucket")).toBeDefined()
  })
})

describe("simpleIconComponent", () => {
  it("renders the icon from a bundled data URI, never a CDN", () => {
    const slug = simpleIconSlug("Postgres")
    if (!slug) throw new Error("expected Postgres to have a slug")
    const Icon = simpleIconComponent(slug)
    const { container } = render(<Icon />)

    const style = container.querySelector("span")?.getAttribute("style") ?? ""
    expect(style).toContain("data:image/svg+xml")
    // The mask must be a local data URI — no remote fetch of any kind.
    expect(style).not.toContain("jsdelivr")
    expect(style).not.toContain("cdn")
    expect(style).not.toContain("https://")
  })

  it("renders an empty placeholder for an unknown slug instead of throwing", () => {
    const Icon = simpleIconComponent("definitely-not-a-real-slug")
    const { container } = render(<Icon />)
    const span = container.querySelector("span")
    expect(span).not.toBeNull()
    expect(span?.getAttribute("style") ?? "").not.toContain("data:image/svg+xml")
  })
})
