import { afterEach, describe, expect, it, vi } from "vitest"
import { fireEvent, screen, waitFor, within } from "@testing-library/react"
import { K8sApiError, K8sClient } from "@cozystack/k8s-client"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import { renderWithK8sProvider } from "../../test-utils/render.tsx"
import { CredentialsTab } from "./CredentialsTab.tsx"
import { useApplicationCredentials, type Credential } from "./use-app-credentials.ts"

const namespace = "tenant-test"
const secret = "s3cr3t-Pa55w0rd-only-once"
const ad: ApplicationDefinition = {
  apiVersion: "cozystack.io/v1alpha1", kind: "ApplicationDefinition",
  metadata: { name: "postgres" },
  spec: { application: { kind: "Postgres", plural: "postgreses", singular: "postgres", openAPISchema: "{}" } },
}
const instance: ApplicationInstance = {
  apiVersion: "apps.cozystack.io/v1alpha1", kind: "Postgres",
  metadata: { name: "demo", namespace },
}

function credential(user: string, password: NonNullable<Credential["status"]>["password"], extra: Partial<Credential> = {}): Credential {
  return {
    apiVersion: "core.cozystack.io/v1alpha1", kind: "Credential",
    metadata: { name: `demo.${user}`, namespace, resourceVersion: `rv-${user}` },
    spec: { application: { apiGroup: "apps.cozystack.io", kind: "Postgres", name: "demo" }, user },
    status: { password, history: [{ operation: "Create", at: "2026-11-20T10:00:00Z", by: "system:serviceaccount:cozy-system:cozystack-api" }] },
    ...extra,
  }
}

const items: Credential[] = [
  credential("alice", { origin: "Minted", at: "2026-11-20T12:00:00Z", by: "jane@example.org" }),
  credential("bob", { origin: "Migrated" }),
  credential("carol", { origin: "NotIssued" }),
  credential("dave", { origin: "Revoked", at: "2026-11-20T13:00:00Z", by: "jane@example.org" }),
  credential("eve", { origin: "Migrated" }, {
    metadata: { name: "other.eve", namespace },
    spec: { application: { apiGroup: "apps.cozystack.io", kind: "Postgres", name: "other" }, user: "eve" },
  }),
]

function Credentials() {
  const result = useApplicationCredentials(ad, instance, namespace)
  return <CredentialsTab namespace={namespace} credentials={result.credentials} isLoading={result.isLoading}
    error={result.error} onChanged={() => { void result.refetch() }} />
}

function makeClient({ allowed = true, list = items }: { allowed?: boolean; list?: Credential[] } = {}) {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockImplementation(async (group, _version, plural, ns) => {
    expect([group, plural, ns]).toEqual(["core.cozystack.io", "credentials", namespace])
    return { apiVersion: "core.cozystack.io/v1alpha1", kind: "CredentialList", metadata: { resourceVersion: "1" }, items: list }
  })
  vi.spyOn(client, "watch").mockReturnValue(() => {})
  vi.spyOn(client, "create").mockImplementation(async (_group, _version, _plural, body) => ({
    ...(body as object), status: { allowed },
  }))
  return client
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe("application credentials", () => {
  it("lists only this application's accounts with what each origin means", async () => {
    renderWithK8sProvider(<Credentials />, { client: makeClient() })
    expect(await screen.findByText("alice")).toBeInTheDocument()
    expect(screen.getByText(/Shown once to jane@example\.org at/)).toBeInTheDocument()
    expect(screen.getByText(/password from before the conversion still works/)).toBeInTheDocument()
    expect(screen.getByText(/Nobody holds a password yet/)).toBeInTheDocument()
    expect(screen.getByText(/The password was revoked/)).toBeInTheDocument()
    expect(screen.queryByText("eve")).not.toBeInTheDocument()
  })

  it("expands an account's history", async () => {
    renderWithK8sProvider(<Credentials />, { client: makeClient({ list: [items[0]] }) })
    fireEvent.click(await screen.findByRole("button", { name: /History \(1\)/ }))
    expect(screen.getByText("Created")).toBeInTheDocument()
    expect(screen.getByText(/by the platform at/)).toBeInTheDocument()
  })

  it("hides the actions when the user may not mint or revoke", async () => {
    const client = makeClient({ allowed: false })
    renderWithK8sProvider(<Credentials />, { client })
    expect(await screen.findByText("alice")).toBeInTheDocument()
    await waitFor(() => expect(client.create).toHaveBeenCalledTimes(2))
    expect(client.create).toHaveBeenCalledWith("authorization.k8s.io", "v1", "selfsubjectaccessreviews", expect.objectContaining({
      spec: { resourceAttributes: { namespace, group: "core.cozystack.io", resource: "credentials", subresource: "mint", verb: "create" } },
    }))
    expect(screen.queryByRole("button", { name: /Generate password/ })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Revoke/ })).not.toBeInTheDocument()
  })

  it("shows a minted password once, with the precondition sent, and drops it on close", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal("navigator", { clipboard: { writeText } })
    const client = makeClient()
    const subresource = vi.spyOn(client, "subresource").mockResolvedValue({
      status: { username: "bob", password: secret, issuedAt: "2026-11-21T09:00:00Z" },
    })
    const { queryClient } = renderWithK8sProvider(<Credentials />, { client })
    fireEvent.click(await screen.findByRole("button", { name: "Generate password for bob" }))
    const dialog = await screen.findByRole("dialog")
    expect(dialog).toHaveTextContent("stops working for every client")
    expect(subresource).not.toHaveBeenCalled()

    fireEvent.click(within(dialog).getByRole("button", { name: "Generate password" }))
    expect(await screen.findByTestId("minted-password")).toHaveTextContent(secret)
    expect(screen.getByText(/cannot be shown again/)).toBeInTheDocument()
    expect(subresource).toHaveBeenCalledWith("core.cozystack.io", "v1alpha1", "credentials", "demo.bob", "mint", namespace, {
      apiVersion: "core.cozystack.io/v1alpha1",
      kind: "CredentialRequest",
      spec: { preconditions: { resourceVersion: "rv-bob" } },
    }, "POST")
    await waitFor(() => expect(client.list).toHaveBeenCalledTimes(2))

    fireEvent.click(screen.getByRole("button", { name: "Copy password" }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(secret))

    fireEvent.click(screen.getByRole("button", { name: "I have saved it, close" }))
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
    expect(document.body).not.toHaveTextContent(secret)
    const cached = JSON.stringify([
      queryClient.getQueryCache().getAll().map((q) => q.state.data),
      queryClient.getMutationCache().getAll().map((m) => m.state.data),
    ])
    expect(cached).not.toContain(secret)
  })

  it("shows the server's conflict, refreshes the list and offers no blind retry", async () => {
    const client = makeClient()
    vi.spyOn(client, "subresource").mockRejectedValue(
      new K8sApiError(409, { message: "credential demo.bob changed: password minted by john@example.org" }),
    )
    renderWithK8sProvider(<Credentials />, { client })
    fireEvent.click(await screen.findByRole("button", { name: "Generate password for bob" }))
    const dialog = await screen.findByRole("dialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Generate password" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("changed: password minted by john@example.org")
    await waitFor(() => expect(client.list).toHaveBeenCalledTimes(2))
    expect(within(dialog).queryByRole("button", { name: "Generate password" })).not.toBeInTheDocument()
    expect(screen.queryByTestId("minted-password")).not.toBeInTheDocument()
  })

  it("reports a denied action instead of failing silently", async () => {
    const client = makeClient()
    vi.spyOn(client, "subresource").mockRejectedValue(new K8sApiError(403, { message: "forbidden" }))
    renderWithK8sProvider(<Credentials />, { client })
    fireEvent.click(await screen.findByRole("button", { name: "Revoke password for bob" }))
    const dialog = await screen.findByRole("dialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Revoke" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("not allowed to revoke")
  })

  it("revokes without showing any secret", async () => {
    const client = makeClient()
    const subresource = vi.spyOn(client, "subresource").mockResolvedValue({
      status: { username: "alice", issuedAt: "2026-11-21T09:00:00Z" },
    })
    renderWithK8sProvider(<Credentials />, { client })
    fireEvent.click(await screen.findByRole("button", { name: "Revoke password for alice" }))
    const dialog = await screen.findByRole("dialog")
    expect(dialog).toHaveTextContent("stops working for every client")
    fireEvent.click(within(dialog).getByRole("button", { name: "Revoke" }))
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
    expect(subresource).toHaveBeenCalledWith("core.cozystack.io", "v1alpha1", "credentials", "demo.alice", "revoke", namespace,
      expect.objectContaining({ spec: { preconditions: { resourceVersion: "rv-alice" } } }), "POST")
    expect(screen.queryByTestId("minted-password")).not.toBeInTheDocument()
  })

  it("offers no revoke where nobody holds a password", async () => {
    renderWithK8sProvider(<Credentials />, { client: makeClient() })
    expect(await screen.findByRole("button", { name: "Generate password for carol" })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Revoke password for carol" })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Revoke password for dave" })).not.toBeInTheDocument()
  })

  it("treats a cluster without the Credential API as having no accounts", async () => {
    const client = makeClient()
    vi.mocked(client.list).mockRejectedValue(new K8sApiError(404, "not found"))
    renderWithK8sProvider(<Credentials />, { client })
    expect(await screen.findByText("No accounts yet.")).toBeInTheDocument()
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  })
})
