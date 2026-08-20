import { useEffect, useMemo, useState, type ReactNode } from "react"
import { useK8sList } from "@cozystack/k8s-client"
import type { TenantNamespace } from "@cozystack/types"
import { SELECTED_TENANT_KEY, TENANT_NAMESPACE_PREFIX } from "./constants.ts"
import {
  TenantContext,
  tenantDisplayName,
  type TenantContextValue,
} from "./tenant-context.tsx"

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
    setSelectedTenant(tenantDisplayName(fallback))
  }, [tenants, selectedTenant])

  const selectTenant = (name: string) => {
    setSelectedTenant(name)
    try {
      window.localStorage.setItem(SELECTED_TENANT_KEY, name)
    } catch {
      // ignore storage quota / private-mode failures
    }
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
