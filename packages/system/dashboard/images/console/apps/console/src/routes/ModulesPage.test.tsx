import { describe, it, expect, vi, beforeAll } from "vitest"
import { screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import {
  K8sClient,
  type K8sList,
  type APIGroupList,
} from "@cozystack/k8s-client"
import { ModulesPage } from "./ModulesPage.tsx"
import { TenantProvider } from "../lib/tenant-context.tsx"
import { SELECTED_TENANT_KEY } from "../lib/constants.ts"
import { renderWithK8sProvider } from "../test-utils/render.tsx"

function moduleAd(kind: string, plural: string) {
  return {
    apiVersion: "cozystack.io/v1alpha1",
    kind: "ApplicationDefinition",
    metadata: { name: kind.toLowerCase() },
    spec: {
      application: { kind, plural, singular: kind.toLowerCase() },
      dashboard: { module: true, category: "Administration", description: kind },
    },
  }
}

// root runs monitoring + its own info; kvaps runs its own info and inherits
// monitoring from root; nobody provides etcd.
const ROOT_TN = {
  apiVersion: "core.cozystack.io/v1alpha1",
  kind: "TenantNamespace",
  metadata: {
    name: "tenant-root",
    labels: {
      "tenant.cozystack.io/tenant-root": "",
      "namespace.cozystack.io/monitoring": "tenant-root",
      "namespace.cozystack.io/info": "tenant-root",
    },
  },
}

const KVAPS_TN = {
  apiVersion: "core.cozystack.io/v1alpha1",
  kind: "TenantNamespace",
  metadata: {
    name: "tenant-kvaps",
    labels: {
      "tenant.cozystack.io/tenant-root": "",
      "tenant.cozystack.io/tenant-kvaps": "",
      "namespace.cozystack.io/monitoring": "tenant-root",
      "namespace.cozystack.io/info": "tenant-kvaps",
    },
  },
}

const TENANT_MODULES = [
  {
    apiVersion: "core.cozystack.io/v1alpha1",
    kind: "TenantModule",
    metadata: {
      name: "monitoring",
      namespace: "tenant-root",
      labels: { "apps.cozystack.io/application.kind": "Monitoring" },
    },
  },
]

function makeClient(
  extraModules: unknown[] = [],
  tns: unknown[] = [ROOT_TN, KVAPS_TN],
): K8sClient {
  const client = new K8sClient()
  vi.spyOn(client, "list").mockImplementation(async (_g, _v, plural) => {
    const items =
      plural === "applicationdefinitions"
        ? [moduleAd("Info", "infos"), moduleAd("Monitoring", "monitorings"), moduleAd("Etcd", "etcds")]
        : plural === "tenantnamespaces"
          ? tns
          : plural === "tenantmodules"
            ? [...TENANT_MODULES, ...extraModules]
            : []
    return {
      apiVersion: "v1",
      kind: `${plural}List`,
      metadata: {},
      items,
    } as K8sList<unknown>
  })
  vi.spyOn(client, "getApiGroups").mockResolvedValue({
    kind: "APIGroupList",
    apiVersion: "v1",
    groups: [],
  } as APIGroupList)
  return client
}

beforeAll(() => {
  const store = new Map<string, string>([[SELECTED_TENANT_KEY, "kvaps"]])
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  })
})

describe("ModulesPage tenant tree with module chips", () => {
  it("shows local and inherited chips per tenant by provider namespace", async () => {
    renderWithK8sProvider(
      <TenantProvider>
        <ModulesPage />
      </TenantProvider>,
      { client: makeClient(), initialRoute: "/admin/modules" },
    )

    // root shows its local monitoring chip.
    expect(await screen.findByText("root")).toBeInTheDocument()
    expect(screen.getByTitle("Monitoring runs in root")).toBeInTheDocument()

    // kvaps ends up with no chips at all (info filtered, monitoring covered
    // by root's own chip) — the whole tenant is hidden, and root shows the
    // hidden-children counter instead.
    expect(screen.queryByText("kvaps")).not.toBeInTheDocument()
    expect(screen.getByText("+ 1 tenant")).toBeInTheDocument()

    // Info is installed everywhere by default — filtered out entirely.
    expect(screen.queryByText("Info")).not.toBeInTheDocument()

    // kvaps inherits monitoring from root, but root's row already shows the
    // module as its own green chip — no redundant inherited chip.
    expect(screen.queryByTitle("Inherited from tenant root, which you cannot open")).not.toBeInTheDocument()

    // Nobody provides etcd → no Etcd chip anywhere.
    expect(screen.queryByText("Etcd")).not.toBeInTheDocument()
  })

  it("collapses a tenant's chips and subtenants via the name toggle", async () => {
    const user = userEvent.setup()
    renderWithK8sProvider(
      <TenantProvider>
        <ModulesPage />
      </TenantProvider>,
      { client: makeClient(), initialRoute: "/admin/modules" },
    )

    expect(await screen.findByTitle("Monitoring runs in root")).toBeInTheDocument()
    // root is the only expandable node in this fixture.
    await user.click(screen.getByTitle("Collapse"))
    expect(screen.queryByTitle("Monitoring runs in root")).not.toBeInTheDocument()
    expect(screen.queryByText("+ 1 tenant")).not.toBeInTheDocument()
    await user.click(screen.getByTitle("Expand"))
    expect(await screen.findByTitle("Monitoring runs in root")).toBeInTheDocument()
  })

  it("shows the inherited chip when the providing tenant is not accessible", async () => {
    // Only kvaps is visible; monitoring comes from tenant-root, which the
    // user cannot see — the inherited chip is the only trace of the module.
    renderWithK8sProvider(
      <TenantProvider>
        <ModulesPage />
      </TenantProvider>,
      { client: makeClient([], [KVAPS_TN]), initialRoute: "/admin/modules" },
    )

    const chip = await screen.findByTitle("Inherited from tenant root, which you cannot open")
    expect(chip).toBeInTheDocument()
    // The chip is deliberately inert: it is rendered only when the providing
    // tenant is NOT visible, so opening it would deny the instance GET and
    // switch the active tenant to a namespace outside the visible set, which
    // the selector then bounces to an arbitrary fallback. It states where the
    // module comes from and nothing more.
    expect(chip.tagName).toBe("DIV")
    expect(chip).not.toHaveAttribute("type", "button")
  })

  it("shows a chip from the TenantModule watch even before provider labels land", async () => {
    // A freshly toggled module can surface as a TenantModule before the
    // TenantNamespace labels update — either live signal must produce a chip.
    renderWithK8sProvider(
      <TenantProvider>
        <ModulesPage />
      </TenantProvider>,
      {
        client: makeClient([
          {
            apiVersion: "core.cozystack.io/v1alpha1",
            kind: "TenantModule",
            metadata: {
              name: "etcd",
              namespace: "tenant-kvaps",
              labels: { "apps.cozystack.io/application.kind": "Etcd" },
            },
          },
        ]),
        initialRoute: "/admin/modules",
      },
    )

    expect(await screen.findByTitle("Etcd runs in kvaps")).toBeInTheDocument()
  })
})
