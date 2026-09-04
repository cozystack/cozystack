import { useMemo } from "react"
import { Link } from "react-router"
import { useApplicationDefinitions } from "../lib/app-definitions.ts"
import { useTenantContext } from "../lib/tenant-context.tsx"
import { TENANT_NAMESPACE_PREFIX } from "../lib/constants.ts"
import { navigatesThisTab } from "../lib/links.ts"

interface WorkloadCellProps {
  /** Namespace the workload lives in (tenant-<name> for tenant workloads). */
  namespace: string
  /** Application kind (apps.cozystack.io/application.kind), or "—" when unknown. */
  kind: string
  /** Application instance name. */
  name: string
}

/**
 * Renders a consuming workload (the owning application) as a deep-link to its
 * Console Workloads tab, with the kind shown as a subtitle. The link is only active for
 * real app instances: the kind must resolve to a plural via ApplicationDefinitions
 * and the workload must live in a tenant namespace, since the link has to name
 * that tenant for the Console to resolve the instance. Shared by every
 * per-resource drill-down.
 */
export function WorkloadCell({ namespace, kind, name }: WorkloadCellProps) {
  const { data: appDefs } = useApplicationDefinitions()
  const { selectTenant } = useTenantContext()

  const plural = useMemo(() => {
    for (const ad of appDefs?.items ?? []) {
      if (ad.spec?.application.kind === kind) return ad.spec?.application.plural
    }
    return undefined
  }, [appDefs, kind])

  const tenant = namespace.startsWith(TENANT_NAMESPACE_PREFIX)
    ? namespace.slice(TENANT_NAMESPACE_PREFIX.length)
    : null
  // These drill-downs list workloads from every namespace, so the tenant has to
  // travel in the URL: onClick alone leaves middle click and open-in-new-tab
  // resolving the name against the previously selected tenant.
  const href =
    plural && tenant
      ? `/console/${plural}/${name}/workloads?tenant=${encodeURIComponent(tenant)}`
      : null

  return (
    <>
      {href ? (
        <Link
          to={href}
          onClick={(e) => {
            if (!navigatesThisTab(e) || !tenant) return
            selectTenant(tenant)
          }}
          className="font-medium text-blue-700 hover:text-blue-800 hover:underline"
        >
          {name}
        </Link>
      ) : (
        <span className="font-medium text-slate-700">{name}</span>
      )}
      {kind !== "—" ? <div className="text-xs text-slate-400">{kind}</div> : null}
    </>
  )
}
