import { describe, it, expect, vi } from "vitest"
import { screen } from "@testing-library/react"
import {
  K8sClient,
  type K8sList,
  type APIGroupList,
  type SelfSubjectAccessReview,
} from "@cozystack/k8s-client"
import { AdminPage } from "./AdminPage.tsx"
import { renderWithK8sProvider } from "../test-utils/render.tsx"

/**
 * Answer each SelfSubjectAccessReview by its requested resource so the two
 * admin gates (nodes/list for Cluster Usage, backupclasses/update for Backup
 * Classes) can be exercised independently.
 */
function makeClient(allow: Record<string, boolean>): K8sClient {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockImplementation(async (_g, _v, plural) => {
    return {
      apiVersion: "v1",
      kind: `${plural}List`,
      metadata: {},
      items: [],
    } as K8sList<unknown>
  })
  vi.spyOn(client, "getApiGroups").mockResolvedValue({
    kind: "APIGroupList",
    apiVersion: "v1",
    groups: [],
  } as APIGroupList)
  vi.spyOn(client, "create").mockImplementation(async (_g, _v, _p, body) => {
    const resource =
      (body as SelfSubjectAccessReview).spec?.resourceAttributes?.resource ?? ""
    return {
      ...(body as object),
      status: { allowed: allow[resource] === true },
    } as unknown
  })
  return client
}

describe("AdminPage routing & access gate", () => {
  it("renders the Cluster Usage page at /cluster-usage for an operator", async () => {
    renderWithK8sProvider(<AdminPage />, {
      client: makeClient({ nodes: true }),
      initialRoute: "/capacity/cluster",
    })
    expect(await screen.findByText("Cluster")).toBeInTheDocument()
  })

  it("redirects the index route to Cluster Usage for an operator", async () => {
    renderWithK8sProvider(<AdminPage />, {
      client: makeClient({ nodes: true }),
      initialRoute: "/",
    })
    expect(await screen.findByText("Cluster")).toBeInTheDocument()
  })

  it("blocks direct access with a 403 notice when the user has neither admin area", async () => {
    renderWithK8sProvider(<AdminPage />, {
      client: makeClient({ nodes: false, backupclasses: false }),
      initialRoute: "/capacity/cluster",
    })
    expect(
      await screen.findByText(/you do not have permission to access the admin portal/i),
    ).toBeInTheDocument()
  })

  it("guards capacity routes for a backup-only operator hitting a capacity URL", async () => {
    // Passes the portal-level gate via backupclasses/update, but the capacity
    // area must still be closed without nodes/list.
    renderWithK8sProvider(<AdminPage />, {
      client: makeClient({ nodes: false, backupclasses: true }),
      initialRoute: "/capacity/cluster",
    })
    expect(
      await screen.findByText(/you do not have permission to view cluster capacity/i),
    ).toBeInTheDocument()
    expect(screen.queryByText("Cluster")).not.toBeInTheDocument()
  })

  it("guards backup-class routes for a capacity-only operator hitting a backups URL", async () => {
    renderWithK8sProvider(<AdminPage />, {
      client: makeClient({ nodes: true, backupclasses: false }),
      initialRoute: "/backups/backupclasses",
    })
    expect(
      await screen.findByText(/you do not have permission to manage backup classes/i),
    ).toBeInTheDocument()
  })
})
