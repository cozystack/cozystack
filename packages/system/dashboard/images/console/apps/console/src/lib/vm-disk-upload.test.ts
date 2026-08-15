import { describe, it, expect } from "vitest"
import {
  isUploadSource,
  uploadState,
  virtctlUploadCommand,
  UPLOAD_PROXY_URL_PLACEHOLDER,
  type DataVolume,
} from "./vm-disk-upload.ts"

function dv(
  source: Record<string, unknown> | undefined,
  status?: DataVolume["status"],
): DataVolume {
  return {
    apiVersion: "cdi.kubevirt.io/v1beta1",
    kind: "DataVolume",
    metadata: { name: "vm-disk-demo", namespace: "tenant-root" },
    ...(source === undefined ? {} : { spec: { source } }),
    ...(status ? { status } : {}),
  }
}

describe("isUploadSource", () => {
  it("recognises an upload source", () => {
    expect(isUploadSource(dv({ upload: {} }))).toBe(true)
  })

  it("rejects the other vm-disk sources", () => {
    expect(isUploadSource(dv({ http: { url: "https://example.org/i.qcow2" } }))).toBe(false)
    expect(isUploadSource(dv({ pvc: { name: "vm-default-images-ubuntu" } }))).toBe(false)
    expect(isUploadSource(dv({ blank: {} }))).toBe(false)
  })

  it("rejects a DataVolume with no source and an unreadable one", () => {
    expect(isUploadSource(dv(undefined))).toBe(false)
    expect(isUploadSource(undefined)).toBe(false)
  })

  it("does not mistake an inherited property for an upload source", () => {
    // Object.create puts `upload` on the prototype, where `in` would find it.
    const inherited = Object.create({ upload: {} }) as Record<string, unknown>
    expect(isUploadSource(dv(inherited))).toBe(false)
  })
})

describe("uploadState", () => {
  it("is awaiting-upload only at UploadReady — the one phase CDI accepts data in", () => {
    expect(uploadState(dv({ upload: {} }, { phase: "UploadReady" })).stage).toBe(
      "awaiting-upload",
    )
  })

  it.each([
    "",
    "Pending",
    "PVCBound",
    "WaitForFirstConsumer",
    "PendingPopulation",
    "UploadScheduled",
  ])("treats phase %s as preparing", (phase) => {
    expect(uploadState(dv({ upload: {} }, { phase })).stage).toBe("preparing")
  })

  it("reports Succeeded and Failed distinctly", () => {
    expect(uploadState(dv({ upload: {} }, { phase: "Succeeded" })).stage).toBe("succeeded")
    expect(uploadState(dv({ upload: {} }, { phase: "Failed" })).stage).toBe("failed")
  })

  it("surfaces the failure message from the Running condition", () => {
    const state = uploadState(
      dv({ upload: {} }, {
        phase: "Failed",
        conditions: [
          { type: "Bound", message: "bound" },
          { type: "Running", message: "upload server crashed" },
        ],
      }),
    )
    expect(state.message).toBe("upload server crashed")
  })

  it("falls back to the Bound condition when Running carries no message", () => {
    const state = uploadState(
      dv({ upload: {} }, {
        phase: "Failed",
        conditions: [{ type: "Bound", message: "no capacity" }],
      }),
    )
    expect(state.message).toBe("no capacity")
  })

  it("carries progress through at UploadReady", () => {
    expect(
      uploadState(dv({ upload: {} }, { phase: "UploadReady", progress: "42.0%" })).progress,
    ).toBe("42.0%")
  })

  it("is unknown for a missing DataVolume and for an unrecognised phase", () => {
    expect(uploadState(undefined).stage).toBe("unknown")
    expect(uploadState(dv({ upload: {} }, { phase: "ImportInProgress" })).stage).toBe(
      "unknown",
    )
  })

  it("does not report a phase it was never given", () => {
    expect(uploadState(dv({ upload: {} }, {})).stage).toBe("preparing")
    expect(uploadState(dv({ upload: {} }, {})).phase).toBe("")
  })
})

describe("virtctlUploadCommand", () => {
  it("reuses the chart-created DataVolume and skips certificate verification", () => {
    const cmd = virtctlUploadCommand({
      name: "vm-disk-demo",
      namespace: "tenant-root",
      uploadProxyURL: "https://cdi-uploadproxy.example.org",
    })
    expect(cmd).toContain("virtctl image-upload dv vm-disk-demo")
    // The Helm release owns the DataVolume, so creating another one fails.
    expect(cmd).toContain("--no-create")
    // cdi-uploadproxy is published with TLS passthrough: CDI's own cert is on the wire.
    expect(cmd).toContain("--insecure")
    expect(cmd).toContain("--namespace=tenant-root")
    expect(cmd).toContain("--uploadproxy-url=https://cdi-uploadproxy.example.org")
  })

  it("falls back to a visible placeholder when the cluster published no proxy URL", () => {
    for (const uploadProxyURL of [undefined, "", "   "]) {
      const cmd = virtctlUploadCommand({
        name: "vm-disk-demo",
        namespace: "tenant-root",
        uploadProxyURL,
      })
      expect(cmd).toContain(`--uploadproxy-url=${UPLOAD_PROXY_URL_PLACEHOLDER}`)
    }
  })
})
