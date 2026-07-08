import { describe, it, expect, vi, afterEach } from "vitest"
import { screen, waitFor, cleanup } from "@testing-library/react"
import { K8sClient } from "@cozystack/k8s-client"
import { renderWithK8sProvider } from "../../test-utils/render.tsx"
import { ServicesTab } from "./ServicesTab.tsx"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"

const ad: ApplicationDefinition = {
  apiVersion: "cozystack.io/v1alpha1",
  kind: "ApplicationDefinition",
  metadata: { name: "http-cache" },
  spec: {
    application: {
      kind: "HTTPCache",
      plural: "httpcaches",
      singular: "http-cache",
      openAPISchema: "{}",
    },
  },
}

const instance: ApplicationInstance = {
  apiVersion: "apps.cozystack.io/v1alpha1",
  kind: "HTTPCache",
  metadata: { name: "demo", namespace: "tenant-root" },
}

function makeClient(items: unknown[]) {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockResolvedValue({
    apiVersion: "v1",
    kind: "List",
    metadata: {},
    items,
  })
  vi.spyOn(client, "watch").mockReturnValue(() => {})
  return client
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe("ServicesTab external IP", () => {
  it("shows the LoadBalancer external IP from status, not just the cluster IP", async () => {
    const client = makeClient([
      {
        metadata: { name: "demo", namespace: "tenant-root" },
        spec: { type: "LoadBalancer", clusterIP: "10.96.1.2", ports: [{ port: 80 }] },
        status: { loadBalancer: { ingress: [{ ip: "192.0.2.50" }] } },
      },
    ])
    renderWithK8sProvider(<ServicesTab ad={ad} instance={instance} />, { client })

    // Regression for #3111: the assigned MetalLB IP must be surfaced, not hidden.
    await waitFor(() => expect(screen.getByText("192.0.2.50")).toBeInTheDocument())
    expect(screen.getByText("10.96.1.2")).toBeInTheDocument()
  })

  it("falls back to the hostname, then to Pending, for a LoadBalancer", async () => {
    const client = makeClient([
      {
        metadata: { name: "by-host", namespace: "tenant-root" },
        spec: { type: "LoadBalancer", clusterIP: "10.96.1.3", ports: [{ port: 443 }] },
        status: { loadBalancer: { ingress: [{ hostname: "lb.example.com" }] } },
      },
      {
        metadata: { name: "pending", namespace: "tenant-root" },
        spec: { type: "LoadBalancer", clusterIP: "10.96.1.4", ports: [{ port: 443 }] },
        status: {},
      },
    ])
    renderWithK8sProvider(<ServicesTab ad={ad} instance={instance} />, { client })

    await waitFor(() => expect(screen.getByText("lb.example.com")).toBeInTheDocument())
    expect(screen.getByText("Pending")).toBeInTheDocument()
  })

  it("does not show an external IP for a ClusterIP service", async () => {
    const client = makeClient([
      {
        metadata: { name: "internal", namespace: "tenant-root" },
        spec: { type: "ClusterIP", clusterIP: "10.96.1.5", ports: [{ port: 8080 }] },
      },
    ])
    renderWithK8sProvider(<ServicesTab ad={ad} instance={instance} />, { client })

    await waitFor(() => expect(screen.getByText("internal")).toBeInTheDocument())
    expect(screen.queryByText("Pending")).not.toBeInTheDocument()
    // The External IP cell renders an em dash for non-LoadBalancer services.
    expect(screen.getAllByText("—").length).toBeGreaterThan(0)
  })
})
