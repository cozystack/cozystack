import { describe, it, expect, vi } from "vitest"
import { screen, waitFor } from "@testing-library/react"
import { Route, Routes } from "react-router"
import { K8sClient, K8sApiError } from "@cozystack/k8s-client"
import { renderWithK8sProvider } from "../test-utils/render.tsx"
import { CapacityAdminGuard } from "./CapacityAdminGuard.tsx"

type SsarOutcome = { allowed: boolean } | "pending" | K8sApiError

function makeClient(outcome: SsarOutcome): K8sClient {
  const client = new K8sClient({ baseUrl: "/mock" })
  vi.spyOn(client, "create").mockImplementation(async (_g, _v, _p, body) => {
    if (outcome === "pending") return new Promise(() => {}) as never
    if (outcome instanceof K8sApiError) throw outcome
    return { ...(body as object), status: { allowed: outcome.allowed } }
  })
  return client
}

function renderGuard(client: K8sClient) {
  return renderWithK8sProvider(
    <Routes>
      <Route element={<CapacityAdminGuard />}>
        <Route path="cap" element={<div>CAPACITY CONTENT</div>} />
      </Route>
    </Routes>,
    { client, initialRoute: "/cap" },
  )
}

describe("CapacityAdminGuard", () => {
  it("renders the child route when nodes/list is allowed", async () => {
    renderGuard(makeClient({ allowed: true }))
    await waitFor(() =>
      expect(screen.getByText("CAPACITY CONTENT")).toBeInTheDocument(),
    )
  })

  it("renders permission-denied instead of the child route when denied", async () => {
    renderGuard(makeClient({ allowed: false }))
    await waitFor(() =>
      expect(
        screen.getByText(/do not have permission to view cluster capacity/i),
      ).toBeInTheDocument(),
    )
    expect(screen.queryByText("CAPACITY CONTENT")).not.toBeInTheDocument()
    expect(screen.getByRole("link", { name: /back to console/i })).toHaveAttribute(
      "href",
      "/console",
    )
  })

  it("fails closed (denied) on SSAR error", async () => {
    renderGuard(makeClient(new K8sApiError(500, "boom")))
    await waitFor(() =>
      expect(
        screen.getByText(/do not have permission to view cluster capacity/i),
      ).toBeInTheDocument(),
    )
    expect(screen.queryByText("CAPACITY CONTENT")).not.toBeInTheDocument()
  })

  it("shows neither content nor denial while the review is loading", () => {
    renderGuard(makeClient("pending"))
    expect(screen.queryByText("CAPACITY CONTENT")).not.toBeInTheDocument()
    expect(
      screen.queryByText(/do not have permission to view cluster capacity/i),
    ).not.toBeInTheDocument()
  })
})
