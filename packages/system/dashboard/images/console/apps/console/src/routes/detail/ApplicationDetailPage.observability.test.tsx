import { describe, it, expect, vi, beforeEach } from "vitest"
import { act, render, screen, waitFor } from "@testing-library/react"
import { createMemoryRouter, RouterProvider } from "react-router"
import { K8sProvider } from "@cozystack/k8s-client"
import { TenantContext, type TenantContextValue } from "../../lib/tenant-context.tsx"
import type { ChartData } from "../../lib/observability/metrics.ts"

// Keep the consoles' and the chart's browser-only libraries out of jsdom; the
// Logs and Metrics tabs, the page and the providers stay real.
vi.mock("./VncTab.tsx", () => ({ VncTab: () => null }))
vi.mock("./SerialTab.tsx", () => ({ SerialTab: () => null }))
vi.mock("../../components/observability/TimeSeriesChart.tsx", () => ({
  TimeSeriesChart: ({ data }: { data: ChartData }) => (
    <div data-testid="chart">{data.series.map((s) => s.label).join(",")}</div>
  ),
}))

const { ApplicationDetailPage } = await import("./ApplicationDetailPage.tsx")

const NS = "tenant-test"
const APPS = `/apis/apps.cozystack.io/v1alpha1/namespaces/${NS}/postgreses`

const tenant: TenantContextValue = {
  tenants: [],
  selectedTenant: "test",
  selectTenant: () => {},
  tenantNamespace: NS,
  isLoading: false,
  error: null,
}

const definitions = {
  items: [{ metadata: { name: "postgres" }, spec: { application: { plural: "postgreses", kind: "Postgres" } } }],
}

const instance = (name: string) => ({
  apiVersion: "apps.cozystack.io/v1alpha1",
  kind: "Postgres",
  metadata: { name, namespace: NS },
})

const logLine = (name: string) =>
  `${JSON.stringify({ _time: "2026-11-04T10:00:01Z", _msg: `${name} is ready`, _stream_id: "s", kubernetes_pod_name: `${name}-0`, kubernetes_container_name: "postgres" })}\n`

const matrix = (name: string) =>
  Response.json({ status: "success", data: { resultType: "matrix", result: [{ metric: { pod: `${name}-0` }, values: [[1, "1"]] }] } })

// app-b's own GET never answers, so the page keeps the placeholder of app-a
// that the production query defaults hand it; its proxy reads do answer.
function handle(url: URL): Response | Promise<Response> {
  const path = url.pathname
  if (path.endsWith("/applicationdefinitions")) return Response.json(definitions)
  if (path === `${APPS}/app-a`) return Response.json(instance("app-a"))
  if (path === `${APPS}/app-b`) return new Promise(() => {})
  const app = path.match(/\/postgreses\/(app-[ab])\//)?.[1]
  if (app && path.endsWith("/select/logsql/query")) return new Response(logLine(app))
  if (app && path.endsWith("/stream_field_values")) return Response.json({ values: [] })
  if (app && path.includes("/metrics/api/v1/query_range")) return matrix(app)
  return Response.json({ items: [] })
}

const fetchMock = vi.fn(async (input: RequestInfo | URL) => handle(new URL(String(input), "http://x")))

function renderAt(path: string) {
  const router = createMemoryRouter(
    [{ path: "/console/:plural/:name/*", element: <ApplicationDetailPage /> }],
    { initialEntries: [path] },
  )
  // No queryClient: the provider's production defaults keep a query's previous
  // result while its key changes, which is what exposes the stale instance.
  render(
    <K8sProvider>
      <TenantContext.Provider value={tenant}>
        <RouterProvider router={router} />
      </TenantContext.Provider>
    </K8sProvider>,
  )
  return { go: (to: string) => act(() => router.navigate(to)) }
}

const requested = (prefix: string) =>
  fetchMock.mock.calls.some(([input]) => new URL(String(input), "http://x").pathname.startsWith(prefix))

beforeEach(() => {
  vi.stubGlobal("fetch", fetchMock)
  fetchMock.mockClear()
})

describe("ApplicationDetailPage observability while the next application loads", () => {
  it("shows the selected application's logs, not the previous one's", async () => {
    const { go } = renderAt("/console/postgreses/app-a/logs")
    expect(await screen.findByText("app-a is ready")).toBeInTheDocument()

    await go("/console/postgreses/app-b/logs")

    expect(screen.getByRole("heading", { name: "app-b" })).toBeInTheDocument()
    expect(screen.queryByText("app-a is ready")).toBeNull()
    await waitFor(() => expect(requested(`${APPS}/app-b/log/`)).toBe(true))
    expect(await screen.findByText("app-b is ready")).toBeInTheDocument()
  })

  it("shows the selected application's metrics, not the previous one's", async () => {
    const { go } = renderAt("/console/postgreses/app-a/metrics")
    await waitFor(() => expect(screen.getAllByTestId("chart")[0]).toHaveTextContent("app-a-0"))

    await go("/console/postgreses/app-b/metrics")

    expect(screen.getByRole("heading", { name: "app-b" })).toBeInTheDocument()
    expect(screen.queryAllByText("app-a-0", { exact: false })).toEqual([])
    await waitFor(() => expect(requested(`${APPS}/app-b/metrics/`)).toBe(true))
    await waitFor(() => expect(screen.getAllByTestId("chart")[0]).toHaveTextContent("app-b-0"))
  })
})
