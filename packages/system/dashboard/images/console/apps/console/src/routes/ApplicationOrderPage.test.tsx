import { forwardRef, useImperativeHandle } from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { act, fireEvent, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { MemoryRouter } from "react-router"
import yaml from "js-yaml"
import type { ApplicationInstance } from "@cozystack/types"

const h = vi.hoisted(() => ({
  validate: true, create: vi.fn(), update: vi.fn(), client: { dryRunCreate: vi.fn() },
  tenant: { ns: "tenant-test" as string | null, loading: false },
  ad: { spec: { application: { kind: "Widget", plural: "widgets", singular: "widget",
    openAPISchema: JSON.stringify({ type: "object", properties: { setting: { type: "string", default: "client-schema" } } }),
  }, dashboard: {} } },
}))
vi.mock("../lib/tenant-context.tsx", () => ({ useTenantContext: () => ({ tenantNamespace: h.tenant.ns, isLoading: h.tenant.loading }) }))
vi.mock("../lib/app-definitions.ts", () => ({
  useApplicationDefinition: () => ({ data: h.ad, isLoading: false, error: null }),
  appDisplayName: () => "Widget", iconDataUrl: () => null,
}))
vi.mock("@cozystack/k8s-client", () => ({
  useK8sClient: () => h.client,
  useK8sCreate: () => ({ mutateAsync: h.create, isPending: false }),
  useK8sUpdate: () => ({ mutateAsync: h.update, isPending: false }),
  K8sApiError: class extends Error {},
}))
vi.mock("../components/SchemaForm.tsx", () => ({
  SchemaForm: forwardRef<{ validate: () => boolean }, { formData: unknown; onChange: (value: unknown) => void; defaulting: string }>(
    function MockSchemaForm(props, ref) {
      useImperativeHandle(ref, () => ({ validate: () => h.validate }))
      return <textarea aria-label="Spec" data-defaulting={props.defaulting} value={JSON.stringify(props.formData)} onChange={(e) => props.onChange(JSON.parse(e.target.value))} />
    },
  ),
}))
vi.mock("../components/YamlEditor.tsx", () => ({
  YamlEditor: ({ value, onChange }: { value: string; onChange: (value: string) => void }) => <textarea data-testid="yaml" value={value} onChange={(e) => onChange(e.target.value)} />,
}))
const { ApplicationOrderPage } = await import("./ApplicationOrderPage.tsx")
function pageElement(route = "/") { return <MemoryRouter initialEntries={[route]}><ApplicationOrderPage appNameOverride="widget" /></MemoryRouter> }
function renderPage(route = "/") { return render(pageElement(route)) }
function editSpec(value: unknown) { fireEvent.change(screen.getByLabelText("Spec"), { target: { value: JSON.stringify(value) } }) }
const deploy = () => screen.getByRole("button", { name: /^deploy$/i })
function deferred() {
  let resolve!: (value: ApplicationInstance) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<ApplicationInstance>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}
function previewBody(call = 0): ApplicationInstance { return h.client.dryRunCreate.mock.calls[call][3] as ApplicationInstance }

describe("ApplicationOrderPage server preview", () => {
  beforeEach(() => {
    h.create.mockReset().mockResolvedValue({})
    h.update.mockReset().mockResolvedValue({})
    h.client.dryRunCreate.mockReset().mockImplementation(async (_g, _v, _p, body: ApplicationInstance) => body)
    h.validate = true
    h.tenant.ns = "tenant-test"
    h.tenant.loading = false
  })

  it("asks the server once when the page opens and starts the form from the answer", async () => {
    const pending = deferred()
    h.client.dryRunCreate.mockReturnValue(pending.promise)
    renderPage()
    expect(h.client.dryRunCreate).toHaveBeenCalledOnce()
    expect(h.client.dryRunCreate.mock.calls[0].slice(0, 3)).toEqual(["apps.cozystack.io", "v1alpha1", "widgets"])
    expect(previewBody()).toMatchObject({ metadata: { name: "preview", namespace: "tenant-test" }, spec: {} })
    expect(screen.getByRole("status")).toHaveTextContent("Loading initial settings")
    expect(screen.queryByLabelText("Spec")).not.toBeInTheDocument()
    expect(deploy()).toBeDisabled()
    expect(screen.getByRole("button", { name: /^form$/i })).toBeDisabled()
    expect(screen.getByRole("button", { name: /^yaml$/i })).toBeDisabled()
    await act(async () => pending.resolve({ ...previewBody(), spec: { setting: "from-server" } }))
    expect(await screen.findByLabelText("Spec")).toHaveValue(JSON.stringify({ setting: "from-server" }))
    expect(deploy()).toBeEnabled()
    expect(h.create).not.toHaveBeenCalled()
  })

  it("previews under the name the user came in with", async () => {
    renderPage("/?name=demo")
    await screen.findByLabelText("Spec")
    expect(previewBody().metadata.name).toBe("demo")
  })

  it("sends no further previews while the user edits", async () => {
    const user = userEvent.setup()
    renderPage()
    await user.type(await screen.findByPlaceholderText("widget"), "demo")
    editSpec({ setting: "user" })
    await user.click(screen.getByRole("button", { name: /^yaml$/i }))
    await user.click(screen.getByRole("button", { name: /^form$/i }))
    expect(h.client.dryRunCreate).toHaveBeenCalledOnce()
  })

  it("submits the displayed spec unchanged through form and YAML", async () => {
    const prepared = { setting: "from-server", nested: {}, list: [], enabled: false, count: 0, text: "", nullable: null }
    h.client.dryRunCreate.mockImplementation(async (_g, _v, _p, body: ApplicationInstance) => ({ ...body, spec: prepared }))
    const user = userEvent.setup()
    renderPage()
    await user.type(await screen.findByPlaceholderText("widget"), "demo")
    await user.click(screen.getByRole("button", { name: /^yaml$/i }))
    const editor = await screen.findByTestId("yaml")
    expect((yaml.load((editor as HTMLTextAreaElement).value) as { spec: unknown }).spec).toEqual(prepared)
    await user.click(screen.getByRole("button", { name: /^form$/i }))
    await user.click(deploy())
    expect(h.create).toHaveBeenCalledOnce()
    expect(h.create.mock.calls[0][0]).toMatchObject({ metadata: { name: "demo" }, spec: prepared })
  })

  it("opens the form with a notice when the preview fails, and deploy still works", async () => {
    h.client.dryRunCreate.mockRejectedValueOnce(new Error("webhook does not support dry run"))
    const user = userEvent.setup()
    renderPage()
    expect(await screen.findByRole("alert")).toHaveTextContent("webhook does not support dry run")
    expect(screen.getByLabelText("Spec")).toHaveValue("{}")
    await user.type(screen.getByPlaceholderText("widget"), "demo")
    editSpec({ setting: "user" })
    await user.click(deploy())
    expect(h.create.mock.calls[0][0]).toMatchObject({ metadata: { name: "demo" }, spec: { setting: "user" } })
  })

  it("gives up on a preview that hangs and opens the form", async () => {
    vi.useFakeTimers()
    try {
      h.client.dryRunCreate.mockReturnValue(new Promise(() => {}))
      renderPage()
      expect(screen.queryByLabelText("Spec")).not.toBeInTheDocument()
      await act(async () => { await vi.advanceTimersByTimeAsync(8000) })
      expect(screen.getByRole("alert")).toHaveTextContent("timed out")
      expect(screen.getByLabelText("Spec")).toHaveValue("{}")
    } finally {
      vi.useRealTimers()
    }
  })

  it("treats a non-object answer as a failed preview", async () => {
    h.client.dryRunCreate.mockImplementation(async (_g, _v, _p, body: ApplicationInstance) => ({ ...body, spec: [] }))
    renderPage()
    expect(await screen.findByRole("alert")).toHaveTextContent("non-object")
    expect(screen.getByLabelText("Spec")).toHaveValue("{}")
  })

  it("shows the form without a preview when there is no tenant", () => {
    h.tenant.ns = null
    renderPage()
    expect(screen.getByLabelText("Spec")).toHaveValue("{}")
    expect(screen.queryByRole("status")).not.toBeInTheDocument()
    expect(deploy()).toBeDisabled()
    expect(h.client.dryRunCreate).not.toHaveBeenCalled()
  })

  it("waits for the tenant list, then sends one preview", async () => {
    h.tenant.ns = null
    h.tenant.loading = true
    const view = renderPage()
    expect(screen.getByRole("status")).toHaveTextContent("Loading initial settings")
    expect(h.client.dryRunCreate).not.toHaveBeenCalled()
    h.tenant.ns = "tenant-test"
    h.tenant.loading = false
    view.rerender(pageElement())
    expect(await screen.findByLabelText("Spec")).toBeInTheDocument()
    expect(h.client.dryRunCreate).toHaveBeenCalledOnce()
  })

  it("keeps what the user typed when the tenant is switched", async () => {
    const view = renderPage()
    await screen.findByLabelText("Spec")
    editSpec({ setting: "user" })
    h.tenant.ns = "tenant-other"
    view.rerender(pageElement())
    await act(async () => {})
    expect(h.client.dryRunCreate).toHaveBeenCalledOnce()
    expect(screen.getByLabelText("Spec")).toHaveValue(JSON.stringify({ setting: "user" }))
  })

  it("drops a preview answer that arrives after the tenant changed", async () => {
    const first = deferred()
    const second = deferred()
    h.client.dryRunCreate.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    const view = renderPage()
    h.tenant.ns = "tenant-other"
    view.rerender(pageElement())
    expect(h.client.dryRunCreate).toHaveBeenCalledTimes(2)
    await act(async () => first.resolve({ ...previewBody(0), spec: { setting: "stale" } }))
    expect(screen.queryByLabelText("Spec")).not.toBeInTheDocument()
    await act(async () => second.resolve({ ...previewBody(1), spec: { setting: "current" } }))
    expect(await screen.findByLabelText("Spec")).toHaveValue(JSON.stringify({ setting: "current" }))
  })

  it("drops a failure that arrives after the tenant changed", async () => {
    const first = deferred()
    const second = deferred()
    h.client.dryRunCreate.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    const view = renderPage()
    h.tenant.ns = "tenant-other"
    view.rerender(pageElement())
    await act(async () => first.reject(new Error("stale failure")))
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
    expect(screen.getByRole("status")).toBeInTheDocument()
    await act(async () => second.resolve({ ...previewBody(1), spec: { setting: "current" } }))
    expect(await screen.findByLabelText("Spec")).toHaveValue(JSON.stringify({ setting: "current" }))
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  })

  it("starts a new preview when the application kind changes back while another is pending", async () => {
    const widgets = h.ad
    const view = renderPage()
    await screen.findByLabelText("Spec")
    h.client.dryRunCreate.mockReturnValueOnce(deferred().promise)
    try {
      h.ad = { spec: { ...widgets.spec, application: { ...widgets.spec.application, plural: "gadgets" } } }
      view.rerender(pageElement())
      expect(screen.getByRole("status")).toBeInTheDocument()
      h.ad = widgets
      view.rerender(pageElement())
      expect(await screen.findByLabelText("Spec")).toBeInTheDocument()
      expect(h.client.dryRunCreate).toHaveBeenCalledTimes(3)
      expect(h.client.dryRunCreate.mock.calls[2][2]).toBe("widgets")
    } finally {
      h.ad = widgets
    }
  })

  it("retries with the edits made after the failure", async () => {
    h.client.dryRunCreate.mockRejectedValueOnce(new Error("rejected"))
    const user = userEvent.setup()
    renderPage()
    await screen.findByRole("alert")
    editSpec({ setting: "user", extra: "keep" })
    h.client.dryRunCreate.mockImplementationOnce(async (_g, _v, _p, body: ApplicationInstance) => ({ ...body, spec: { platform: "set", ...(body.spec as object) } }))
    await user.click(screen.getByRole("button", { name: /retry/i }))
    expect(await screen.findByLabelText("Spec")).toHaveValue(JSON.stringify({ platform: "set", setting: "user", extra: "keep" }))
    expect(previewBody(1).spec).toEqual({ setting: "user", extra: "keep" })
  })

  it("does not carry a retry draft into another application kind", async () => {
    const widgets = h.ad
    h.client.dryRunCreate.mockRejectedValueOnce(new Error("rejected"))
    const user = userEvent.setup()
    const view = renderPage()
    await screen.findByRole("alert")
    editSpec({ setting: "user" })
    await user.click(screen.getByRole("button", { name: /retry/i }))
    await screen.findByLabelText("Spec")
    try {
      h.ad = { spec: { ...widgets.spec, application: { ...widgets.spec.application, plural: "gadgets" } } }
      view.rerender(pageElement())
      await screen.findByLabelText("Spec")
      expect(h.client.dryRunCreate.mock.calls[2][2]).toBe("gadgets")
      expect(previewBody(2).spec).toEqual({})
    } finally {
      h.ad = widgets
    }
  })

  it("offers Retry in the form only, where the draft is the form's", async () => {
    h.client.dryRunCreate.mockRejectedValueOnce(new Error("rejected"))
    const user = userEvent.setup()
    renderPage()
    await screen.findByRole("alert")
    expect(screen.getByRole("button", { name: /retry/i })).toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: /^yaml$/i }))
    expect(screen.getByRole("alert")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /retry/i })).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: /^form$/i }))
    expect(screen.getByRole("button", { name: /retry/i })).toBeInTheDocument()
  })

  it("retries on request under the name typed meanwhile", async () => {
    h.client.dryRunCreate.mockRejectedValueOnce(new Error("name rejected"))
    const user = userEvent.setup()
    renderPage()
    await screen.findByRole("alert")
    await user.type(screen.getByPlaceholderText("widget"), "demo")
    h.client.dryRunCreate.mockImplementationOnce(async (_g, _v, _p, body: ApplicationInstance) => ({ ...body, spec: { setting: "from-server" } }))
    await user.click(screen.getByRole("button", { name: /retry/i }))
    expect(await screen.findByLabelText("Spec")).toHaveValue(JSON.stringify({ setting: "from-server" }))
    expect(previewBody(1).metadata.name).toBe("demo")
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
    expect(h.client.dryRunCreate).toHaveBeenCalledTimes(2)
  })

  it("leaves malformed YAML editable and does not send it", async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByLabelText("Spec")
    await user.click(screen.getByRole("button", { name: /^yaml$/i }))
    fireEvent.change(screen.getByTestId("yaml"), { target: { value: "spec: [" } })
    await user.click(deploy())
    expect(h.create).not.toHaveBeenCalled()
    expect(document.querySelector(".bg-red-50")).not.toBeNull()
    fireEvent.change(screen.getByTestId("yaml"), { target: { value: yaml.dump({ metadata: { name: "demo" }, spec: { setting: "user" } }) } })
    expect(document.querySelector(".bg-red-50")).toBeNull()
    await user.click(deploy())
    expect(h.create.mock.calls[0][0]).toMatchObject({ metadata: { name: "demo" }, spec: { setting: "user" } })
  })

  it("keeps form validation after a successful preview", async () => {
    h.validate = false
    const user = userEvent.setup()
    renderPage()
    await user.type(await screen.findByPlaceholderText("widget"), "demo")
    await user.click(deploy())
    expect(h.create).not.toHaveBeenCalled()
    await user.click(screen.getByRole("button", { name: /^yaml$/i }))
    await user.click(deploy())
    expect(h.create).toHaveBeenCalledOnce()
  })

  it("edits saved values without a creation preview", async () => {
    const user = userEvent.setup()
    render(<MemoryRouter><ApplicationOrderPage appNameOverride="widget" editMode={{ name: "existing", initialSpec: { setting: "saved" } }} /></MemoryRouter>)
    expect(screen.getByLabelText("Spec")).toHaveValue(JSON.stringify({ setting: "saved" }))
    expect(screen.getByLabelText("Spec")).toHaveAttribute("data-defaulting", "server")
    await user.click(screen.getByRole("button", { name: /^save$/i }))
    expect(h.update).toHaveBeenCalledOnce()
    expect(h.client.dryRunCreate).not.toHaveBeenCalled()
  })
})
