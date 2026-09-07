import { describe, it, expect, vi } from "vitest"
import { screen, fireEvent } from "@testing-library/react"
import type { FieldProps } from "@rjsf/utils"
import { SourceField } from "./SourceField.tsx"
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

// The shipped vm-disk shape: each branch marks its own leaf required, and
// `source` itself is not, so validation reports `.source.http.url`.
const sourceSchema = {
  type: "object",
  description: "The source image location used to create a disk.",
  properties: {
    http: {
      type: "object",
      required: ["url"],
      properties: { url: { type: "string", title: "URL" } },
    },
    disk: {
      type: "object",
      required: ["name"],
      properties: { name: { type: "string", title: "Name" } },
    },
  },
}

function props(overrides: Partial<FieldProps> = {}): FieldProps {
  return {
    schema: sourceSchema,
    formData: {},
    onChange: vi.fn(),
    name: "source",
    required: false,
    idSchema: { $id: "root_source" },
    ...overrides,
  } as unknown as FieldProps
}

describe("SourceField", () => {
  // Validation reports the branch leaf, and focus-first-error resolves the
  // field by RJSF's generated id, so the control has to carry it or a blocked
  // submit scrolls nowhere. Nothing else in the widget renders that id.
  it("puts the generated id on the selected branch's control", () => {
    renderWithK8sProvider(<SourceField {...props()} />, {
      client: createMockK8sClient({ lists: [] }),
    })

    fireEvent.click(screen.getByRole("radio", { name: /http/i }))

    expect(document.getElementById("root_source_http_url")).not.toBeNull()
  })

})
