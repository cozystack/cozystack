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

const schema = {
  type: "object",
  properties: {
    image: { type: "object", properties: { name: { type: "string" } } },
  },
}

function renderImagePicker() {
  const client = createMockK8sClient({
    lists: [
      {
        apiGroup: "core.cozystack.io",
        apiVersion: "v1alpha1",
        plural: "options",
        namespace: "tenant-root",
        result: {
          apiVersion: "core.cozystack.io/v1alpha1",
          kind: "OptionList",
          metadata: { resourceVersion: "1" },
          items: [
            {
              apiVersion: "core.cozystack.io/v1alpha1",
              kind: "Option",
              metadata: { name: "image" },
              spec: { items: [{ value: "fedora" }] },
            },
          ],
        },
      },
      // A golden whose import failed still has its PVC; the image option
      // source leaves it out, a raw PVC listing would not.
      {
        apiGroup: "",
        apiVersion: "v1",
        plural: "persistentvolumeclaims",
        namespace: "cozy-public",
        result: {
          apiVersion: "v1",
          kind: "PersistentVolumeClaimList",
          metadata: { resourceVersion: "1" },
          items: [
            { apiVersion: "v1", kind: "PersistentVolumeClaim", metadata: { name: "vm-default-images-ubuntu", namespace: "cozy-public" } },
          ],
        },
      },
    ],
  })
  const props = {
    schema,
    formData: { image: { name: "" } },
    onChange: vi.fn(),
    name: "source",
    required: false,
    idSchema: { $id: "root_source" },
  } as unknown as FieldProps
  return renderWithK8sProvider(<SourceField {...props} />, { client })
}

describe("SourceField image picker", () => {
  it("offers the images the image option source serves", async () => {
    renderImagePicker()
    expect(await screen.findByRole("option", { name: "fedora" })).toBeInTheDocument()
    expect(screen.queryByRole("option", { name: "ubuntu" })).not.toBeInTheDocument()
  })
})

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
