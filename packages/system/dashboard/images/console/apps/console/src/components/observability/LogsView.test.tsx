import { describe, it, expect, vi } from "vitest"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { K8sClient, K8sProvider } from "@cozystack/k8s-client"
import { renderWithK8sProvider } from "../../test-utils/render.tsx"
import { LogsView } from "./LogsView.tsx"

const app = { namespace: "tenant-a", plural: "postgreses", name: "db" }
const ROOT = "/apis/apps.cozystack.io/v1alpha1/namespaces/tenant-a/postgreses/db/log/"

const line = (time: string, msg: string, pod = "db-0") =>
  JSON.stringify({ _time: time, _msg: msg, _stream_id: "s", kubernetes_pod_name: pod, kubernetes_container_name: "postgres" })

function clientWith(handler: (endpoint: string, params: URLSearchParams) => Response | Promise<Response>) {
  const client = new K8sClient()
  const raw = vi.spyOn(client, "raw").mockImplementation(async (path) => {
    const url = new URL(path, "http://x")
    return handler(url.pathname, url.searchParams)
  })
  return { client, raw }
}

const values = (...v: string[]) => Response.json({ values: v.map((value) => ({ value, hits: 1 })) })

function defaultHandler(endpoint: string, params?: URLSearchParams): Response | Promise<Response> {
  if (endpoint.endsWith("stream_field_values")) {
    return params?.get("field") === "kubernetes_pod_name" ? values("db-0", "db-1") : values("postgres")
  }
  if (endpoint.endsWith("query")) return new Response(`${line("2026-11-04T10:00:01Z", "ready to accept connections")}\n`)
  return new Promise(() => {})
}

// The provider's own QueryClient keeps the previous result while a key changes; it is
// shared across tests, so each test below uses namespaces no other test reads.
function renderWithProductionDefaults(client: K8sClient, namespace: string) {
  const view = (ns: string) => (
    <K8sProvider client={client}>
      <LogsView scope={{ namespace: ns }} />
    </K8sProvider>
  )
  const result = render(view(namespace))
  return { switchTo: (ns: string) => result.rerender(view(ns)) }
}

describe("LogsView with the production query defaults", () => {
  it("drops the previous tenant's lines and pods while the next tenant loads", async () => {
    const { client } = clientWith((endpoint, params) =>
      endpoint.includes("/tenant-switch-b/") ? new Promise(() => {}) : defaultHandler(endpoint, params),
    )
    const { switchTo } = renderWithProductionDefaults(client, "tenant-switch-a")
    expect(await screen.findByText("ready to accept connections")).toBeInTheDocument()
    await userEvent.click(screen.getByRole("combobox", { name: "Pod" }))
    await screen.findByRole("option", { name: "db-1" })
    await userEvent.keyboard("{Escape}")

    switchTo("tenant-switch-b")

    const frame = screen.getByRole("region", { name: "Log lines" })
    expect(within(frame).getByText("Loading log lines…")).toBeInTheDocument()
    expect(screen.queryByText("ready to accept connections")).toBeNull()
    await userEvent.click(screen.getByRole("combobox", { name: "Pod" }))
    expect((await screen.findAllByRole("option")).map((o) => o.textContent)).toEqual(["All"])
  })

  it("starts the next tenant with no filter, pod or container of the previous one", async () => {
    const { client, raw } = clientWith(defaultHandler)
    const { switchTo } = renderWithProductionDefaults(client, "tenant-reset-a")
    await userEvent.type(screen.getByLabelText("LogsQL filter"), "error{Enter}")
    await userEvent.click(screen.getByRole("combobox", { name: "Pod" }))
    await userEvent.click(await screen.findByRole("option", { name: "db-1" }))
    await waitFor(() =>
      expect(raw.mock.calls.some(([p]) => new URL(p, "http://x").searchParams.get("query") === '{kubernetes_pod_name="db-1"} (error)')).toBe(true),
    )

    switchTo("tenant-reset-b")

    await waitFor(() => expect(raw.mock.calls.some(([p]) => p.includes("/tenant-reset-b/") && p.includes("select/logsql/query?"))).toBe(true))
    const first = raw.mock.calls.find(([p]) => p.includes("/tenant-reset-b/") && p.includes("select/logsql/query?"))!
    expect(new URL(first[0], "http://x").searchParams.get("query")).toBe("*")
    expect(screen.getByRole("combobox", { name: "Pod" })).toHaveTextContent("All")
    expect(screen.getByRole("combobox", { name: "Container" })).toHaveTextContent("All")
    expect(screen.getByLabelText("LogsQL filter")).toHaveValue("")
  })

  it("keeps the current lines while a new filter in the same tenant loads", async () => {
    const { client } = clientWith((endpoint, params) =>
      endpoint.endsWith("query") && params.get("query") === "slow" ? new Promise(() => {}) : defaultHandler(endpoint, params),
    )
    renderWithProductionDefaults(client, "tenant-filter-a")
    expect(await screen.findByText("ready to accept connections")).toBeInTheDocument()

    await userEvent.type(screen.getByLabelText("LogsQL filter"), "slow{Enter}")

    expect(screen.getByText("ready to accept connections")).toBeInTheDocument()
  })

  it("says it is loading, not that nothing matched, while a longer range replaces an empty one", async () => {
    const { client } = clientWith((endpoint, params) => {
      if (!endpoint.endsWith("query")) return defaultHandler(endpoint, params)
      return Date.parse(params.get("start") ?? "") < Date.now() - 2 * 3600_000
        ? new Promise(() => {})
        : new Response("")
    })
    renderWithProductionDefaults(client, "tenant-range-a")
    const frame = () => screen.getByRole("region", { name: "Log lines" })
    expect(await within(frame()).findByText("No log lines for this range and filter.")).toBeInTheDocument()

    await userEvent.click(screen.getByRole("button", { name: "24h" }))

    await waitFor(() => expect(within(frame()).getByRole("status")).toHaveAttribute("aria-busy", "true"))
    expect(within(frame()).queryByText("No log lines for this range and filter.")).toBeNull()
  })

  it("marks the lines of the previous range as standing in while a longer range loads", async () => {
    const { client } = clientWith((endpoint, params) =>
      endpoint.endsWith("query") && Date.parse(params.get("start") ?? "") < Date.now() - 2 * 3600_000
        ? new Promise(() => {})
        : defaultHandler(endpoint, params),
    )
    renderWithProductionDefaults(client, "tenant-range-b")
    expect(await screen.findByText("ready to accept connections")).toBeInTheDocument()
    expect(screen.queryByText(/previous lines/)).toBeNull()

    await userEvent.click(screen.getByRole("button", { name: "24h" }))

    expect(await screen.findByText(/previous lines/)).toHaveAttribute("aria-busy", "true")
    expect(screen.getByText("ready to accept connections")).toBeInTheDocument()
  })
})

describe("LogsView", () => {
  it("shows recent lines from the application root", async () => {
    const { client, raw } = clientWith(defaultHandler)

    renderWithK8sProvider(<LogsView scope={app} />, { client })

    expect(await screen.findByText("ready to accept connections")).toBeInTheDocument()
    const call = raw.mock.calls.find(([p]) => p.includes("select/logsql/query?"))!
    expect(call[0].startsWith(`${ROOT}select/logsql/query?`)).toBe(true)
    const params = new URL(call[0], "http://x").searchParams
    expect(params.get("query")).toBe("*")
    expect(params.get("limit")).toBe("500")
  })

  it("narrows to the picked pod with a stream filter", async () => {
    const { client, raw } = clientWith(defaultHandler)
    renderWithK8sProvider(<LogsView scope={app} />, { client })

    await userEvent.click(screen.getByRole("combobox", { name: "Pod" }))
    await userEvent.click(await screen.findByRole("option", { name: "db-1" }))

    await waitFor(() =>
      expect(
        raw.mock.calls.some(([p]) => new URL(p, "http://x").searchParams.get("query") === '{kubernetes_pod_name="db-1"} (*)'),
      ).toBe(true),
    )
  })

  it("offers All next to every pod and starts on it", async () => {
    const { client } = clientWith(defaultHandler)
    renderWithK8sProvider(<LogsView scope={app} />, { client })
    const pod = screen.getByRole("combobox", { name: "Pod" })
    expect(pod).toHaveTextContent("All")

    await userEvent.click(pod)

    expect(await screen.findByRole("option", { name: "db-1" })).toBeInTheDocument()
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["All", "db-0", "db-1"])
  })

  it("keeps the log console and says so inside it when nothing matches", async () => {
    const { client } = clientWith((endpoint, params) =>
      endpoint.endsWith("query") ? new Response("") : defaultHandler(endpoint, params),
    )

    renderWithK8sProvider(<LogsView scope={app} />, { client })

    const frame = screen.getByRole("region", { name: "Log lines" })
    expect(await within(frame).findByText("No log lines for this range and filter.")).toBeInTheDocument()
  })

  it("suggests a longer range only while one exists", async () => {
    const { client } = clientWith((endpoint, params) =>
      endpoint.endsWith("query") ? new Response("") : defaultHandler(endpoint, params),
    )
    renderWithK8sProvider(<LogsView scope={app} />, { client })
    const frame = () => screen.getByRole("region", { name: "Log lines" })
    expect(await within(frame()).findByText("Try a longer range or a broader filter.")).toBeInTheDocument()

    await userEvent.click(screen.getByRole("button", { name: "24h" }))

    await waitFor(() => expect(within(frame()).getByText("Try a broader filter.")).toBeInTheDocument())
    expect(within(frame()).queryByText(/longer range/)).toBeNull()
  })

  it("shows loading inside the log console", () => {
    const { client } = clientWith((endpoint, params) =>
      endpoint.endsWith("query") ? new Promise(() => {}) : defaultHandler(endpoint, params),
    )

    renderWithK8sProvider(<LogsView scope={app} />, { client })

    const frame = screen.getByRole("region", { name: "Log lines" })
    expect(within(frame).getByRole("status")).toHaveAttribute("aria-busy", "true")
  })

  it("says it is waiting while Follow has no line yet", async () => {
    const { client } = clientWith(defaultHandler)
    renderWithK8sProvider(<LogsView scope={app} />, { client })

    await userEvent.click(screen.getByRole("button", { name: "Follow" }))

    const frame = screen.getByRole("region", { name: "Log lines" })
    expect(within(frame).getByText("Waiting for new log lines…")).toBeInTheDocument()
  })

  it("applies the LogsQL filter on submit", async () => {
    const { client, raw } = clientWith(defaultHandler)
    renderWithK8sProvider(<LogsView scope={app} />, { client })

    await userEvent.type(screen.getByLabelText("LogsQL filter"), "error{Enter}")

    await waitFor(() =>
      expect(raw.mock.calls.some(([p]) => p.includes("select/logsql/query?") && p.includes("query=error"))).toBe(true),
    )
  })

  it("follows the live tail when Follow is pressed", async () => {
    const { client } = clientWith((endpoint, params) =>
      endpoint.endsWith("tail") ? new Response(`${line("2026-11-04T10:00:05Z", "checkpoint complete")}\n`) : defaultHandler(endpoint, params),
    )
    renderWithK8sProvider(<LogsView scope={app} />, { client })

    await userEvent.click(screen.getByRole("button", { name: "Follow" }))

    expect(await screen.findByText("checkpoint complete")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Follow" })).toHaveAttribute("aria-pressed", "true")
  })

  it("says the tail stopped when the proxy refuses the query", async () => {
    const { client } = clientWith((endpoint, params) =>
      endpoint.endsWith("tail") ? new Response("pipe not allowed in tail", { status: 400 }) : defaultHandler(endpoint, params),
    )
    renderWithK8sProvider(<LogsView scope={app} />, { client })

    await userEvent.click(screen.getByRole("button", { name: "Follow" }))

    expect(await screen.findByText(/Stopped/)).toBeInTheDocument()
    expect(screen.getByText("pipe not allowed in tail")).toBeInTheDocument()
    const frame = screen.getByRole("region", { name: "Log lines" })
    expect(within(frame).getByText("Follow stopped before any line arrived.")).toBeInTheDocument()
  })

  it("renders a cluster without the proxy as not available", async () => {
    const { client } = clientWith(() => new Response("404 page not found", { status: 404 }))

    renderWithK8sProvider(<LogsView scope={app} />, { client })

    expect(await screen.findByText("Logs are not available on this cluster.")).toBeInTheDocument()
    expect(screen.queryByLabelText("LogsQL filter")).toBeNull()
  })

  it("renders the tenant logs page as not available when kube-apiserver answers instead of the proxy", async () => {
    const { client } = clientWith(() =>
      Response.json({ kind: "Status", apiVersion: "v1", status: "Failure", reason: "Forbidden", code: 403 }, { status: 403 }),
    )

    renderWithK8sProvider(<LogsView scope={{ namespace: "tenant-a" }} />, { client })

    expect(await screen.findByText("Logs are not available on this cluster.")).toBeInTheDocument()
  })

  it("keeps the controls and explains a rejected query", async () => {
    const { client } = clientWith((endpoint, params) =>
      endpoint.endsWith("query") ? new Response("cannot parse query", { status: 400 }) : defaultHandler(endpoint, params),
    )

    renderWithK8sProvider(<LogsView scope={app} />, { client })

    expect(await screen.findByText("cannot parse query")).toBeInTheDocument()
    expect(screen.getByLabelText("LogsQL filter")).toBeInTheDocument()
    const frame = screen.getByRole("region", { name: "Log lines" })
    expect(within(frame).getByText("No log lines to show.")).toBeInTheDocument()
  })

  it("offers All once when some lines carry no pod name", async () => {
    const { client } = clientWith((endpoint, params) =>
      endpoint.endsWith("stream_field_values") && params.get("field") === "kubernetes_pod_name"
        ? values("", "db-0")
        : defaultHandler(endpoint, params),
    )
    renderWithK8sProvider(<LogsView scope={app} />, { client })

    await userEvent.click(screen.getByRole("combobox", { name: "Pod" }))

    await screen.findByRole("option", { name: "db-0" })
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["All", "db-0"])
  })

  it("shows rows of a query whose pipe removed _time", async () => {
    const { client } = clientWith((endpoint, params) =>
      endpoint.endsWith("query") ? new Response('{"_msg":"visible message"}\n') : defaultHandler(endpoint, params),
    )
    renderWithK8sProvider(<LogsView scope={app} />, { client })

    await userEvent.type(screen.getByLabelText("LogsQL filter"), "* | fields _msg{Enter}")

    const frame = screen.getByRole("region", { name: "Log lines" })
    expect(await within(frame).findByText("visible message")).toBeInTheDocument()
    expect(within(frame).queryByText(/No log lines/)).toBeNull()
  })

  it("keeps the view when only the pod list is refused", async () => {
    const status = JSON.stringify({ kind: "Status", apiVersion: "v1", status: "Failure", code: 404 })
    const { client } = clientWith((endpoint, params) =>
      endpoint.endsWith("stream_field_values") ? new Response(status, { status: 404 }) : defaultHandler(endpoint, params),
    )

    renderWithK8sProvider(<LogsView scope={app} />, { client })

    expect(await screen.findByText("ready to accept connections")).toBeInTheDocument()
    expect(screen.queryByText("Logs are not available on this cluster.")).toBeNull()
  })

  it("retries a failed history query on request", async () => {
    let calls = 0
    const { client } = clientWith((endpoint, params) =>
      endpoint.endsWith("query") && calls++ === 0 ? new Response("upstream down", { status: 502 }) : defaultHandler(endpoint, params),
    )
    renderWithK8sProvider(<LogsView scope={app} />, { client })

    await userEvent.click(await screen.findByRole("button", { name: "Retry" }))

    expect(await screen.findByText("ready to accept connections")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
  })

  it("says it is loading while a retry runs", async () => {
    let calls = 0
    const { client } = clientWith((endpoint, params) => {
      if (!endpoint.endsWith("query")) return defaultHandler(endpoint, params)
      return calls++ === 0 ? new Response("upstream down", { status: 502 }) : new Promise(() => {})
    })
    renderWithK8sProvider(<LogsView scope={app} />, { client })

    await userEvent.click(await screen.findByRole("button", { name: "Retry" }))

    const frame = screen.getByRole("region", { name: "Log lines" })
    await waitFor(() => expect(within(frame).getByRole("status")).toHaveAttribute("aria-busy", "true"))
    expect(within(frame).queryByRole("button", { name: "Retry" })).toBeNull()
  })

  it("opens a new history result at its newest line, not the old scroll position", async () => {
    const { client } = clientWith((endpoint, params) =>
      endpoint.endsWith("query")
        ? new Response(`${line("2026-11-04T10:00:01Z", `result for ${params.get("query")}`)}\n`)
        : defaultHandler(endpoint, params),
    )
    const { queryClient } = renderWithK8sProvider(<LogsView scope={app} />, { client })
    queryClient.setDefaultOptions({ queries: { ...queryClient.getDefaultOptions().queries, placeholderData: (prev: unknown) => prev } })
    await screen.findByText("result for *")
    const before = screen.getByRole("list")

    await userEvent.type(screen.getByLabelText("LogsQL filter"), "error{Enter}")

    await screen.findByText("result for error")
    expect(screen.getByRole("list")).not.toBe(before)
  })

  it("names the source pod and container of each line", async () => {
    const { client } = clientWith(defaultHandler)

    renderWithK8sProvider(<LogsView scope={{ namespace: "tenant-a" }} />, { client })

    expect(await screen.findByText("db-0/postgres")).toBeInTheDocument()
  })
})
