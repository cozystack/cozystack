import { useMemo, useState } from "react"
import { useNavigate } from "react-router"
import { Plus, Edit, Info, ChevronDown, ChevronRight, CornerDownRight } from "lucide-react"
import { Spinner, Section, Button } from "@cozystack/ui"
import { useK8sList } from "@cozystack/k8s-client"
import { useResourceBasePath } from "../lib/portal.ts"
import { useTenantContext, tenantDisplayName } from "../lib/tenant-context.tsx"
import {
  ancestorNamespaces,
  buildTenantForest,
  realParentNamespace,
  flattenTenantForest,
  relativeTenantName,
  type TenantTreeNode,
} from "../lib/tenant-tree.ts"
import { TENANT_NAMESPACE_PREFIX } from "../lib/constants.ts"
import { formatAge } from "../lib/status.ts"
import { TenantQuotaCompact } from "../components/QuotaDisplay.tsx"
import type { ResourceQuota } from "../components/QuotaDisplay.tsx"

interface TenantModule {
  apiVersion: string
  kind: string
  metadata: {
    name: string
    namespace: string
  }
}

const HOST_LABEL = "namespace.cozystack.io/host"

type TreeNode = TenantTreeNode

/**
 * The Tenant CR of a namespace lives in its parent's namespace under the
 * short name: `tenant-whmcs-jzmnbwum` under `tenant-whmcs` is CR `jzmnbwum`.
 * Root-level children (`tenant-kvaps` under `tenant-root`) collapse to the
 * same rule via the plain `tenant-` prefix strip.
 */
function tenantCrName(ns: string, parentNs: string): string {
  return ns.startsWith(`${parentNs}-`)
    ? ns.slice(parentNs.length + 1)
    : ns.slice(TENANT_NAMESPACE_PREFIX.length)
}

export function TenantsPage() {
  const { tenants, selectTenant, isLoading } = useTenantContext()
  const basePath = useResourceBasePath()
  const navigate = useNavigate()
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())

  const toggleCollapse = (ns: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (next.has(ns)) next.delete(ns)
      else next.add(ns)
      return next
    })
  }

  // Get TenantModules from all namespaces to show modules for each tenant
  const { data: modulesData } = useK8sList<TenantModule>({
    apiGroup: "core.cozystack.io",
    apiVersion: "v1alpha1",
    plural: "tenantmodules",
  })

  // Cluster-wide ResourceQuota list — one watch instead of N per row
  const { data: quotasData } = useK8sList<ResourceQuota>({
    apiGroup: "",
    apiVersion: "v1",
    plural: "resourcequotas",
  })

  // Group non-info modules by namespace (info is always the default, never shown)
  const modulesByNamespace = useMemo(() => {
    const map = new Map<string, string[]>()
    for (const mod of modulesData?.items ?? []) {
      if (mod.metadata.name === "info") continue
      const ns = mod.metadata.namespace
      if (!map.has(ns)) map.set(ns, [])
      map.get(ns)!.push(mod.metadata.name)
    }
    return map
  }, [modulesData])

  const quotasByNamespace = useMemo(() => {
    const map = new Map<string, ResourceQuota[]>()
    for (const q of quotasData?.items ?? []) {
      const ns = q.metadata.namespace ?? ""
      if (!map.has(ns)) map.set(ns, [])
      map.get(ns)!.push(q)
    }
    return map
  }, [quotasData])

  // Which namespaces the user can actually read, for deciding whether a node's
  // Tenant CR is reachable at all.
  const visibleNs = useMemo(
    () => new Set(tenants.map((t) => t.metadata.name)),
    [tenants],
  )

  // The context list is the selected tenant's visible subtree (self included).
  const rows = useMemo(
    () => flattenTenantForest(buildTenantForest(tenants), collapsed),
    [tenants, collapsed],
  )

  // Per-tenant Info: switch the active tenant, then let InfoRedirect resolve
  // the Info AD's detail route inside the current portal.
  const openInfo = (node: TreeNode) => {
    selectTenant(tenantDisplayName(node.tn))
    navigate(`${basePath}/info`)
  }

  // Creating a sub-tenant under an arbitrary node reuses the tenant-scoped
  // order page: switch the active tenant to that node first, then order a
  // Tenant there. Same drill pattern as inherited modules.
  const createUnder = (node: TreeNode) => {
    selectTenant(tenantDisplayName(node.tn))
    navigate(`${basePath}/new/tenant`)
  }

  // A node's Tenant CR lives in its REAL parent's namespace, which is not
  // necessarily the parent it hangs under: a node whose real parent is
  // inaccessible is bridged onto the nearest visible ancestor. Deriving the CR
  // from that bridged ancestor names a CR that does not exist, so the edit
  // target comes from realParentNamespace instead — and is offered only when
  // that namespace is actually readable, since otherwise there is no CR the
  // user could open. The hierarchy root (no ancestor labels at all) is
  // self-referential: its CR (`root`) lives in its own namespace.
  const isTrueRoot = (node: TreeNode) =>
    !node.parentNs && ancestorNamespaces(node.tn).length === 0
  const editParentNs = (node: TreeNode) => {
    if (isTrueRoot(node)) return node.tn.metadata.name
    const real = realParentNamespace(node.tn)
    return real && visibleNs.has(real) ? real : undefined
  }
  const canEdit = (node: TreeNode) => !!editParentNs(node)
  const editNode = (node: TreeNode) => {
    const parentNs = editParentNs(node)
    if (!parentNs) return
    selectTenant(parentNs.slice(TENANT_NAMESPACE_PREFIX.length))
    navigate(`${basePath}/tenants/${tenantCrName(node.tn.metadata.name, parentNs)}/edit`)
  }

  return (
    <div className="p-6">
      <div className="mb-5 flex items-end justify-between">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">Tenants</h1>
          <p className="mt-0.5 text-sm text-slate-500">
            The visible tenant hierarchy. Create a sub-tenant at any level with
            its row's <Plus className="inline size-3" /> button.
          </p>
        </div>
      </div>
      {isLoading ? (
        <div className="flex items-center gap-2 text-sm text-slate-500">
          <Spinner /> Loading…
        </div>
      ) : rows.length === 0 ? (
        <Section>
          <p className="py-6 text-center text-sm text-slate-500">No tenants yet.</p>
        </Section>
      ) : (
        <Section bodyClassName="p-0">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-slate-200 bg-slate-50 text-left text-xs font-medium uppercase tracking-wider text-slate-500">
                <th className="px-4 py-3">Tenant</th>
                <th className="px-4 py-3">Host</th>
                <th className="px-4 py-3">Modules</th>
                <th className="px-4 py-3">Quotas</th>
                <th className="px-4 py-3">Age</th>
                <th className="px-4 py-3"></th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {rows.map((node) => {
                const ns = node.tn.metadata.name
                const modules = modulesByNamespace.get(ns) ?? []
                const tenantQuotas = quotasByNamespace.get(ns) ?? []
                const host = node.tn.metadata.labels?.[HOST_LABEL]
                return (
                  <tr key={ns} className="hover:bg-slate-50">
                    <td className="px-4 py-3">
                      <div
                        className="flex items-center gap-1.5"
                        style={{ paddingLeft: `${node.depth * 20}px` }}
                      >
                        {node.children.length > 0 ? (
                          <button
                            type="button"
                            onClick={() => toggleCollapse(ns)}
                            title={collapsed.has(ns) ? "Expand" : "Collapse"}
                            className="shrink-0 rounded p-0.5 text-slate-400 hover:bg-slate-100 hover:text-slate-700"
                          >
                            {collapsed.has(ns) ? (
                              <ChevronRight className="size-3.5" />
                            ) : (
                              <ChevronDown className="size-3.5" />
                            )}
                          </button>
                        ) : node.depth > 0 ? (
                          <CornerDownRight className="size-3.5 shrink-0 text-slate-300" />
                        ) : null}
                        <div className="min-w-0">
                          <p className="truncate text-sm font-medium text-slate-900">
                            {relativeTenantName(node)}
                          </p>
                          <p className="truncate font-mono text-[11px] text-slate-400">
                            {ns}
                          </p>
                        </div>
                      </div>
                    </td>
                    <td className="px-4 py-3 text-xs text-slate-600">{host ?? "—"}</td>
                    <td className="px-4 py-3">
                      {modules.length > 0 ? (
                        <div className="flex flex-wrap gap-1">
                          {modules.map((m) => (
                            <span
                              key={m}
                              className="rounded-full bg-slate-100 px-2 py-0.5 text-[11px] text-slate-600"
                            >
                              {m}
                            </span>
                          ))}
                        </div>
                      ) : (
                        <span className="text-xs text-slate-400">—</span>
                      )}
                    </td>
                    <td className="px-4 py-3 max-w-xs">
                      <TenantQuotaCompact quotas={tenantQuotas} />
                    </td>
                    <td className="px-4 py-3 tabular-nums text-xs text-slate-500">
                      {formatAge(node.tn.metadata.creationTimestamp)}
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex justify-end gap-1.5">
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => openInfo(node)}
                          title={`Open Info of ${tenantDisplayName(node.tn)}`}
                        >
                          <Info className="size-3" /> Info
                        </Button>
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => createUnder(node)}
                          title={`Create a sub-tenant under ${tenantDisplayName(node.tn)}`}
                        >
                          <Plus className="size-3" /> Tenant
                        </Button>
                        {canEdit(node) && (
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={() => editNode(node)}
                            title={`Edit tenant ${tenantDisplayName(node.tn)}`}
                          >
                            <Edit className="size-3" /> Edit
                          </Button>
                        )}
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </Section>
      )}
    </div>
  )
}
