import { describe, it, expect } from "vitest"
import type { K8sCondition } from "@cozystack/k8s-client"
import type { ApplicationInstance } from "@cozystack/types"
import { readyCondition } from "./status.ts"

function instance(kind: string, ...conditions: K8sCondition[]): ApplicationInstance {
  return {
    apiVersion: "apps.cozystack.io/v1alpha1",
    kind,
    metadata: { name: "x" },
    status: { conditions },
  } as ApplicationInstance
}

const ready: K8sCondition = { type: "Ready", status: "True", reason: "InstallSucceeded" }

describe("readyCondition", () => {
  it("returns Ready when the application has no WorkloadsReady", () => {
    expect(readyCondition(instance("Postgres", ready))?.status).toBe("True")
  })

  it("is not ready while a DataVolume is not ready, whatever Ready says", () => {
    const got = readyCondition(
      instance("VMDisk", ready, {
        type: "WorkloadsReady",
        status: "False",
        reason: "DataVolumeNotReady",
        message: "DataVolume x is ImportInProgress",
      }),
    )
    expect(got?.status).toBe("False")
    expect(got?.reason).toBe("DataVolumeNotReady")
    expect(got?.message).toBe("DataVolume x is ImportInProgress")
  })

  // A stopped VM has no pod, so its monitor reports it not operational.
  it("stays ready for a halted VM whose workloads are not ready for no named reason", () => {
    const got = readyCondition(
      instance("VMInstance", ready, {
        type: "WorkloadsReady",
        status: "False",
        reason: "WorkloadMonitorCheck",
        message: "One or more workloads are not operational",
      }),
    )
    expect(got?.status).toBe("True")
    expect(got?.reason).toBe("InstallSucceeded")
  })

  it("keeps a failing Ready ahead of the workloads", () => {
    const failed: K8sCondition = { type: "Ready", status: "False", reason: "InstallFailed" }
    const got = readyCondition(
      instance("VMDisk", failed, { type: "WorkloadsReady", status: "False", reason: "DataVolumeNotReady" }),
    )
    expect(got?.reason).toBe("InstallFailed")
  })
})
