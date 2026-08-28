import { QueryClient } from "@tanstack/react-query"
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { MemoryRouter } from "react-router"
import { K8sClient, K8sProvider } from "@cozystack/k8s-client"
import type { K8sList, WatchEvent } from "@cozystack/k8s-client"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import { DiskUploadPanel } from "@/routes/detail/DiskUploadPanel.tsx"
import { renderWithK8sProvider } from "@/test-utils/render.tsx"
import type { CDIConfig, DataVolume } from "@/lib/vm-disk-upload.ts"

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

function makeInstance({
  name = "demo",
  source = { upload: {} },
  ready = "True",
}: {
  name?: string
  source?: Record<string, unknown>
  ready?: "True" | "False" | "Unknown" | undefined
} = {}): ApplicationInstance {
  return {
    apiVersion: "apps.cozystack.io/v1alpha1",
    kind: "VMDisk",
    metadata: { name, namespace: "tenant-root" },
    spec: { source },
    ...(ready === undefined
      ? {}
      : {
          status: {
            conditions: [
              {
                type: "Ready",
                status: ready,
                reason: ready === "True" ? "ReconciliationSucceeded" : "Progressing",
              },
            ],
          },
        }),
  }
}

function makeDV({
  name = "vm-disk-demo",
  source = { upload: {} },
  phase = "UploadReady",
  progress,
  capacity = "5Gi",
  conditions,
}: {
  name?: string
  source?: Record<string, unknown>
  phase?: string
  progress?: string
  capacity?: string
  conditions?: NonNullable<DataVolume["status"]>["conditions"]
} = {}): DataVolume {
  return {
    apiVersion: "cdi.kubevirt.io/v1beta1",
    kind: "DataVolume",
    metadata: { name, namespace: "tenant-root" },
    spec: {
      source,
      storage: { resources: { requests: { storage: capacity } } },
    },
    status: { phase, progress, conditions },
  }
}

function dvList(items: DataVolume[], resourceVersion = "10"): K8sList<DataVolume> {
  return {
    apiVersion: "cdi.kubevirt.io/v1beta1",
    kind: "DataVolumeList",
    metadata: { resourceVersion },
    items,
  }
}

interface ClusterFixture {
  items?: DataVolume[]
  uploadProxyURL?: string
  uploadProxyURLOverride?: string
  cdiConfigError?: Error
  cdiConfigPending?: boolean
  canCreateUploadToken?: boolean
  canGetPVC?: boolean
  ssarErrorResource?: string
}

function makeHarness(fixture: ClusterFixture = {}) {
  const client = new K8sClient()
  const handlers: Array<(event: WatchEvent<DataVolume>) => void> = []
  const watchErrorHandlers: Array<(error: Error) => void> = []
  const stopWatch = vi.fn()
  const listSpy = vi
    .spyOn(client, "list")
    .mockResolvedValue(dvList(fixture.items ?? [makeDV()]) as never)
  const getSpy = vi.spyOn(client, "get").mockImplementation(
    async (_group: string, _version: string, plural: string) => {
      if (plural !== "cdiconfigs") throw new Error(`Unexpected GET for ${plural}`)
      if (fixture.cdiConfigPending) return new Promise(() => {})
      if (fixture.cdiConfigError) throw fixture.cdiConfigError
      return {
        apiVersion: "cdi.kubevirt.io/v1beta1",
        kind: "CDIConfig",
        metadata: { name: "config" },
        ...(fixture.uploadProxyURLOverride === undefined
          ? {}
          : { spec: { uploadProxyURLOverride: fixture.uploadProxyURLOverride } }),
        status: {
          uploadProxyURL:
            fixture.uploadProxyURL ?? "https://cdi-uploadproxy.example.org",
        },
      } as CDIConfig
    },
  )
  const createSpy = vi.spyOn(client, "create").mockImplementation(
    async (_group: string, _version: string, plural: string, body: unknown) => {
      if (plural !== "selfsubjectaccessreviews") {
        throw new Error(`Unexpected create for ${plural}`)
      }
      const request = body as {
        spec?: { resourceAttributes?: { resource?: string } }
      }
      const resource = request.spec?.resourceAttributes?.resource ?? ""
      if (fixture.ssarErrorResource === resource) {
        throw new Error(`SSAR failed for ${resource}`)
      }
      const allowed =
        resource === "persistentvolumeclaims"
          ? (fixture.canGetPVC ?? true)
          : (fixture.canCreateUploadToken ?? true)
      return {
        apiVersion: "authorization.k8s.io/v1",
        kind: "SelfSubjectAccessReview",
        metadata: { name: "" },
        spec: request.spec,
        status: { allowed },
      }
    },
  )
  const watchSpy = vi.spyOn(client, "watch").mockImplementation(
    (
      _group,
      _version,
      _plural,
      _namespace,
      _resourceVersion,
      onEvent,
      onError,
    ) => {
      handlers.push(onEvent as (event: WatchEvent<DataVolume>) => void)
      if (onError) watchErrorHandlers.push(onError)
      return stopWatch
    },
  )

  return {
    client,
    listSpy,
    getSpy,
    createSpy,
    watchSpy,
    stopWatch,
    async emit(event: WatchEvent<DataVolume>) {
      await waitFor(() => expect(handlers.length).toBeGreaterThan(0))
      act(() => handlers.at(-1)?.(event))
    },
    async failWatch() {
      await waitFor(() => expect(watchErrorHandlers.length).toBeGreaterThan(0))
      act(() => watchErrorHandlers.at(-1)?.(new Error("watch ended")))
    },
  }
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  Reflect.deleteProperty(navigator, "clipboard")
})

describe("DiskUploadPanel query lifecycle", () => {
  it("field-selects and watches the exact DataVolume name", async () => {
    const h = makeHarness()
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })

    await waitFor(() =>
      expect(h.listSpy).toHaveBeenCalledWith(
        "cdi.kubevirt.io",
        "v1beta1",
        "datavolumes",
        "tenant-root",
        expect.objectContaining({ fieldSelector: "metadata.name=vm-disk-demo" }),
      ),
    )
    await waitFor(() =>
      expect(h.watchSpy).toHaveBeenCalledWith(
        "cdi.kubevirt.io",
        "v1beta1",
        "datavolumes",
        "tenant-root",
        "10",
        expect.any(Function),
        expect.any(Function),
        expect.objectContaining({ fieldSelector: "metadata.name=vm-disk-demo" }),
      ),
    )
  })

  it("uses the singular release fallback in the selector", async () => {
    const h = makeHarness({ items: [makeDV({ name: "vmdisk-demo" })] })
    renderWithK8sProvider(
      <DiskUploadPanel ad={adWithoutPrefix} instance={makeInstance()} />,
      { client: h.client },
    )
    await waitFor(() =>
      expect(h.listSpy).toHaveBeenCalledWith(
        "cdi.kubevirt.io",
        "v1beta1",
        "datavolumes",
        "tenant-root",
        expect.objectContaining({ fieldSelector: "metadata.name=vmdisk-demo" }),
      ),
    )
  })

  it("queries only the exact child for a non-upload VMDisk and stays hidden", async () => {
    const h = makeHarness({
      items: [makeDV({ source: { http: { url: "https://example.org/i" } } })],
    })
    const { container } = renderWithK8sProvider(
      <DiskUploadPanel
        ad={ad}
        instance={makeInstance({ source: { http: { url: "https://example.org/i" } } })}
      />,
      { client: h.client },
    )
    await waitFor(() => expect(h.watchSpy).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
    expect(h.listSpy).toHaveBeenCalledTimes(1)
    expect(h.watchSpy).toHaveBeenCalledTimes(1)
    expect(h.getSpy).not.toHaveBeenCalled()
    expect(h.createSpy).not.toHaveBeenCalled()
  })

  it("surfaces a preserved upload DataVolume after the VMDisk source changes", async () => {
    const h = makeHarness()
    renderWithK8sProvider(
      <DiskUploadPanel
        ad={ad}
        instance={makeInstance({ source: { http: { url: "https://example.org/i" } } })}
      />,
      { client: h.client },
    )

    expect(await screen.findByText(/preserved DataVolume is still an upload target/)).toBeInTheDocument()
    expect(await screen.findByText(/virtctl image-upload/)).toBeInTheDocument()
  })

  it("shows the intended panel but defers the child query until the VMDisk is Ready", async () => {
    const h = makeHarness()
    renderWithK8sProvider(
      <DiskUploadPanel ad={ad} instance={makeInstance({ ready: "False" })} />,
      { client: h.client },
    )
    expect(await screen.findByText(/Waiting for disk reconciliation/)).toBeInTheDocument()
    expect(h.listSpy).not.toHaveBeenCalled()
    expect(h.getSpy).not.toHaveBeenCalled()
    expect(h.createSpy).not.toHaveBeenCalled()
  })

  it("recovers from an empty list and follows ADDED then MODIFIED to completion", async () => {
    const h = makeHarness({ items: [] })
    const { unmount } = renderWithK8sProvider(
      <DiskUploadPanel ad={ad} instance={makeInstance()} />,
      { client: h.client },
    )
    expect(await screen.findByText("Preparing upload target")).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()

    await h.emit({ type: "ADDED", object: makeDV({ phase: "UploadReady" }) })
    expect(await screen.findByText("Upload target ready")).toBeInTheDocument()
    expect(await screen.findByText(/virtctl image-upload/)).toBeInTheDocument()

    await h.emit({ type: "MODIFIED", object: makeDV({ phase: "Succeeded" }) })
    expect(await screen.findByText("Image uploaded")).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()

    unmount()
    expect(h.stopWatch).toHaveBeenCalled()
  })

  it("reopens a closed watch after a relist returns the same resourceVersion", async () => {
    const h = makeHarness()
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText("Upload target ready")).toBeInTheDocument()
    await waitFor(() => expect(h.watchSpy).toHaveBeenCalledTimes(1))

    await h.failWatch()
    await waitFor(() => expect(h.listSpy).toHaveBeenCalledTimes(2), { timeout: 2500 })
    await waitFor(() => expect(h.watchSpy).toHaveBeenCalledTimes(2), { timeout: 2500 })

    await h.emit({ type: "MODIFIED", object: makeDV({ phase: "Succeeded" }) })
    expect(await screen.findByText("Image uploaded")).toBeInTheDocument()
  })

  it("keeps a failed read visible and lets Refresh recover", async () => {
    const h = makeHarness()
    const error = Object.assign(new Error("Forbidden"), { status: 401 })
    h.listSpy
      .mockRejectedValueOnce(error)
      .mockResolvedValueOnce(dvList([makeDV()]) as never)
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText(/could not read the upload target/)).toBeInTheDocument()
    expect(screen.getByText("Unknown")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }))
    expect(await screen.findByText("Upload target ready")).toBeInTheDocument()
  })

  it("does not call a pending read an absent upload target", async () => {
    const h = makeHarness()
    h.listSpy.mockImplementation(() => new Promise(() => {}))
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText("Reading the upload target…")).toBeInTheDocument()
    expect(screen.queryByText(/CDI has not created/)).not.toBeInTheDocument()
  })

  it("reports a mismatched child source instead of silently hiding", async () => {
    const h = makeHarness({
      items: [
        makeDV({ source: { http: { url: "https://example.org/image.qcow2" } } }),
      ],
    })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText(/underlying DataVolume is not configured/)).toBeInTheDocument()
    expect(h.getSpy).not.toHaveBeenCalled()
    expect(h.createSpy).not.toHaveBeenCalled()
  })

  it("never carries the previous disk through production placeholderData", async () => {
    const h = makeHarness()
    const pending = new Promise<never>(() => {})
    h.listSpy.mockImplementation(
      async (_group, _version, _plural, _namespace, search) => {
        if (search?.fieldSelector === "metadata.name=vm-disk-demo") {
          return dvList([makeDV()]) as never
        }
        return pending
      },
    )
    const queryClient = new QueryClient({
      defaultOptions: {
        queries: {
          retry: false,
          placeholderData: (previous: unknown) => previous,
        },
      },
    })
    const tree = (instance: ApplicationInstance) => (
      <K8sProvider client={h.client} queryClient={queryClient}>
        <MemoryRouter>
          <DiskUploadPanel ad={ad} instance={instance} />
        </MemoryRouter>
      </K8sProvider>
    )
    const result = render(tree(makeInstance()))
    expect(await screen.findByText(/virtctl image-upload/)).toBeInTheDocument()

    result.rerender(tree(makeInstance({ name: "other" })))
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()
    expect(screen.getByText("Preparing upload target")).toBeInTheDocument()
    expect(screen.getByText("Reading the upload target…")).toBeInTheDocument()
  })
})

describe("DiskUploadPanel states and prerequisites", () => {
  it("does not query upload prerequisites before UploadReady", async () => {
    const h = makeHarness({ items: [makeDV({ phase: "Pending" })] })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText("Preparing upload target")).toBeInTheDocument()
    expect(h.getSpy).not.toHaveBeenCalled()
    expect(h.createSpy).not.toHaveBeenCalled()
  })

  it("shows progress, capacity, and a command only when every prerequisite passes", async () => {
    const h = makeHarness({
      items: [makeDV({ progress: "42.0%", capacity: "20Gi" })],
    })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText("Upload target ready")).toBeInTheDocument()
    expect(screen.getByText("42.0%")).toBeInTheDocument()
    expect(screen.getByText(/Virtual image capacity:/)).toHaveTextContent("20Gi")
    const command = await screen.findByText(/virtctl image-upload/)
    expect(command).toHaveTextContent(
      "virtctl image-upload dv 'vm-disk-demo' --no-create --namespace 'tenant-root'",
    )
    expect(command).toHaveTextContent(
      "--uploadproxy-url 'https://cdi-uploadproxy.example.org'",
    )
    expect(command).not.toHaveTextContent("--insecure")
    expect(h.getSpy).toHaveBeenCalledWith(
      "cdi.kubevirt.io",
      "v1beta1",
      "cdiconfigs",
      "config",
      undefined,
    )
    expect(h.createSpy).toHaveBeenCalledWith(
      "authorization.k8s.io",
      "v1",
      "selfsubjectaccessreviews",
      expect.objectContaining({
        spec: {
          resourceAttributes: {
            namespace: "tenant-root",
            group: "",
            resource: "persistentvolumeclaims",
            verb: "get",
          },
        },
      }),
    )
  })

  it("uses CDIConfig spec override precedence", async () => {
    const h = makeHarness({
      uploadProxyURLOverride: "https://override.example.org",
      uploadProxyURL: "https://status.example.org",
    })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText(/virtctl image-upload/)).toHaveTextContent(
      "--uploadproxy-url 'https://override.example.org'",
    )
  })

  it("withholds the command while CDIConfig is loading", async () => {
    const h = makeHarness({ cdiConfigPending: true })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText("Checking upload prerequisites…")).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()
  })

  it("withholds the command when CDIConfig cannot be read", async () => {
    const h = makeHarness({ cdiConfigError: new Error("unavailable") })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText(/could not verify the upload proxy/)).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()
  })

  it.each([
    ["missing", ""],
    ["insecure", "http://proxy.example.org"],
    ["invalid", "ftp://proxy.example.org"],
  ])("withholds the command for a %s proxy URL", async (_label, uploadProxyURL) => {
    const h = makeHarness({ uploadProxyURL })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText(/does not advertise a usable upload proxy/)).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()
  })

  it.each([
    ["uploadtokenrequests", { canCreateUploadToken: false }],
    ["persistentvolumeclaims", { canGetPVC: false }],
  ] as const)("withholds the command when %s permission is denied", async (_resource, fixture) => {
    const h = makeHarness(fixture)
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText(/does not have all permissions required/)).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()
    expect(h.createSpy).toHaveBeenCalledTimes(2)
  })

  it("withholds the command when a permission check fails", async () => {
    const h = makeHarness({ ssarErrorResource: "persistentvolumeclaims" })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText(/could not verify your upload permissions/)).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()
  })

  it("reruns the proxy and both permission checks when Retry recovers", async () => {
    const h = makeHarness()
    h.getSpy.mockRejectedValueOnce(new Error("unavailable"))
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText(/could not verify the upload proxy/)).toBeInTheDocument()
    expect(h.createSpy).toHaveBeenCalledTimes(2)

    fireEvent.click(screen.getByRole("button", { name: "Retry checks" }))

    expect(await screen.findByText(/virtctl image-upload/)).toBeInTheDocument()
    expect(h.getSpy).toHaveBeenCalledTimes(2)
    expect(h.createSpy).toHaveBeenCalledTimes(4)
  })

  it("disables Retry while a prerequisite refresh is in flight", async () => {
    const h = makeHarness({ cdiConfigError: new Error("unavailable") })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    const retry = await screen.findByRole("button", { name: "Retry checks" })
    h.getSpy.mockImplementationOnce(() => new Promise(() => {}))

    fireEvent.click(retry)

    await waitFor(() => expect(retry).toBeDisabled())
  })

  it("withholds the command once the image is written", async () => {
    const h = makeHarness({ items: [makeDV({ phase: "Succeeded" })] })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText("Image uploaded")).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()
  })

  it("shows an explicit DataVolume failure and its reason", async () => {
    const h = makeHarness({
      items: [
        makeDV({
          phase: "Failed",
          conditions: [{ type: "Bound", status: "False", message: "no capacity" }],
        }),
      ],
    })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText("Upload failed")).toBeInTheDocument()
    expect(screen.getByText("no capacity")).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()
  })

  it("shows a terminal upload-pod failure that CDI leaves at UploadScheduled", async () => {
    const h = makeHarness({
      items: [
        makeDV({
          phase: "UploadScheduled",
          conditions: [
            {
              type: "Running",
              status: "False",
              reason: "OOMKilled",
              message: "upload server ran out of memory",
            },
          ],
        }),
      ],
    })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText("Upload failed")).toBeInTheDocument()
    expect(screen.getByText("upload server ran out of memory")).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()
  })

  it("shows Paused and withholds the command for CDI's paused phase", async () => {
    const h = makeHarness({ items: [makeDV({ phase: "Paused" })] })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText("Upload paused")).toBeInTheDocument()
    expect(screen.getByText("Paused")).toBeInTheDocument()
    expect(screen.getByText(/withheld until it resumes/)).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()
  })

  it("shows Unknown and withholds the command for an incompatible phase", async () => {
    const h = makeHarness({ items: [makeDV({ phase: "ImportInProgress" })] })
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByText("Unknown")).toBeInTheDocument()
    expect(screen.getByText("ImportInProgress")).toBeInTheDocument()
    expect(screen.queryByText(/virtctl image-upload/)).not.toBeInTheDocument()
  })
})

describe("DiskUploadPanel copy feedback", () => {
  it("keeps the command selectable but disables unavailable clipboard access", async () => {
    const h = makeHarness()
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    expect(await screen.findByLabelText("Copy command")).toBeDisabled()
    expect(screen.getByText(/virtctl image-upload/).tagName).toBe("PRE")
  })

  it("copies the exact command and announces success", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    })
    const h = makeHarness()
    renderWithK8sProvider(<DiskUploadPanel ad={ad} instance={makeInstance()} />, {
      client: h.client,
    })
    fireEvent.click(await screen.findByLabelText("Copy command"))
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(
        "virtctl image-upload dv 'vm-disk-demo' --no-create --namespace 'tenant-root' --image-path './disk.qcow2' --uploadproxy-url 'https://cdi-uploadproxy.example.org'",
      ),
    )
    expect(screen.getByLabelText("Command copied")).toBeInTheDocument()
  })
})
