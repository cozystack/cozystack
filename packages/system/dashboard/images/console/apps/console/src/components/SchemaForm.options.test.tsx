import { useState } from "react"
import { describe, it, expect, vi } from "vitest"
import { screen, waitFor } from "@testing-library/react"
import type { K8sList } from "@cozystack/k8s-client"
import { SchemaForm } from "./SchemaForm.tsx"
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

const storageClass = { type: "string", "x-cozystack-options": { source: "storageclass" } }

const schemas = {
  field: JSON.stringify({ type: "object", properties: { storageClass } }),
  dictionaryValue: JSON.stringify({
    type: "object",
    properties: { nodeGroups: { type: "object", additionalProperties: { type: "object", properties: { storageClass } } } },
  }),
  arrayRow: JSON.stringify({
    type: "object",
    properties: { rows: { type: "array", items: { type: "object", properties: { storageClass } } } },
  }),
}

const options: K8sList<unknown> = {
  apiVersion: "core.cozystack.io/v1alpha1",
  kind: "OptionList",
  metadata: { resourceVersion: "1" },
  items: [{
    apiVersion: "core.cozystack.io/v1alpha1",
    kind: "Option",
    metadata: { name: "storageclass" },
    spec: { items: [{ value: "local", default: true }, { value: "replicated" }] },
  }],
}

function Harness({ schema, initial, autoSelectDefaults }: { schema: string; initial: unknown; autoSelectDefaults?: boolean }) {
  const [spec, setSpec] = useState<unknown>(initial)
  return <SchemaForm openAPISchema={schema} formData={spec} onChange={setSpec} defaulting="server" autoSelectDefaults={autoSelectDefaults} />
}

const client = () => createMockK8sClient({
  lists: [{ apiGroup: "core.cozystack.io", apiVersion: "v1alpha1", plural: "options", namespace: "tenant-root", result: options }],
})

describe("dynamic options in a server-prepared form", () => {
  it.each([
    ["a field", schemas.field, {}],
    ["a dictionary value", schemas.dictionaryValue, { nodeGroups: { md0: {} } }],
    ["an array row", schemas.arrayRow, { rows: [{}] }],
  ])("selects the default option of an empty value in %s", async (_where, schema, initial) => {
    renderWithK8sProvider(<Harness schema={schema} initial={initial} />, { client: client() })
    await waitFor(() => expect(screen.getByRole("combobox")).toHaveValue("local"))
  })

  it.each([
    ["a field", schemas.field, {}],
    ["a dictionary value", schemas.dictionaryValue, { nodeGroups: { md0: {} } }],
    ["an array row", schemas.arrayRow, { rows: [{}] }],
  ])("leaves an empty value in %s alone when told the form did not start from the server's values", async (_where, schema, initial) => {
    renderWithK8sProvider(<Harness schema={schema} initial={initial} autoSelectDefaults={false} />, { client: client() })
    await screen.findByRole("option", { name: "local" })
    expect(screen.getByRole("combobox")).toHaveValue("")
  })
})
