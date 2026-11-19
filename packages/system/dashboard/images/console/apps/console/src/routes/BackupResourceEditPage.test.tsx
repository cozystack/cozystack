import { forwardRef, useEffect, useImperativeHandle } from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { MemoryRouter } from "react-router"

// The Save button bypasses RJSF; the gate calls SchemaForm.validate(). Mock
// SchemaForm so the test drives that boolean directly, isolating the gate.
const h = vi.hoisted(() => ({
  validateReturn: true,
  updateMutateAsync: vi.fn(),
  resource: {
    apiVersion: "backups.cozystack.io/v1alpha1",
    kind: "Backup",
    metadata: { name: "demo", namespace: "tenant-test" },
    spec: { foo: "bar" },
  },
}))

const overrideSchema = JSON.stringify({
  type: "object",
  properties: { foo: { type: "string" } },
})

vi.mock("../lib/tenant-context.tsx", () => ({
  useTenantContext: () => ({ tenantNamespace: "tenant-test" }),
}))
vi.mock("../lib/use-crd-schema.ts", () => ({
  useCRDSchema: () => ({ schema: overrideSchema, isLoading: false }),
}))
vi.mock("@cozystack/k8s-client", () => ({
  useK8sGet: () => ({ data: h.resource, isLoading: false, error: null }),
  useK8sUpdate: () => ({ mutateAsync: h.updateMutateAsync, isPending: false }),
}))
vi.mock("../components/SchemaForm.tsx", () => ({
  SchemaForm: forwardRef<{ validate: () => boolean }, { onChange: (d: unknown) => void }>(
    function MockSchemaForm({ onChange }, ref) {
      useImperativeHandle(ref, () => ({ validate: () => h.validateReturn }))
      useEffect(() => {
        onChange({ foo: "baz" })
      }, [onChange])
      return null
    },
  ),
}))

const { BackupResourceEditPage } = await import("./BackupResourceEditPage.tsx")

function renderPage() {
  return render(
    <MemoryRouter>
      <BackupResourceEditPage resourceType="backups" title="Backups" overrideSchema={overrideSchema} />
    </MemoryRouter>,
  )
}

describe("BackupResourceEditPage submit validation gate", () => {
  beforeEach(() => {
    h.updateMutateAsync.mockReset()
    h.updateMutateAsync.mockResolvedValue({})
    h.validateReturn = true
  })

  it("does not PUT when the form fails RJSF validation", async () => {
    h.validateReturn = false
    const user = userEvent.setup()
    renderPage()

    await user.click(screen.getByRole("button", { name: /save/i }))

    expect(h.updateMutateAsync).not.toHaveBeenCalled()
  })

  it("PUTs when the form passes validation", async () => {
    h.validateReturn = true
    const user = userEvent.setup()
    renderPage()

    await user.click(screen.getByRole("button", { name: /save/i }))

    expect(h.updateMutateAsync).toHaveBeenCalledTimes(1)
  })
})
