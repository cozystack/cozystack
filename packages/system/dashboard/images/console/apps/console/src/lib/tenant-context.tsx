import { createContext, useContext } from "react"
import type { TenantNamespace } from "@cozystack/types"
import { TENANT_NAMESPACE_PREFIX } from "./constants.ts"

export interface TenantContextValue {
  /**
   * Flat list of every TenantNamespace in the cluster, ordered by display
   * name (the namespace prefix stripped). Callers typically only need
   * `selectedTenant` / `tenantNamespace`, but the full list is exposed for
   * pickers and breadcrumbs.
   */
  tenants: TenantNamespace[]
  /** Display name (namespace minus the `tenant-` prefix). */
  selectedTenant: string | null
  selectTenant: (name: string) => void
  /** Namespace of the selected tenant — `tenant-<name>`. */
  tenantNamespace: string | null
  isLoading: boolean
  error: unknown
}

// TenantProvider lives in tenant-provider.tsx: a file exporting a component
// alongside a context or a hook breaks Fast Refresh.
export const TenantContext = createContext<TenantContextValue | null>(null)

export function useTenantContext(): TenantContextValue {
  const ctx = useContext(TenantContext)
  if (!ctx) throw new Error("useTenantContext must be used inside TenantProvider")
  return ctx
}

/**
 * Pull the display name of a TenantNamespace (no `tenant-` prefix).
 */
export function tenantDisplayName(ns: TenantNamespace): string {
  const name = ns.metadata.name
  return name.startsWith(TENANT_NAMESPACE_PREFIX)
    ? name.slice(TENANT_NAMESPACE_PREFIX.length)
    : name
}
