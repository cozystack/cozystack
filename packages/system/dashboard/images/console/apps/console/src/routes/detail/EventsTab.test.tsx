import { describe, it, expect, vi, afterEach } from "vitest"
import { waitFor, cleanup } from "@testing-library/react"
import { K8sClient } from "@cozystack/k8s-client"
import { renderWithK8sProvider } from "../../test-utils/render.tsx"
import { EventsTab } from "./EventsTab.tsx"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"

vi.mock("../../lib/tenant-context.tsx", () => ({
  useTenantContext: () => ({
    tenants: [],
    selectedTenant: "root",
    selectTenant: () => {},
    tenantNamespace: "tenant-root",
    isLoading: false,
    error: null,
  }),
}))

function makeAd(release?: { prefix?: string }): ApplicationDefinition {
  return {
    apiVersion: "cozystack.io/v1alpha1",
    kind: "ApplicationDefinition",
    metadata: { name: "virtual-machine" },
    spec: {
      application: {
        kind: "VMInstance",
        plural: "vminstances",
        singular: "vm-instance",
        openAPISchema: "{}",
      },
      ...(release ? { release } : {}),
    },
  }
}

const instance: ApplicationInstance = {
  apiVersion: "apps.cozystack.io/v1alpha1",
  kind: "VMInstance",
  metadata: { name: "demo-vm", namespace: "tenant-root" },
}

function makeClient() {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockResolvedValue({
    apiVersion: "v1",
    kind: "List",
    metadata: {},
    items: [],
  })
  vi.spyOn(client, "watch").mockReturnValue(() => {})
  return client
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe("EventsTab Helm release selector", () => {
  it("builds the Helm instance selector from the prefixed release name", async () => {
    const client = makeClient()
    renderWithK8sProvider(<EventsTab ad={makeAd()} instance={instance} />, { client })

    // releasePrefix falls back to "<singular>-" so the Helm release is
    // "vm-instance-demo-vm" — the label child Pods/PVCs actually carry.
    await waitFor(() =>
      expect(client.list).toHaveBeenCalledWith(
        "",
        "v1",
        "pods",
        "tenant-root",
        expect.objectContaining({
          labelSelector: "app.kubernetes.io/instance=vm-instance-demo-vm",
        }),
      ),
    )
    // The old empty-string fallback queried the bare app name, matching nothing.
    expect(client.list).not.toHaveBeenCalledWith(
      "",
      "v1",
      "pods",
      "tenant-root",
      expect.objectContaining({ labelSelector: "app.kubernetes.io/instance=demo-vm" }),
    )
  })

  it("honors an explicit release.prefix", async () => {
    const client = makeClient()
    renderWithK8sProvider(
      <EventsTab ad={makeAd({ prefix: "custom-" })} instance={instance} />,
      { client },
    )

    await waitFor(() =>
      expect(client.list).toHaveBeenCalledWith(
        "",
        "v1",
        "persistentvolumeclaims",
        "tenant-root",
        expect.objectContaining({
          labelSelector: "app.kubernetes.io/instance=custom-demo-vm",
        }),
      ),
    )
  })
})
