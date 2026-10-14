import { afterEach, describe, expect, it, vi } from "vitest"
import { act, fireEvent, screen, waitFor } from "@testing-library/react"
import { K8sApiError, K8sClient } from "@cozystack/k8s-client"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import { renderWithK8sProvider } from "../../test-utils/render.tsx"
import { ConfigMapsTab } from "./ConfigMapsTab.tsx"
import { configMapNames, useApplicationConfigMaps } from "./use-app-configmaps.ts"

const namespace = "tenant-test"
const clusterFile = "demo:example@192.0.2.1:4500"
const ad: ApplicationDefinition = {
  apiVersion: "cozystack.io/v1alpha1", kind: "ApplicationDefinition",
  metadata: { name: "foundationdb" },
  spec: { application: { kind: "FoundationDB", plural: "foundationdbs", singular: "foundationdb", openAPISchema: "{}" } },
}
const instance: ApplicationInstance = {
  apiVersion: "apps.cozystack.io/v1alpha1", kind: "FoundationDB",
  metadata: { name: "demo", namespace },
}
const resources = "- apiVersion: v1\n  kind: ConfigMap\n  name: foundationdb-demo-config"

function Configuration() {
  const maps = useApplicationConfigMaps(ad, instance, namespace)
  return <ConfigMapsTab namespace={namespace} names={maps.names} error={maps.error} />
}

function makeClient(error?: Error, map = resources) {
  const client = new K8sClient()
  vi.spyOn(client, "watch").mockReturnValue(() => {})
  vi.spyOn(client, "list").mockImplementation(async (_group, _version, _plural, ns, options) => {
    expect(ns).toBe(namespace)
    const name = options?.fieldSelector?.replace("metadata.name=", "")
    if (name === "foundationdb-demo-resourcemap") {
      return { apiVersion: "v1", kind: "ConfigMapList", metadata: { resourceVersion: "1" }, items: [
        { metadata: { name, namespace }, data: { resources: map } },
      ] }
    }
    expect(name).toBe("foundationdb-demo-config")
    if (error) throw error
    return { apiVersion: "v1", kind: "ConfigMapList", metadata: { resourceVersion: "1" }, items: [
      { metadata: { name: "foundationdb-demo-config", namespace }, data: { "cluster-file": clusterFile } },
    ] }
  })
  return client
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe("application ConfigMaps", () => {
  it("discovers and reads only the named ConfigMaps in the active namespace", async () => {
    const client = makeClient()
    renderWithK8sProvider(<Configuration />, { client })
    expect(await screen.findByText(clusterFile)).toBeInTheDocument()
    expect(client.list).toHaveBeenCalledWith("", "v1", "configmaps", namespace, {
      labelSelector: undefined, fieldSelector: "metadata.name=foundationdb-demo-resourcemap",
    })
    expect(client.list).toHaveBeenCalledWith("", "v1", "configmaps", namespace, {
      labelSelector: undefined, fieldSelector: "metadata.name=foundationdb-demo-config",
    })
    expect(client.watch).toHaveBeenCalledWith("", "v1", "configmaps", namespace, "1",
      expect.any(Function), expect.any(Function), {
        labelSelector: undefined, fieldSelector: "metadata.name=foundationdb-demo-config",
      })
  })

  it("copies the cluster file without base64 decoding", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal("navigator", { clipboard: { writeText } })
    renderWithK8sProvider(<Configuration />, { client: makeClient() })
    fireEvent.click(await screen.findByRole("button", { name: "Copy cluster-file" }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(clusterFile))
    expect(await screen.findByText("Copied.")).toBeInTheDocument()
  })

  it("reports a clipboard failure", async () => {
    vi.stubGlobal("navigator", { clipboard: { writeText: vi.fn().mockRejectedValue(new Error("Denied")) } })
    renderWithK8sProvider(<Configuration />, { client: makeClient() })
    fireEvent.click(await screen.findByRole("button", { name: "Copy cluster-file" }))
    expect(await screen.findByRole("status")).toHaveTextContent("Could not copy")
  })

  it("shows a read denial instead of reporting an empty ConfigMap", async () => {
    renderWithK8sProvider(<Configuration />, { client: makeClient(new K8sApiError(403, "ConfigMap access denied")) })
    expect(await screen.findByRole("alert")).toHaveTextContent("ConfigMap access denied")
    expect(screen.queryByText("No configuration values.")).not.toBeInTheDocument()
  })

  it.each([403, 404])("drops the ConfigMaps when the resource map answers %i after a successful read", async (status) => {
    const client = makeClient()
    const { queryClient } = renderWithK8sProvider(<Configuration />, { client })
    expect(await screen.findByText(clusterFile)).toBeInTheDocument()
    vi.mocked(client.list).mockRejectedValue(new K8sApiError(status, "resource map unavailable"))
    await act(() => queryClient.invalidateQueries())
    expect(await screen.findByText("No ConfigMaps.")).toBeInTheDocument()
    expect(screen.queryByText(clusterFile)).not.toBeInTheDocument()
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  })

  it("drops a malformed-map error when the resource map turns forbidden", async () => {
    const client = makeClient(undefined, "not a list")
    const { queryClient } = renderWithK8sProvider(<Configuration />, { client })
    expect(await screen.findByRole("alert")).toHaveTextContent("must contain a list")
    vi.mocked(client.list).mockRejectedValue(new K8sApiError(403, "resource map unavailable"))
    await act(() => queryClient.invalidateQueries())
    expect(await screen.findByText("No ConfigMaps.")).toBeInTheDocument()
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  })

  it("ignores non-ConfigMap and cross-namespace entries and removes duplicate names", () => {
    expect(configMapNames(JSON.stringify([
      { apiVersion: "v1", kind: "ConfigMap", name: "connection" },
      { apiVersion: "v1", kind: "ConfigMap", name: "connection", namespace },
      { apiVersion: "v1", kind: "ConfigMap", name: "foreign", namespace: "tenant-other" },
      { apiVersion: "v1", kind: "Secret", name: "password" },
      { apiVersion: "other/v1", kind: "ConfigMap", name: "unrelated" },
      null,
    ]), namespace)).toEqual(["connection"])
  })

  it("rejects malformed maps and unsafe resource names", () => {
    expect(() => configMapNames("{broken", namespace)).toThrow()
    expect(() => configMapNames("not a list", namespace)).toThrow("must contain a list")
    expect(() => configMapNames(resources.replace("foundationdb-demo-config", "../secrets"), namespace)).toThrow("invalid ConfigMap name")
  })
})
