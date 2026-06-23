import { useState } from "react"
import { describe, it, expect, vi } from "vitest"
import { screen, fireEvent, waitFor } from "@testing-library/react"
import type { WidgetProps } from "@rjsf/utils"
import type { K8sList } from "@cozystack/k8s-client"
import { DynamicOptionsWidget } from "./DynamicOptionsWidget.tsx"
import { createMockK8sClient } from "../test-utils/mock-k8s-client.ts"
import { renderWithK8sProvider } from "../test-utils/render.tsx"

vi.mock("../lib/tenant-context.tsx", () => ({
  useTenantContext: () => ({
    tenants: [],
    selectedTenant: "root",
    selectTenant: () => {},
    tenantNamespace: "tenant-root",
    isLoading: false,
    error: null,
  }),
}))

interface OptionItem {
  value: string
  label?: string
  description?: string
  default?: boolean
}

function option(name: string, items: OptionItem[]) {
  return {
    apiVersion: "core.cozystack.io/v1alpha1",
    kind: "Option",
    metadata: { name },
    spec: { items },
  }
}

function list(...options: ReturnType<typeof option>[]): K8sList<unknown> {
  return {
    apiVersion: "core.cozystack.io/v1alpha1",
    kind: "OptionList",
    metadata: { resourceVersion: "1" },
    items: options,
  }
}

type ListResult = K8sList<unknown> | (() => K8sList<unknown> | Promise<K8sList<unknown>>)

function clientWith(result: ListResult) {
  return createMockK8sClient({
    lists: [
      {
        apiGroup: "core.cozystack.io",
        apiVersion: "v1alpha1",
        plural: "options",
        namespace: "tenant-root",
        result,
      },
    ],
  })
}

const NEVER_RESOLVES = () => new Promise<K8sList<unknown>>(() => {})

function makeProps(overrides: Partial<WidgetProps> = {}, source = "storageclass"): WidgetProps {
  const base = {
    id: "field",
    name: "field",
    label: "field",
    value: undefined as unknown,
    onChange: vi.fn(),
    onBlur: vi.fn(),
    onFocus: vi.fn(),
    required: false,
    disabled: false,
    readonly: false,
    autofocus: false,
    placeholder: "",
    options: {},
    schema: { type: "string", "x-cozystack-options": { source } },
    uiSchema: {},
    formContext: {},
    rawErrors: [],
    hideError: false,
    multiple: false,
    registry: {},
  }
  return { ...base, ...overrides } as unknown as WidgetProps
}

describe("DynamicOptionsWidget", () => {
  it("auto-selects the server-marked default item exactly once when no value is set", async () => {
    const onChange = vi.fn()
    renderWithK8sProvider(
      <DynamicOptionsWidget {...makeProps({ onChange })} />,
      { client: clientWith(list(option("storageclass", [{ value: "fast" }, { value: "standard", default: true }]))) },
    )

    await waitFor(() => expect(onChange).toHaveBeenCalledWith("standard"))
    expect(onChange).toHaveBeenCalledTimes(1)
  })

  it("does not auto-commit when no item is marked default (preserves the VMDisk invariant)", async () => {
    // The old VMDiskWidget deliberately never auto-selected the first disk —
    // auto-committing dropped the user's choice on a fast submit. The generic
    // widget only auto-selects when the server marks item.default, so a
    // default-less source (e.g. vmdisk) must stay untouched on load.
    const onChange = vi.fn()
    renderWithK8sProvider(
      <DynamicOptionsWidget {...makeProps({ onChange }, "vmdisk")} />,
      { client: clientWith(list(option("vmdisk", [{ value: "disk-a" }, { value: "disk-b" }]))) },
    )

    await screen.findByRole("option", { name: /disk-a/ })
    expect(onChange).not.toHaveBeenCalled()
  })

  it("lists the Option resource namespaced to the active tenant", async () => {
    // The Option CRD is namespaced (tenant-scoped); querying it cluster-wide
    // would return nothing. Pin the namespaced LIST.
    const client = clientWith(list(option("storageclass", [{ value: "fast" }])))
    renderWithK8sProvider(<DynamicOptionsWidget {...makeProps()} />, { client })

    await screen.findByRole("option", { name: /fast/ })
    expect(client.list).toHaveBeenCalledWith(
      "core.cozystack.io",
      "v1alpha1",
      "options",
      "tenant-root",
      expect.anything(),
    )
  })

  it("shows an explicit placeholder instead of the first option when required and empty", async () => {
    renderWithK8sProvider(
      // No default item, so the auto-default effect stays idle and the
      // value-less required state is observable.
      <DynamicOptionsWidget {...makeProps({ required: true })} />,
      { client: clientWith(list(option("storageclass", [{ value: "fast" }, { value: "slow" }]))) },
    )

    await screen.findByRole("option", { name: /^fast$/ })
    const select = screen.getByRole("combobox") as HTMLSelectElement
    expect(select.value).toBe("")
    expect(screen.getByRole("option", { name: /select an option/i })).toBeInTheDocument()
    expect((screen.getByRole("option", { name: /^fast$/ }) as HTMLOptionElement).selected).toBe(false)
  })

  it("keeps a committed value visible while the list is still loading", () => {
    renderWithK8sProvider(
      <DynamicOptionsWidget {...makeProps({ required: true, value: "custom-x" })} />,
      { client: clientWith(NEVER_RESOLVES) },
    )

    const select = screen.getByRole("combobox") as HTMLSelectElement
    expect(select.value).toBe("custom-x")
    expect(screen.getByRole("option", { name: /custom-x/ })).toBeInTheDocument()
  })

  it("lets the user clear an optional field that has a server default (no snap-back)", async () => {
    // The auto-default must fire only once; clearing must stick rather than the
    // effect immediately re-applying the default. Needs a stateful host so the
    // cleared value actually flows back and could (wrongly) re-trigger the effect.
    function Host() {
      const [value, setValue] = useState<string | undefined>(undefined)
      return (
        <DynamicOptionsWidget
          {...makeProps({ value, onChange: setValue as (v: unknown) => void })}
        />
      )
    }
    renderWithK8sProvider(<Host />, {
      client: clientWith(
        list(option("storageclass", [{ value: "fast" }, { value: "standard", default: true }])),
      ),
    })

    const select = (await screen.findByRole("combobox")) as HTMLSelectElement
    await waitFor(() => expect(select.value).toBe("standard"))

    fireEvent.change(select, { target: { value: "" } })
    await waitFor(() => expect(select.value).toBe(""))
    // The default must not re-apply after a deliberate clear.
    expect(select.value).toBe("")
  })

  it("emits undefined (not an empty string) when an optional field is cleared", async () => {
    const onChange = vi.fn()
    renderWithK8sProvider(
      // No default item, so clearing is not immediately re-applied.
      <DynamicOptionsWidget {...makeProps({ value: "fast", onChange })} />,
      { client: clientWith(list(option("storageclass", [{ value: "fast" }, { value: "slow" }]))) },
    )

    await screen.findByRole("option", { name: /^fast$/ })
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "" } })
    expect(onChange).toHaveBeenCalledWith(undefined)
  })

  it("renders labels and resolves items only from the Option whose name matches the source", async () => {
    renderWithK8sProvider(
      <DynamicOptionsWidget {...makeProps({}, "backupclass")} />,
      {
        client: clientWith(
          list(
            option("storageclass", [{ value: "fast", label: "Fast SSD" }]),
            option("backupclass", [{ value: "s3", label: "S3 bucket" }]),
          ),
        ),
      },
    )

    expect(await screen.findByRole("option", { name: "S3 bucket" })).toBeInTheDocument()
    // The unrelated source's items must not leak in.
    expect(screen.queryByRole("option", { name: "Fast SSD" })).not.toBeInTheDocument()
  })
})
