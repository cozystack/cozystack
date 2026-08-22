import { forwardRef, useEffect, useImperativeHandle } from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { MemoryRouter } from "react-router"

// The submit button bypasses RJSF; the gate calls SchemaForm.validate(). Mock
// SchemaForm so the test drives that boolean directly and emits a spec that
// satisfies the page's own manual required-field checks, isolating the gate.
const h = vi.hoisted(() => ({
  validateReturn: true,
  createMutateAsync: vi.fn(),
  validSpec: {
    applicationRef: { kind: "VirtualMachine", name: "demo" },
    strategyRef: { kind: "Strategy", name: "s3" },
    takenAt: "2024-01-01T00:00:00Z",
  },
  // Spec the mocked SchemaForm emits into the page on mount; reset per test.
  emitSpec: {} as unknown,
}))

vi.mock("../lib/tenant-context.tsx", () => ({
  useTenantContext: () => ({ tenantNamespace: "tenant-test" }),
}))
vi.mock("../lib/app-definitions.ts", () => ({
  useApplicationDefinitions: () => ({ data: { items: [] } }),
}))
vi.mock("../lib/use-crd-schema.ts", () => ({
  useCRDSchema: () => ({
    schema: JSON.stringify({ type: "object", properties: {} }),
    isLoading: false,
  }),
}))
vi.mock("@cozystack/k8s-client", () => ({
  useK8sCreate: () => ({ mutateAsync: h.createMutateAsync, isPending: false }),
  useK8sList: () => ({ data: undefined }),
}))
vi.mock("../components/SchemaForm.tsx", () => ({
  SchemaForm: forwardRef<{ validate: () => boolean }, { onChange: (d: unknown) => void }>(
    function MockSchemaForm({ onChange }, ref) {
      useImperativeHandle(ref, () => ({ validate: () => h.validateReturn }))
      useEffect(() => {
        onChange(h.emitSpec)
      }, [onChange])
      return null
    },
  ),
}))

const { BackupCreatePage } = await import("./BackupCreatePage.tsx")

function renderPage() {
  return render(
    <MemoryRouter>
      <BackupCreatePage />
    </MemoryRouter>,
  )
}

describe("BackupCreatePage submit validation gate", () => {
  beforeEach(() => {
    h.createMutateAsync.mockReset()
    h.createMutateAsync.mockResolvedValue({})
    h.validateReturn = true
    h.emitSpec = h.validSpec
  })

  it("does not POST when the form fails RJSF validation", async () => {
    h.validateReturn = false
    const user = userEvent.setup()
    renderPage()

    await user.type(screen.getByRole("textbox"), "my-backup")
    await user.click(screen.getByRole("button", { name: /create/i }))

    expect(h.createMutateAsync).not.toHaveBeenCalled()
  })

  it("POSTs when the form passes validation", async () => {
    h.validateReturn = true
    const user = userEvent.setup()
    renderPage()

    await user.type(screen.getByRole("textbox"), "my-backup")
    await user.click(screen.getByRole("button", { name: /create/i }))

    expect(h.createMutateAsync).toHaveBeenCalledTimes(1)
  })

  it("runs RJSF validation before the page-level required-field alerts", async () => {
    // Form is RJSF-invalid AND a page-required field is missing. The gate must
    // fire first, so no alert() is shown — proving validate() runs before the
    // manual checks (under the old ordering the applicationRef alert fired).
    h.validateReturn = false
    h.emitSpec = {}
    const alertSpy = vi.spyOn(window, "alert").mockImplementation(() => {})
    const user = userEvent.setup()
    renderPage()

    await user.type(screen.getByRole("textbox"), "my-backup")
    await user.click(screen.getByRole("button", { name: /create/i }))

    expect(h.createMutateAsync).not.toHaveBeenCalled()
    expect(alertSpy).not.toHaveBeenCalled()
    alertSpy.mockRestore()
  })
})
