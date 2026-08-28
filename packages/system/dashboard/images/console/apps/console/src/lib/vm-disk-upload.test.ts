import { describe, expect, it } from "vitest"
import {
  dataVolumeCapacity,
  isUploadSource,
  proxyURLFromCDIConfig,
  uploadState,
  usableProxyURL,
  virtctlUploadCommand,
} from "@/lib/vm-disk-upload.ts"
import type { CDIConfig, DataVolume } from "@/lib/vm-disk-upload.ts"

function dv(
  source: Record<string, unknown> | undefined,
  status?: DataVolume["status"],
  storage = "5Gi",
): DataVolume {
  return {
    apiVersion: "cdi.kubevirt.io/v1beta1",
    kind: "DataVolume",
    metadata: { name: "vm-disk-demo", namespace: "tenant-root" },
    ...(source === undefined
      ? {}
      : {
          spec: {
            source,
            storage: { resources: { requests: { storage } } },
          },
        }),
    ...(status ? { status } : {}),
  }
}

describe("isUploadSource", () => {
  it("recognises upload on both a DataVolume and a generic application spec", () => {
    expect(isUploadSource(dv({ upload: {} }))).toBe(true)
    expect(isUploadSource({ spec: { source: { upload: {} } } })).toBe(true)
  })

  it("rejects the other vm-disk sources", () => {
    expect(isUploadSource(dv({ http: { url: "https://example.org/i.qcow2" } }))).toBe(
      false,
    )
    expect(isUploadSource(dv({ pvc: { name: "vm-default-images-ubuntu" } }))).toBe(
      false,
    )
    expect(isUploadSource(dv({ blank: {} }))).toBe(false)
  })

  it("rejects missing and malformed specs without throwing", () => {
    expect(isUploadSource(dv(undefined))).toBe(false)
    expect(isUploadSource(undefined)).toBe(false)
    expect(isUploadSource({ spec: { source: "upload" } })).toBe(false)
  })

  it("does not mistake an inherited property for an upload source", () => {
    const inherited = Object.create({ upload: {} }) as Record<string, unknown>
    expect(isUploadSource(dv(inherited))).toBe(false)
  })
})

describe("uploadState", () => {
  it("is awaiting-upload only at UploadReady", () => {
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
    "PrepClaimInProgress",
    "RebindInProgress",
    "ExpansionInProgress",
    "UploadScheduled",
  ])("treats phase %s as preparing", (phase) => {
    expect(uploadState(dv({ upload: {} }, { phase })).stage).toBe("preparing")
  })

  it("reports Succeeded and explicit Failed distinctly", () => {
    expect(uploadState(dv({ upload: {} }, { phase: "Succeeded" })).stage).toBe(
      "succeeded",
    )
    expect(uploadState(dv({ upload: {} }, { phase: "Failed" })).stage).toBe("failed")
  })

  it.each(["Error", "OOMKilled", "ContainerCannotRun", "StartError", "DeadlineExceeded"])(
    "recognises terminal upload-pod reason %s even when CDI leaves UploadScheduled",
    (reason) => {
      const state = uploadState(
        dv(
          { upload: {} },
          {
            phase: "UploadScheduled",
            conditions: [
              {
                type: "Running",
                status: "False",
                reason,
                message: "upload server terminated",
              },
            ],
          },
        ),
      )
      expect(state).toMatchObject({
        stage: "failed",
        phase: "UploadScheduled",
        message: "upload server terminated",
      })
    },
  )

  it("recognises a terminal pod failure while the last phase is UploadReady", () => {
    expect(
      uploadState(
        dv(
          { upload: {} },
          {
            phase: "UploadReady",
            conditions: [
              { type: "Running", status: "False", reason: "OOMKilled" },
            ],
          },
        ),
      ),
    ).toMatchObject({ stage: "failed", message: "OOMKilled" })
  })

  it.each(["ContainerCreating", "PodInitializing", "Completed"])(
    "does not flash failed for non-terminal reason %s",
    (reason) => {
      const state = uploadState(
        dv(
          { upload: {} },
          {
            phase: "UploadScheduled",
            conditions: [
              { type: "Running", status: "False", reason, message: reason },
            ],
          },
        ),
      )
      expect(state.stage).toBe("preparing")
      expect(state.message).toBeUndefined()
    },
  )

  it("surfaces a non-terminal condition problem without calling it terminal", () => {
    const state = uploadState(
      dv(
        { upload: {} },
        {
          phase: "UploadScheduled",
          conditions: [
            {
              type: "Running",
              status: "False",
              reason: "ImagePullBackOff",
              message: "Unable to pull upload server image",
            },
          ],
        },
      ),
    )
    expect(state.stage).toBe("preparing")
    expect(state.message).toBe("Unable to pull upload server image")
  })

  it("prefers a Running failure message and falls back to Bound", () => {
    expect(
      uploadState(
        dv(
          { upload: {} },
          {
            phase: "Failed",
            conditions: [
              { type: "Bound", message: "bound failure" },
              { type: "Running", message: "upload server crashed" },
            ],
          },
        ),
      ).message,
    ).toBe("upload server crashed")
    expect(
      uploadState(
        dv(
          { upload: {} },
          {
            phase: "Failed",
            conditions: [
              { type: "Bound", message: "no capacity" },
              { type: "Running", message: "" },
            ],
          },
        ),
      ).message,
    ).toBe("no capacity")
  })

  it("carries progress through at UploadReady", () => {
    expect(
      uploadState(dv({ upload: {} }, { phase: "UploadReady", progress: "42.0%" }))
        .progress,
    ).toBe("42.0%")
  })

  it("recognises CDI's explicit Paused phase", () => {
    expect(uploadState(dv({ upload: {} }, { phase: "Paused" }))).toMatchObject({
      stage: "paused",
      phase: "Paused",
    })
  })

  it("is unknown for a missing DataVolume and an unrecognised phase", () => {
    expect(uploadState(undefined).stage).toBe("unknown")
    expect(uploadState(dv({ upload: {} }, { phase: "ImportInProgress" })).stage).toBe(
      "unknown",
    )
  })
})

describe("dataVolumeCapacity", () => {
  it("returns the requested virtual image capacity", () => {
    expect(dataVolumeCapacity(dv({ upload: {} }, undefined, "20Gi"))).toBe("20Gi")
  })

  it("ignores a missing or blank request", () => {
    expect(dataVolumeCapacity(dv(undefined))).toBeUndefined()
    expect(dataVolumeCapacity(dv({ upload: {} }, undefined, "   "))).toBeUndefined()
  })
})

describe("usableProxyURL", () => {
  it("accepts trimmed HTTP(S) URLs", () => {
    expect(usableProxyURL("  https://cdi-uploadproxy.example.org  ")).toBe(
      "https://cdi-uploadproxy.example.org",
    )
    expect(usableProxyURL("http://127.0.0.1:8443/upload")).toBe(
      "http://127.0.0.1:8443/upload",
    )
  })

  it.each([
    undefined,
    "",
    "   ",
    "cdi-uploadproxy.example.org",
    "ftp://example.org",
    "https://user:secret@example.org",
    "https://example.org/line\nbreak",
  ])("rejects an unusable or unsafe URL %#", (value) => {
    expect(usableProxyURL(value)).toBeUndefined()
  })
})

describe("proxyURLFromCDIConfig", () => {
  function config(
    specURL: string | undefined,
    statusURL: string | undefined,
  ): CDIConfig {
    return {
      apiVersion: "cdi.kubevirt.io/v1beta1",
      kind: "CDIConfig",
      metadata: { name: "config" },
      ...(specURL === undefined ? {} : { spec: { uploadProxyURLOverride: specURL } }),
      status: statusURL === undefined ? {} : { uploadProxyURL: statusURL },
    }
  }

  it("uses the explicit spec override before discovered status", () => {
    expect(
      proxyURLFromCDIConfig(
        config("https://custom.example.org", "https://discovered.example.org"),
      ),
    ).toBe("https://custom.example.org")
  })

  it("uses status when no override exists", () => {
    expect(proxyURLFromCDIConfig(config(undefined, "https://discovered.example.org"))).toBe(
      "https://discovered.example.org",
    )
  })

  it("does not fall through an explicit blank or invalid override", () => {
    expect(proxyURLFromCDIConfig(config("", "https://discovered.example.org"))).toBeUndefined()
    expect(
      proxyURLFromCDIConfig(config("ftp://invalid.example.org", "https://ok.example.org")),
    ).toBeUndefined()
  })
})

describe("virtctlUploadCommand", () => {
  it("reuses the chart-created DataVolume with shell-safe arguments", () => {
    expect(
      virtctlUploadCommand({
        name: "vm-disk-demo",
        namespace: "tenant-root",
        uploadProxyURL: "https://cdi-uploadproxy.example.org",
      }),
    ).toBe(
      "virtctl image-upload dv 'vm-disk-demo' --no-create --namespace 'tenant-root' --image-path './disk.qcow2' --uploadproxy-url 'https://cdi-uploadproxy.example.org' --insecure",
    )
  })

  it("quotes administrator-controlled shell metacharacters and apostrophes", () => {
    expect(
      virtctlUploadCommand({
        name: "vm-disk-demo",
        namespace: "tenant-root",
        uploadProxyURL:
          "https://proxy.example.org/upload?next=$(touch%20/tmp/pwn)&note=it's;literal`too`",
      }),
    ).toBe(
      "virtctl image-upload dv 'vm-disk-demo' --no-create --namespace 'tenant-root' --image-path './disk.qcow2' --uploadproxy-url 'https://proxy.example.org/upload?next=$(touch%20/tmp/pwn)&note=it'\"'\"'s;literal`too`' --insecure",
    )
  })

  it.each([undefined, "", "   ", "not-a-url", "ftp://example.org"])(
    "withholds the command without a real HTTP(S) proxy URL %#",
    (uploadProxyURL) => {
      expect(
        virtctlUploadCommand({
          name: "vm-disk-demo",
          namespace: "tenant-root",
          uploadProxyURL,
        }),
      ).toBeUndefined()
    },
  )
})
