import { beforeEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { MemoryRouter } from "react-router"

const h = vi.hoisted(() => ({
  create: vi.fn(),
  update: vi.fn(),
  ad: {
    spec: {
      application: {
        kind: "NATS", plural: "natses", singular: "nats",
        openAPISchema: JSON.stringify({
          type: "object",
          properties: {
            config: {
              type: "object",
              properties: {
                merge: { type: "object", "x-kubernetes-preserve-unknown-fields": true },
                resolver: { type: "object", additionalProperties: true },
              },
            },
          },
        }),
      },
      dashboard: {},
    },
  },
}))

vi.mock("../lib/tenant-context.tsx", () => ({
  useTenantContext: () => ({ tenantNamespace: "tenant-test" }),
}))
vi.mock("../lib/app-definitions.ts", () => ({
  useApplicationDefinition: () => ({ data: h.ad, isLoading: false, error: null }),
  appDisplayName: () => "NATS", iconDataUrl: () => null,
}))
vi.mock("@cozystack/k8s-client", () => ({
  useK8sCreate: () => ({ mutateAsync: h.create, isPending: false }),
  useK8sUpdate: () => ({ mutateAsync: h.update, isPending: false }),
  K8sApiError: class extends Error {},
}))
vi.mock("../components/YamlEditor.tsx", () => ({
  YamlEditor: ({ value }: { value: string }) => <textarea data-testid="yaml" readOnly value={value} />,
}))

const { ApplicationOrderPage } = await import("./ApplicationOrderPage.tsx")
const merge = { accounts: { A: { jetstream: { max_streams: 2 }, subjects: ["one"], enabled: false } } }

describe("ApplicationOrderPage free-form objects", () => {
  beforeEach(() => {
    h.create.mockReset().mockResolvedValue({})
    h.update.mockReset().mockResolvedValue({})
  })

  it("sends nested objects on create after a form/YAML round trip", async () => {
    const user = userEvent.setup()
    render(<MemoryRouter><ApplicationOrderPage appNameOverride="nats" /></MemoryRouter>)
    await user.type(screen.getByPlaceholderText("nats"), "demo")
    fireEvent.change(screen.getByLabelText("merge"), { target: { value: JSON.stringify(merge) } })
    fireEvent.change(screen.getByLabelText("resolver"), { target: { value: '{"type":"full"}' } })
    await user.click(screen.getByRole("button", { name: /^yaml$/i }))
    await screen.findByTestId("yaml")
    await user.click(screen.getByRole("button", { name: /^form$/i }))
    expect(JSON.parse((screen.getByLabelText("merge") as HTMLTextAreaElement).value)).toEqual(merge)
    await user.click(screen.getByRole("button", { name: /^deploy$/i }))
    expect(h.create).toHaveBeenCalledOnce()
    expect(h.create.mock.calls[0][0].spec.config).toEqual({ merge, resolver: { type: "full" } })
  })

  it("preserves existing nested settings and saves an edited object", async () => {
    const user = userEvent.setup()
    render(<MemoryRouter><ApplicationOrderPage appNameOverride="nats" editMode={{ name: "demo", initialSpec: { config: { merge, resolver: { type: "full" } } } }} /></MemoryRouter>)
    expect(JSON.parse((screen.getByLabelText("merge") as HTMLTextAreaElement).value)).toEqual(merge)
    fireEvent.change(screen.getByLabelText("resolver"), { target: { value: "{}" } })
    await user.click(screen.getByRole("button", { name: /^save$/i }))
    expect(h.update).toHaveBeenCalledOnce()
    expect(h.update.mock.calls[0][0].spec.config).toEqual({ merge, resolver: {} })
  })

  it.each([false, true])("blocks invalid JSON from the API in edit mode %s", async (edit) => {
    const user = userEvent.setup()
    render(<MemoryRouter><ApplicationOrderPage appNameOverride="nats" editMode={edit ? { name: "demo", initialSpec: { config: { merge } } } : undefined} /></MemoryRouter>)
    if (!edit) await user.type(screen.getByPlaceholderText("nats"), "demo")
    fireEvent.change(screen.getByLabelText("merge"), { target: { value: "{" } })
    await user.click(screen.getByRole("button", { name: edit ? /^save$/i : /^deploy$/i }))
    expect(h.create).not.toHaveBeenCalled()
    expect(h.update).not.toHaveBeenCalled()
  })
})
