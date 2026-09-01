import { useEffect, useMemo, useState, type ReactNode } from "react"
import { useK8sList } from "@cozystack/k8s-client"
import type { TenantNamespace } from "@cozystack/types"
import { SELECTED_TENANT_KEY, TENANT_NAMESPACE_PREFIX } from "./constants.ts"
import { TenantContext, tenantDisplayName, type TenantContextValue } from "./tenant-context.tsx"

function persistTenant(name: string) {
  try {
    window.localStorage.setItem(SELECTED_TENANT_KEY, name)
  } catch {
    // ignore storage quota / private-mode failures
  }
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
      .sort((a, b) => tenantDisplayName(a).localeCompare(tenantDisplayName(b)))
  }, [list.data])

  useEffect(() => {
    if (!tenants.length) return
    if (selectedTenant && tenants.some((t) => tenantDisplayName(t) === selectedTenant)) return
    const fallback =
      tenants.find((t) => tenantDisplayName(t) === "root") ?? tenants[0]
    // Write the override through to storage, not just to state. Whatever the
    // stored name was, it is one the user cannot see — a revoked grant, or a
    // `?tenant=` naming someone else's tenant — and leaving it there makes
    // every later load open on it and correct itself again.
    persistTenant(tenantDisplayName(fallback))
    // eslint-disable-next-line react-hooks/set-state-in-effect -- falls back to an existing tenant once the list has loaded
    setSelectedTenant(tenantDisplayName(fallback))
  }, [tenants, selectedTenant])

  const selectTenant = (name: string) => {
    setSelectedTenant(name)
    persistTenant(name)
  }

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
