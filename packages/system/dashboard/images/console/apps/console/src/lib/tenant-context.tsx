import { createContext, useContext, useEffect, useRef } from "react"
import { useLocation, useSearchParams } from "react-router"
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

export const TenantContext = createContext<TenantContextValue | null>(null)

function displayName(ns: TenantNamespace): string {
  const name = ns.metadata.name
  return name.startsWith(TENANT_NAMESPACE_PREFIX)
    ? name.slice(TENANT_NAMESPACE_PREFIX.length)
    : name
}

export function useTenantContext(): TenantContextValue {
  const ctx = useContext(TenantContext)
  if (!ctx) throw new Error("useTenantContext must be used inside TenantProvider")
  return ctx
}

/**
 * The detail routes take the resource name from the URL and the namespace from
 * this context, so a link that crosses tenants has to name its tenant in the
 * URL: the browser-native paths a real anchor enables — middle click, open in
 * new tab, a bookmarked or pasted URL — never run React's onClick, and two
 * tenants under different parents can share a relative CR name, so the name
 * alone would resolve against whichever tenant happened to be selected last.
 * Applied once per navigation rather than on every render, so the tenant picker
 * -- which switches the tenant without leaving the page -- is not dragged back
 * to the URL under the user, while returning to the entry through history
 * asserts it again. The provider's own fallback stays free to reject a tenant
 * the user cannot see.
 */
export function useTenantFromUrl() {
  const { key } = useLocation()
  const [params] = useSearchParams()
  const wanted = params.get("tenant")
  const { selectTenant } = useTenantContext()
  const navigated = useRef<string | null>(null)

  useEffect(() => {
    const previous = navigated.current
    navigated.current = key
    if (!wanted || previous === key) return
    selectTenant(wanted)
  }, [key, wanted, selectTenant])
}

/**
 * Pull the display name of a TenantNamespace (no `tenant-` prefix).
 */
export function tenantDisplayName(ns: TenantNamespace): string {
  return displayName(ns)
}
