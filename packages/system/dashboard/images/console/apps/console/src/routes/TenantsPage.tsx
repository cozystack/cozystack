import { useMemo } from "react"
import { Link } from "react-router"
import { Plus, Edit } from "lucide-react"
import { Spinner, Section, Button } from "@cozystack/ui"
import { useK8sList } from "@cozystack/k8s-client"
import type { Tenant } from "@cozystack/types"
import { useTenantContext } from "../lib/tenant-context.tsx"
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
  status?: {
    ready?: boolean
  }
}

function tenantHost(tenant: Tenant): string | undefined {
  return tenant.spec?.host
}

export function TenantsPage() {
  const { tenantNamespace } = useTenantContext()

  // Get Tenant ApplicationInstances from current tenant namespace
  const { data: tenantsData, isLoading } = useK8sList<Tenant>(
    {
      apiGroup: "apps.cozystack.io",
      apiVersion: "v1alpha1",
      plural: "tenants",
      namespace: tenantNamespace ?? undefined,
    },
    { enabled: !!tenantNamespace }
  )

  const tenants = tenantsData?.items ?? []

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

  return (
    <div className="p-6">
      <div className="mb-5 flex items-end justify-between">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">Tenants</h1>
          <p className="mt-0.5 text-sm text-slate-500">
            Top-level owners of their own namespaces and applications.
          </p>
        </div>
        <Link to="/marketplace/tenant">
          <Button variant="primary" size="sm">
            <Plus className="size-3.5" /> Create tenant
          </Button>
        </Link>
      </div>
      {isLoading ? (
        <div className="flex items-center gap-2 text-sm text-slate-500">
          <Spinner /> Loading…
        </div>
      ) : tenants.length === 0 ? (
        <Section>
          <p className="py-6 text-center text-sm text-slate-500">No tenants yet.</p>
        </Section>
      ) : (
        <Section bodyClassName="p-0">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-slate-200 bg-slate-50 text-left text-xs font-medium uppercase tracking-wider text-slate-500">
                <th className="px-4 py-3">Name</th>
                <th className="px-4 py-3">Namespace</th>
                <th className="px-4 py-3">Host</th>
                <th className="px-4 py-3">Modules</th>
                <th className="px-4 py-3">Quotas</th>
                <th className="px-4 py-3">Age</th>
                <th className="px-4 py-3"></th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {tenants.map((t) => {
                const name = t.metadata.name
                const tenantNs = t.status?.namespace ?? ""
                const modules = modulesByNamespace.get(tenantNs) ?? []
                const tenantQuotas = quotasByNamespace.get(tenantNs) ?? []
                const host = tenantHost(t)
                return (
                  <tr key={name} className="hover:bg-slate-50">
                    <td className="px-4 py-3 text-sm font-medium text-slate-900">
                      {name}
                    </td>
                    <td className="px-4 py-3 font-mono text-xs text-slate-600">
                      {tenantNs || "—"}
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
                      {formatAge(t.metadata.creationTimestamp)}
                    </td>
                    <td className="px-4 py-3">
                      <Link to={`/console/tenants/${name}/edit`}>
                        <Button variant="outline" size="sm">
                          <Edit className="size-3" /> Edit
                        </Button>
                      </Link>
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
