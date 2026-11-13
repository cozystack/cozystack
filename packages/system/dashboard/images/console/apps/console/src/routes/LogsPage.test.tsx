import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

vi.mock("../lib/tenant-context.tsx", () => ({
  useTenantContext: () => ({ tenantNamespace: "tenant-a" }),
}))
vi.mock("../components/observability/LogsView.tsx", () => ({
  LogsView: ({ scope }: { scope: object }) => <pre data-testid="scope">{JSON.stringify(scope)}</pre>,
}))

const { LogsPage } = await import("./LogsPage.tsx")

describe("LogsPage", () => {
  it("reads logs from the selected tenant's namespace root", () => {
    render(<LogsPage />)

    expect(screen.getByRole("heading", { name: "Logs" })).toBeInTheDocument()
    expect(JSON.parse(screen.getByTestId("scope").textContent ?? "")).toEqual({ namespace: "tenant-a" })
  })
})
