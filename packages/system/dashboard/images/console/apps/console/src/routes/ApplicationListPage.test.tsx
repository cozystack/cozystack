import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"
import { MemoryRouter, Routes, Route } from "react-router"

// Drive the two upstream hooks per test; the page's guards are the unit under test.
const h = vi.hoisted(() => ({
  defs: { data: undefined as unknown, isLoading: true },
  instances: { data: undefined as unknown, isLoading: false },
}))

vi.mock("../lib/app-definitions.ts", () => ({
  useApplicationDefinitions: () => h.defs,
  useApplicationInstances: () => h.instances,
  iconDataUrl: () => null,
  appDisplayName: () => "App",
}))
vi.mock("../lib/tenant-context.tsx", () => ({
  useTenantContext: () => ({ tenantNamespace: "tenant-test", selectedTenant: "test" }),
}))

const { ApplicationListPage } = await import("./ApplicationListPage.tsx")

function renderAt(plural: string) {
  return render(
    <MemoryRouter initialEntries={[`/console/${plural}`]}>
      <Routes>
        <Route path="/console/:plural" element={<ApplicationListPage />} />
      </Routes>
    </MemoryRouter>,
  )
}

describe("ApplicationListPage guards", () => {
  it("shows the unknown-type message once defs have loaded and the plural is unrecognized", () => {
    // Regression: previously `defsLoading || (!ad && plural)` kept the spinner
    // up forever for an unknown plural, so this branch was unreachable.
    h.defs = { data: { items: [] }, isLoading: false }
    renderAt("nonexistent")
    expect(screen.getByText("Unknown application type.")).toBeInTheDocument()
    expect(screen.queryByText("Loading…")).not.toBeInTheDocument()
  })

  it("still shows the spinner while definitions are loading", () => {
    h.defs = { data: undefined, isLoading: true }
    renderAt("postgreses")
    expect(screen.getByText("Loading…")).toBeInTheDocument()
    expect(screen.queryByText("Unknown application type.")).not.toBeInTheDocument()
  })
})
