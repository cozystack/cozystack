import { describe, it, expect, vi } from "vitest"
import { screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { K8sClient } from "@cozystack/k8s-client"
import { renderWithK8sProvider } from "../../test-utils/render.tsx"
import type { ChartData } from "../../lib/observability/metrics.ts"

vi.mock("./TimeSeriesChart.tsx", () => ({
  TimeSeriesChart: ({ data }: { data: ChartData }) => (
    <div data-testid="chart">{data.series.map((s) => s.label).join(",")}</div>
  ),
}))

const { MetricsView } = await import("./MetricsView.tsx")

const scope = { namespace: "tenant-a", plural: "postgreses", name: "db" }

function matrix(pod: string) {
  return Response.json({
    status: "success",
    data: { resultType: "matrix", result: [{ metric: { pod }, values: [[1, "1"]] }] },
  })
}

function clientWith(handler: (url: URL) => Response | Promise<Response>) {
  const client = new K8sClient()
  const raw = vi.spyOn(client, "raw").mockImplementation(async (path) => handler(new URL(path, "http://x")))
  return { client, raw }
}

describe("MetricsView", () => {
  it("draws CPU, memory and network from the application root", async () => {
    const { client, raw } = clientWith(() => matrix("db-0"))

    renderWithK8sProvider(<MetricsView scope={scope} />, { client })

    await waitFor(() => expect(screen.getAllByTestId("chart")).toHaveLength(3))
    expect(screen.getByText("CPU (cores)")).toBeInTheDocument()
    expect(screen.getByText("Memory (working set)")).toBeInTheDocument()
    expect(screen.getByText("Network")).toBeInTheDocument()
    for (const [path] of raw.mock.calls) {
      expect(path.startsWith("/apis/apps.cozystack.io/v1alpha1/namespaces/tenant-a/postgreses/db/metrics/api/v1/query_range?")).toBe(true)
    }
  })

  it("requeries with a coarser step when a longer range is picked", async () => {
    const { client, raw } = clientWith(() => matrix("db-0"))
    renderWithK8sProvider(<MetricsView scope={scope} />, { client })
    await waitFor(() => expect(raw).toHaveBeenCalled())

    await userEvent.click(screen.getByRole("button", { name: "7d" }))

    await waitFor(() =>
      expect(raw.mock.calls.some(([p]) => new URL(p, "http://x").searchParams.get("step") === "2520")).toBe(true),
    )
  })

  it("shows one quiet notice instead of three errors when the proxy is absent", async () => {
    const { client } = clientWith(() => new Response("404 page not found", { status: 404 }))

    renderWithK8sProvider(<MetricsView scope={scope} />, { client })

    expect(await screen.findByText("Metrics are not available on this cluster.")).toBeInTheDocument()
    expect(screen.getAllByRole("status")).toHaveLength(1)
    expect(screen.queryByText("CPU (cores)")).toBeNull()
  })

  it("keeps the other panels when one query hits a limit", async () => {
    const { client } = clientWith((url) =>
      url.searchParams.get("query")?.includes("memory")
        ? Response.json({ status: "error", error: "too many series" }, { status: 422 })
        : matrix("db-0"),
    )

    renderWithK8sProvider(<MetricsView scope={scope} />, { client })

    expect(await screen.findByText("too many series. Choose a shorter range.")).toBeInTheDocument()
    await waitFor(() => expect(screen.getAllByTestId("chart")).toHaveLength(2))
  })

  it("says so when a panel has no data", async () => {
    const { client } = clientWith(() => Response.json({ status: "success", data: { resultType: "matrix", result: [] } }))

    renderWithK8sProvider(<MetricsView scope={scope} />, { client })

    await waitFor(() => expect(screen.getAllByText("No data for this range.")).toHaveLength(3))
    for (const status of screen.getAllByRole("status")) expect(status).toHaveClass("h-[230px]")
  })

  it("suggests a longer range only while one exists", async () => {
    const { client } = clientWith(() => Response.json({ status: "success", data: { resultType: "matrix", result: [] } }))
    renderWithK8sProvider(<MetricsView scope={scope} />, { client })
    await waitFor(() => expect(screen.getAllByText("Try a longer range.")).toHaveLength(3))

    await userEvent.click(screen.getByRole("button", { name: "7d" }))

    await waitFor(() => expect(screen.getAllByText("No data for this range.")).toHaveLength(3))
    expect(screen.queryByText("Try a longer range.")).toBeNull()
  })

  it("holds each chart's place while it loads", () => {
    const { client } = clientWith(() => new Promise<Response>(() => {}))

    renderWithK8sProvider(<MetricsView scope={scope} />, { client })

    const statuses = screen.getAllByRole("status")
    expect(statuses).toHaveLength(3)
    for (const status of statuses) {
      expect(status).toHaveAttribute("aria-busy", "true")
      expect(status).toHaveClass("h-[230px]")
    }
  })
})
