import { describe, it, expect, vi, afterEach } from "vitest"
import { screen, waitFor, cleanup, fireEvent } from "@testing-library/react"
import { K8sClient } from "@cozystack/k8s-client"
import { renderWithK8sProvider } from "../../test-utils/render.tsx"
import { VMDiskUpload } from "./VMDiskUpload.tsx"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"

const ad: ApplicationDefinition = {
  apiVersion: "cozystack.io/v1alpha1",
  kind: "ApplicationDefinition",
  metadata: { name: "vm-disk" },
  spec: {
    application: { kind: "VMDisk", plural: "vmdisks", singular: "vm-disk", openAPISchema: "{}" },
  },
}

const instance: ApplicationInstance = {
  apiVersion: "apps.cozystack.io/v1alpha1",
  kind: "VMDisk",
  metadata: { name: "data", namespace: "tenant-root" },
  spec: { source: { upload: {} } },
}

// The kubemox VirtualMachineVolume behind the disk, waiting for its file.
function makeClient(uploadTarget?: Record<string, string>) {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockResolvedValue({
    apiVersion: "proxmox.alperen.cloud/v1alpha1",
    kind: "VirtualMachineVolumeList",
    metadata: {},
    items: [
      {
        apiVersion: "proxmox.alperen.cloud/v1alpha1",
        kind: "VirtualMachineVolume",
        metadata: { name: "vm-disk-data" },
        status: { phase: "Pending", ...(uploadTarget ? { uploadTarget } : {}) },
      },
    ],
  })
  vi.spyOn(client, "watch").mockReturnValue(() => {})
  return client
}

// The browser's XMLHttpRequest, scripted: it records what was sent and lets
// the test drive progress and the answer.
class FakeXHR {
  static last: FakeXHR | undefined
  static status = 201
  static responseText = JSON.stringify({ kind: "Status", message: "local:import/vm-disk-data.qcow2" })
  method = ""
  url = ""
  body: unknown
  status = 0
  responseText = ""
  upload = { onprogress: null as ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null }
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  open(method: string, url: string) {
    this.method = method
    this.url = url
    FakeXHR.last = this
  }
  send(body: unknown) {
    this.body = body
  }
  finish() {
    this.status = FakeXHR.status
    this.responseText = FakeXHR.responseText
    this.onload?.()
  }
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  FakeXHR.last = undefined
  FakeXHR.status = 201
})

function pickFile(name: string) {
  const file = new File(["QFI\xfb"], name, { type: "application/octet-stream" })
  const input = document.querySelector('input[type="file"]') as HTMLInputElement
  fireEvent.change(input, { target: { files: [file] } })
  return file
}

describe("VMDiskUpload", () => {
  it("shows nothing while the volume waits for no upload", async () => {
    const client = makeClient()
    renderWithK8sProvider(<VMDiskUpload ad={ad} instance={instance} />, { client, config: { vmSubstrate: "proxmox" } })
    await waitFor(() => expect(client.list).toHaveBeenCalled())
    expect(screen.queryByText("Upload image")).toBeNull()
  })

  it("shows nothing on the kubevirt substrate", () => {
    const client = makeClient({ content: "import" })
    renderWithK8sProvider(
      <VMDiskUpload ad={ad} instance={{ ...instance, spec: { source: { upload: {} } } }} />,
      { client },
    )
    expect(client.list).not.toHaveBeenCalled()
    expect(screen.queryByText("Upload image")).toBeNull()
  })

  it("streams the picked file to the volume's upload subresource with progress", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeXHR)
    const client = makeClient({ node: "pve1", storage: "local", content: "import", filename: "vm-disk-data" })
    renderWithK8sProvider(<VMDiskUpload ad={ad} instance={instance} />, { client, config: { vmSubstrate: "proxmox" } })
    await screen.findByText("Upload image")
    expect(client.list).toHaveBeenCalledWith(
      "proxmox.alperen.cloud",
      "v1alpha1",
      "virtualmachinevolumes",
      "tenant-root",
      expect.objectContaining({ fieldSelector: "metadata.name=vm-disk-data" }),
    )
    expect((document.querySelector('input[type="file"]') as HTMLInputElement).accept).toBe(".qcow2,.raw,.vmdk,.ova")

    const file = pickFile("debian 13.qcow2")
    await waitFor(() => expect(FakeXHR.last).toBeDefined())
    const xhr = FakeXHR.last!
    expect(xhr.method).toBe("POST")
    expect(xhr.url).toBe(
      "/k8s/apis/subresources.proxmox.cozystack.io/v1alpha1/namespaces/tenant-root/virtualmachinevolumes/vm-disk-data/upload?filename=debian%2013.qcow2",
    )
    expect(xhr.body).toBe(file)

    xhr.upload.onprogress?.({ lengthComputable: true, loaded: 1, total: 4 })
    await screen.findByText("Uploading… 25%")
    expect((screen.getByText("Upload image").closest("button") as HTMLButtonElement).disabled).toBe(true)

    xhr.finish()
    await screen.findByText("Uploaded; importing")
  })

  it("shows the proxy's message when the upload is refused", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeXHR)
    FakeXHR.status = 400
    FakeXHR.responseText = JSON.stringify({ kind: "Status", message: '"a.vhdx" is not an image the volume takes' })
    const client = makeClient({ content: "iso" })
    renderWithK8sProvider(<VMDiskUpload ad={ad} instance={instance} />, { client, config: { vmSubstrate: "proxmox" } })
    await screen.findByText("Upload image")
    expect((document.querySelector('input[type="file"]') as HTMLInputElement).accept).toBe(".iso,.img")

    pickFile("a.vhdx")
    await waitFor(() => expect(FakeXHR.last).toBeDefined())
    FakeXHR.last!.finish()
    await screen.findByText('Upload failed: "a.vhdx" is not an image the volume takes')
    expect((screen.getByText("Upload image").closest("button") as HTMLButtonElement).disabled).toBe(false)
  })
})
