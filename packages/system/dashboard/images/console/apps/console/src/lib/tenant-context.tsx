import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react"
import { useLocation, useSearchParams } from "react-router"
import { useK8sList } from "@cozystack/k8s-client"
import type { TenantNamespace } from "@cozystack/types"
import { SELECTED_TENANT_KEY, TENANT_NAMESPACE_PREFIX } from "./constants.ts"

interface TenantContextValue {
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

const TenantContext = createContext<TenantContextValue | null>(null)

function displayName(ns: TenantNamespace): string {
  const name = ns.metadata.name
  return name.startsWith(TENANT_NAMESPACE_PREFIX)
    ? name.slice(TENANT_NAMESPACE_PREFIX.length)
    : name
}

export function TenantProvider({ children }: { children: ReactNode }) {
  const [selectedTenant, setSelectedTenant] = useState<string | null>(() => {
    if (typeof window === "undefined") return null
    return window.localStorage.getItem(SELECTED_TENANT_KEY)
  })

  const ns = selectedTenant ? `${TENANT_NAMESPACE_PREFIX}${selectedTenant}` : null

  // TenantNamespace is cluster-scoped, filter by parent tenant label
  // to show only child tenants of the selected tenant
  const labelSelector = ns ? `tenant.cozystack.io/${ns}` : undefined

  const list = useK8sList<TenantNamespace>(
    {
      apiGroup: "core.cozystack.io",
      apiVersion: "v1alpha1",
      plural: "tenantnamespaces",
    },
    { labelSelector }
  )

  const tenants = useMemo<TenantNamespace[]>(() => {
    return (list.data?.items ?? [])
      .slice()
      .sort((a, b) => displayName(a).localeCompare(displayName(b)))
  }, [list.data])


  const storeTenant = (name: string | null) => {
    try {
      if (name === null) window.localStorage.removeItem(SELECTED_TENANT_KEY)
      else window.localStorage.setItem(SELECTED_TENANT_KEY, name)
    } catch {
      // ignore storage quota / private-mode failures
    }
  }

  const selectTenant = (name: string) => {
    setSelectedTenant(name)
    storeTenant(name)
  }

  // The list is scoped by the selection itself, so a tenant that was deleted,
  // was never visible, or arrived from a URL comes back empty: the fallback
  // below then has no candidate and the picker nothing to offer, and the
  // session strands there with storage putting it back on every reload.
  // Forgetting the selection re-runs the query unscoped. A tenant always
  // carries its own label, so an empty list means it is gone, never that it
  // merely has no children.
  const stranded =
    !list.isLoading && !tenants.length && selectedTenant !== null
  const unresolved =
    !list.isLoading &&
    tenants.length > 0 &&
    !(selectedTenant && tenants.some((t) => displayName(t) === selectedTenant))

  useEffect(() => {
    if (!stranded && !unresolved) return
    const next = stranded
      ? null
      : displayName(tenants.find((t) => displayName(t) === "root") ?? tenants[0])
    if (next === null) storeTenant(null)
    setSelectedTenant(next)
  }, [stranded, unresolved, tenants])

  const value: TenantContextValue = {
    tenants,
    selectedTenant,
    selectTenant,
    tenantNamespace: list.isLoading ? null : ns,
    isLoading: list.isLoading,
    error: list.error,
  }

  return <TenantContext.Provider value={value}>{children}</TenantContext.Provider>
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
 * asserts it again. The value is not validated here: a tenant the user cannot
 * see resolves to an empty list, which the provider treats as a dead selection
 * and forgets.
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
