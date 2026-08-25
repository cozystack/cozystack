import { describe, it, expect, vi, afterEach } from "vitest"
import { act, screen, waitFor, cleanup, fireEvent } from "@testing-library/react"
import { K8sClient } from "@cozystack/k8s-client"
import { renderWithK8sProvider } from "../../test-utils/render.tsx"
import { DiskUploadPanel } from "./DiskUploadPanel.tsx"
import type { DataVolume } from "../../lib/vm-disk-upload.ts"
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

/**
 * The same application without an explicit `release.prefix`. `releasePrefix`
 * then falls back to `<singular>-`, and the panel has to name the DataVolume
 * the same way the chart does.
 */
const adWithoutPrefix: ApplicationDefinition = {
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
  /** Successive DataVolume phases, one per GET; the last one repeats. */
  phases?: string[]
  conditions?: NonNullable<DataVolume["status"]>["conditions"]
  dvError?: Error
  uploadProxyURL?: string
  cdiConfigError?: Error
  canUpload?: boolean
  ssarError?: Error
}

function makeClient(fixture: ClusterFixture) {
  const client = new K8sClient()
  const phases = fixture.phases ?? [fixture.phase ?? "UploadReady"]
  let dvReads = 0
  vi.spyOn(client, "get").mockImplementation(
    async (_g: string, _v: string, plural: string, name: string) => {
      if (plural === "cdiconfigs") {
        if (fixture.cdiConfigError) throw fixture.cdiConfigError
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
      const phase = phases[Math.min(dvReads++, phases.length - 1)]
      return {
        apiVersion: "cdi.kubevirt.io/v1beta1",
        kind: "DataVolume",
        metadata: { name, namespace: "tenant-root" },
        spec: { source: fixture.source ?? { upload: {} } },
        status: { phase, conditions: fixture.conditions },
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

/** How many times the panel has read the DataVolume through this client. */
function dvReads(client: K8sClient): number {
  const get = client.get as unknown as { mock: { calls: unknown[][] } }
  return get.mock.calls.filter((c) => c[2] === "datavolumes").length
}

afterEach(() => {
  cleanup()
  vi.useRealTimers()
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

  it("names the DataVolume from the singular when the definition sets no prefix", async () => {
    const client = makeClient({})
    renderWithK8sProvider(
      <DiskUploadPanel ad={adWithoutPrefix} instance={instance} />,
      { client },
    )
    await waitFor(() =>
      expect(client.get).toHaveBeenCalledWith(
        "cdi.kubevirt.io",
        "v1beta1",
        "datavolumes",
        "vmdisk-demo",
        "tenant-root",
      ),
    )
  })

  it("reads CDIConfig cluster-scoped, not from the instance namespace", async () => {
    const client = makeClient({})
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    // CDIConfig is a cluster-scoped singleton; a namespace here builds
    // /namespaces/tenant-root/cdiconfigs/config, which is a 404.
    await waitFor(() =>
      expect(client.get).toHaveBeenCalledWith(
        "cdi.kubevirt.io",
        "v1beta1",
        "cdiconfigs",
        "config",
        undefined,
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

  it("does not poll a disk whose panel renders nothing", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const client = makeClient({
      source: { http: { url: "https://example.org/i.qcow2" } },
      phase: "Pending",
    })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    await waitFor(() => expect(dvReads(client)).toBe(1))
    await act(() => vi.advanceTimersByTimeAsync(15_000))
    expect(dvReads(client)).toBe(1)
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

  it("renders the whole awaiting-upload panel, not just the badge", async () => {
    const client = makeClient({ uploadProxyURL: "https://cdi-uploadproxy.example.org" })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    expect(
      await screen.findByRole("heading", { name: "Image upload" }),
    ).toBeInTheDocument()
    expect(
      screen.getByText("This disk was created from an uploaded image."),
    ).toBeInTheDocument()
    expect(await screen.findByText("Waiting for an image")).toBeInTheDocument()
    expect(screen.getByText("UploadReady")).toBeInTheDocument()
    // The panel exists to say the upload cannot happen in the browser; the
    // command alone does not say that.
    const guidance = screen.getByText(/the browser cannot reach the CDI/)
    expect(guidance.textContent).toContain("--image-path")
    expect(screen.getByRole("button", { name: "Copy command" })).toBeInTheDocument()
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

  it("does not report an unreadable CDIConfig as a cluster with no proxy URL", async () => {
    const client = makeClient({ cdiConfigError: new Error("forbidden") })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    expect(await screen.findByText(/could not be read/)).toBeInTheDocument()
    expect(screen.queryByText(/publishes no upload proxy URL/)).not.toBeInTheDocument()
  })

  it("withholds the command before the upload target exists", async () => {
    const client = makeClient({ phase: "Pending" })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    expect(await screen.findByText("Preparing upload target")).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()
  })

  it("advances from Pending to UploadReady without a manual refresh", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const client = makeClient({ phases: ["Pending", "UploadReady"] })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    expect(await screen.findByText("Preparing upload target")).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()
    await act(() => vi.advanceTimersByTimeAsync(5_000))
    expect(await screen.findByText("Waiting for an image")).toBeInTheDocument()
    expect(await screen.findByText(/virtctl image-upload/)).toBeInTheDocument()
    // ...and the poll stops there: UploadReady is where CDI waits for the
    // user, so polling on would be load with nothing to observe.
    const reads = dvReads(client)
    await act(() => vi.advanceTimersByTimeAsync(15_000))
    expect(dvReads(client)).toBe(reads)
  })

  it("withholds the command once the image is written", async () => {
    const client = makeClient({ phase: "Succeeded" })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    expect(await screen.findByText("Image uploaded")).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()
  })

  it("withholds the command after a failure and says to recreate the disk", async () => {
    // A Failed DataVolume has no upload server to talk to — CDI never
    // recreates one — and the phase covers bind failures where none existed.
    const client = makeClient({
      phase: "Failed",
      conditions: [{ type: "Bound", status: "False", message: "no capacity" }],
    })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={instance} />, { client })
    expect(await screen.findByText("Upload failed")).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()
    expect(screen.getByText(/Recreate the disk/)).toBeInTheDocument()
    // The reason CDI gave must survive: it is the only clue the user gets.
    expect(screen.getByText("no capacity")).toBeInTheDocument()
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
