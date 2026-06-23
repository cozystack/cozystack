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
    backupClassName: "s3",
  },
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
        onChange(h.validSpec)
      }, [onChange])
      return null
    },
  ),
}))

const { BackupJobCreatePage } = await import("./BackupJobCreatePage.tsx")

function renderPage() {
  return render(
    <MemoryRouter>
      <BackupJobCreatePage />
    </MemoryRouter>,
  )
}

describe("BackupJobCreatePage submit validation gate", () => {
  beforeEach(() => {
    h.createMutateAsync.mockReset()
    h.createMutateAsync.mockResolvedValue({})
    h.validateReturn = true
  })

  it("does not POST when the form fails RJSF validation", async () => {
    h.validateReturn = false
    const user = userEvent.setup()
    renderPage()

    await user.type(screen.getByRole("textbox"), "my-job")
    await user.click(screen.getByRole("button", { name: /create/i }))

    expect(h.createMutateAsync).not.toHaveBeenCalled()
  })

  it("POSTs when the form passes validation", async () => {
    h.validateReturn = true
    const user = userEvent.setup()
    renderPage()

    await user.type(screen.getByRole("textbox"), "my-job")
    await user.click(screen.getByRole("button", { name: /create/i }))

    expect(h.createMutateAsync).toHaveBeenCalledTimes(1)
  })
})
