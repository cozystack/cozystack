import { forwardRef, useImperativeHandle } from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { MemoryRouter } from "react-router"

// The Deploy button bypasses RJSF; in form mode the gate calls
// SchemaForm.validate(), in YAML mode it is skipped. Mock SchemaForm so the
// test drives that boolean directly, and YamlEditor so Monaco stays out.
const h = vi.hoisted(() => ({
  validateReturn: true,
  createMutateAsync: vi.fn(),
  updateMutateAsync: vi.fn(),
  ad: {
    spec: {
      application: {
        kind: "VirtualMachine",
        plural: "virtualmachines",
        singular: "virtualmachine",
        openAPISchema: JSON.stringify({ type: "object", properties: {} }),
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
  appDisplayName: () => "Virtual Machine",
  iconDataUrl: () => null,
}))
vi.mock("@cozystack/k8s-client", () => ({
  useK8sCreate: () => ({ mutateAsync: h.createMutateAsync, isPending: false }),
  useK8sUpdate: () => ({ mutateAsync: h.updateMutateAsync, isPending: false }),
  K8sApiError: class K8sApiError extends Error {},
}))
vi.mock("../components/SchemaForm.tsx", () => ({
  SchemaForm: forwardRef<{ validate: () => boolean }, Record<string, unknown>>(
    function MockSchemaForm(_props, ref) {
      useImperativeHandle(ref, () => ({ validate: () => h.validateReturn }))
      return null
    },
  ),
}))
vi.mock("../components/YamlEditor.tsx", () => ({
  YamlEditor: ({ value }: { value: string }) => <textarea data-testid="yaml" readOnly value={value} />,
}))

const { ApplicationOrderPage } = await import("./ApplicationOrderPage.tsx")

function renderPage() {
  return render(
    <MemoryRouter>
      <ApplicationOrderPage appNameOverride="virtualmachine" />
    </MemoryRouter>,
  )
}

describe("ApplicationOrderPage submit validation gate", () => {
  beforeEach(() => {
    h.createMutateAsync.mockReset()
    h.createMutateAsync.mockResolvedValue({})
    h.updateMutateAsync.mockReset()
    h.updateMutateAsync.mockResolvedValue({})
    h.validateReturn = true
  })

  it("does not POST in form mode when the form fails RJSF validation", async () => {
    h.validateReturn = false
    const user = userEvent.setup()
    renderPage()

    await user.type(screen.getByRole("textbox"), "demo-vm")
    await user.click(screen.getByRole("button", { name: /^deploy$/i }))

    expect(h.createMutateAsync).not.toHaveBeenCalled()
  })

  it("POSTs in form mode when the form passes validation", async () => {
    h.validateReturn = true
    const user = userEvent.setup()
    renderPage()

    await user.type(screen.getByRole("textbox"), "demo-vm")
    await user.click(screen.getByRole("button", { name: /^deploy$/i }))

    expect(h.createMutateAsync).toHaveBeenCalledTimes(1)
  })

  it("skips the gate in YAML mode even when validation would fail", async () => {
    // In YAML mode the spec is hand-authored, so the gate must not block it.
    h.validateReturn = false
    const user = userEvent.setup()
    renderPage()

    await user.type(screen.getByRole("textbox"), "demo-vm")
    await user.click(screen.getByRole("button", { name: /yaml/i }))
    await user.click(screen.getByRole("button", { name: /^deploy$/i }))

    expect(h.createMutateAsync).toHaveBeenCalledTimes(1)
  })
})
