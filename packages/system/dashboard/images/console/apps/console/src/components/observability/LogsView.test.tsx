import { describe, it, expect, vi } from "vitest"
import { screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { K8sClient } from "@cozystack/k8s-client"
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

  it("names the source pod and container of each line", async () => {
    const { client } = clientWith(defaultHandler)

    renderWithK8sProvider(<LogsView scope={{ namespace: "tenant-a" }} />, { client })

    expect(await screen.findByText("db-0/postgres")).toBeInTheDocument()
  })
})
