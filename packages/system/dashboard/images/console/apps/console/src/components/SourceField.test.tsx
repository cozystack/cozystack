import { describe, it, expect, vi } from "vitest"
import { screen } from "@testing-library/react"
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
