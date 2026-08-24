import { describe, it, expect, vi, afterEach } from "vitest"
import { screen, waitFor, cleanup, fireEvent } from "@testing-library/react"
import { K8sClient } from "@cozystack/k8s-client"
import { renderWithK8sProvider } from "../../test-utils/render.tsx"
import { DiskUploadPanel } from "./DiskUploadPanel.tsx"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"

const ad: ApplicationDefinition = {
  apiVersion: "cozystack.io/v1alpha1",
  kind: "ApplicationDefinition",
  metadata: { name: "vm-disk" },
  spec: {
    application: {
      kind: "VMDisk",
      plural: "vmdisks",
      singular: "vmdisk",
      openAPISchema: "{}",
    },
    release: { prefix: "vm-disk-" },
  },
}

const instance: ApplicationInstance = {
  apiVersion: "apps.cozystack.io/v1alpha1",
  kind: "VMDisk",
  metadata: { name: "demo", namespace: "tenant-root" },
}

interface ClusterFixture {
  source?: Record<string, unknown>
  phase?: string
  dvError?: Error
  uploadProxyURL?: string
  canUpload?: boolean
  ssarError?: Error
}

function makeClient(fixture: ClusterFixture) {
  const client = new K8sClient()
  vi.spyOn(client, "get").mockImplementation(
    async (_g: string, _v: string, plural: string, name: string) => {
      if (plural === "cdiconfigs") {
        return {
          apiVersion: "cdi.kubevirt.io/v1beta1",
          kind: "CDIConfig",
          metadata: { name: "config" },
          status: fixture.uploadProxyURL
            ? { uploadProxyURL: fixture.uploadProxyURL }
            : {},
        }
      }
      if (fixture.dvError) throw fixture.dvError
      return {
        apiVersion: "cdi.kubevirt.io/v1beta1",
        kind: "DataVolume",
        metadata: { name, namespace: "tenant-root" },
        spec: { source: fixture.source ?? { upload: {} } },
        status: { phase: fixture.phase ?? "UploadReady" },
      }
    },
  )
  vi.spyOn(client, "create").mockImplementation(async () => {
    if (fixture.ssarError) throw fixture.ssarError
    return {
      apiVersion: "authorization.k8s.io/v1",
      kind: "SelfSubjectAccessReview",
      metadata: { name: "" },
      status: { allowed: fixture.canUpload ?? true },
    }
  })
  vi.spyOn(client, "watch").mockReturnValue(() => {})
  return client
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  // jsdom exposes no clipboard; drop the one the copy test installs.
  Reflect.deleteProperty(navigator, "clipboard")
})

describe("DiskUploadPanel visibility", () => {
  it("reads the DataVolume named by the ApplicationDefinition release prefix", async () => {
    const client = makeClient({})
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    await waitFor(() =>
      expect(client.get).toHaveBeenCalledWith(
        "cdi.kubevirt.io",
        "v1beta1",
        "datavolumes",
        "vm-disk-demo",
        "tenant-root",
      ),
    )
  })

  it("renders nothing for a disk with a non-upload source", async () => {
    const client = makeClient({ source: { http: { url: "https://example.org/i.qcow2" } } })
    const { container } = renderWithK8sProvider(
      <DiskUploadPanel ad={ad} instance={instance} />,
      { client },
    )
    await waitFor(() => expect(client.get).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })

  it("renders nothing when the DataVolume cannot be read", async () => {
    const client = makeClient({ dvError: new Error("forbidden") })
    const { container } = renderWithK8sProvider(
      <DiskUploadPanel ad={ad} instance={instance} />,
      { client },
    )
    await waitFor(() => expect(client.get).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })
})

describe("DiskUploadPanel states", () => {
  it("offers the command at UploadReady, with the cluster's proxy URL", async () => {
    const client = makeClient({ uploadProxyURL: "https://cdi-uploadproxy.example.org" })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    expect(await screen.findByText("Waiting for an image")).toBeInTheDocument()
    const cmd = await screen.findByText(/virtctl image-upload dv vm-disk-demo/)
    expect(cmd.textContent).toContain("--uploadproxy-url=https://cdi-uploadproxy.example.org")
    expect(cmd.textContent).toContain("--no-create")
    expect(screen.queryByText(/publishes no upload proxy URL/)).not.toBeInTheDocument()
  })

  it("flags the placeholder when the cluster published no proxy URL", async () => {
    const client = makeClient({})
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    expect(await screen.findByText(/publishes no upload proxy URL/)).toBeInTheDocument()
  })

  it("flags the placeholder when the published proxy URL is only whitespace", async () => {
    const client = makeClient({ uploadProxyURL: "   " })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    expect(await screen.findByText(/publishes no upload proxy URL/)).toBeInTheDocument()
    const cmd = await screen.findByText(/virtctl image-upload dv vm-disk-demo/)
    expect(cmd.textContent).toContain("cdi-uploadproxy.<your-cozystack-domain>")
  })

  it("withholds the command before the upload target exists", async () => {
    const client = makeClient({ phase: "Pending" })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    expect(await screen.findByText("Preparing upload target")).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()
  })

  it("withholds the command once the image is written", async () => {
    const client = makeClient({ phase: "Succeeded" })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    expect(await screen.findByText("Image uploaded")).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()
  })

  it("offers the command again after a failed upload", async () => {
    const client = makeClient({ phase: "Failed" })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    expect(await screen.findByText("Upload failed")).toBeInTheDocument()
    expect(await screen.findByText(/virtctl image-upload/)).toBeInTheDocument()
  })
})

describe("DiskUploadPanel RBAC warning", () => {
  it("warns when the user cannot create uploadtokenrequests", async () => {
    const client = makeClient({ canUpload: false })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    expect(await screen.findByText(/will fail with Forbidden/)).toBeInTheDocument()
    expect(client.create).toHaveBeenCalledWith(
      "authorization.k8s.io",
      "v1",
      "selfsubjectaccessreviews",
      expect.objectContaining({
        spec: {
          resourceAttributes: {
            namespace: "tenant-root",
            group: "upload.cdi.kubevirt.io",
            resource: "uploadtokenrequests",
            verb: "create",
          },
        },
      }),
    )
  })

  it("stays quiet when the access check itself failed", async () => {
    const client = makeClient({ ssarError: new Error("SSAR unavailable") })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    expect(await screen.findByText(/virtctl image-upload/)).toBeInTheDocument()
    await waitFor(() => expect(client.create).toHaveBeenCalled())
    await expect(screen.findByText(/will fail with Forbidden/)).rejects.toThrow()
  })

  it("stays quiet when the user is allowed to upload", async () => {
    const client = makeClient({ canUpload: true })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    expect(await screen.findByText(/virtctl image-upload/)).toBeInTheDocument()
    await waitFor(() => expect(client.create).toHaveBeenCalled())
    expect(screen.queryByText(/will fail with Forbidden/)).not.toBeInTheDocument()
  })
})

describe("DiskUploadPanel copy button", () => {
  it("does not offer a copy the browser cannot perform", async () => {
    const client = makeClient({})
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    expect(await screen.findByLabelText("Copy command")).toBeDisabled()
  })

  it("copies the command where a clipboard exists", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true })
    const client = makeClient({})
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    fireEvent.click(await screen.findByLabelText("Copy command"))
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(
        expect.stringContaining("virtctl image-upload dv vm-disk-demo"),
      ),
    )
  })
})
