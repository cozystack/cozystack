import { Dropdown } from "@cozystack/ui"
import { tenantDisplayName, useTenantContext } from "../lib/tenant-context.tsx"

/**
 * Subtitle bar that sits under the header and surfaces the active tenant.
 * In cozyportal-ui this is where the account/project selectors live; for
 * Cozystack we only need the tenant picker.
 */
export function Breadcrumb() {
  const { tenants, selectedTenant, selectTenant, isLoading } = useTenantContext()

  if (isLoading && !tenants.length) {
    return <span className="text-slate-400">Loading tenants…</span>
  }
  if (!tenants.length) {
    return <span className="text-amber-600">No tenants found</span>
  }

  return (
    <span className="inline-flex items-center gap-1.5">
      <span className="text-slate-400">Tenant</span>
      <span className="text-slate-400">/</span>
      {tenants.length > 1 ? (
        <Dropdown
          value={selectedTenant ?? ""}
          onChange={selectTenant}
          options={tenants.map((t) => {
            const name = tenantDisplayName(t)
            return { value: name, label: name }
          })}
          size="sm"
          className="min-w-[140px]"
        />
      ) : (
        <span className="font-medium text-slate-900">{selectedTenant}</span>
      )}
    </span>
  )
}
