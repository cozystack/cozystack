/**
 * Namespace that hosts the top-level tenants (every tenant chart creates its
 * own namespace named `tenant-<name>`; the root tenant lives in `tenant-root`
 * and its children live there as well).
 */
export const ROOT_TENANT_NAMESPACE = "tenant-root"

export const TENANT_NAMESPACE_PREFIX = "tenant-"

export function tenantNamespace(tenantName: string): string {
  return `${TENANT_NAMESPACE_PREFIX}${tenantName}`
}

/**
 * Local storage key for remembering the tenant the user last selected.
 */
export const SELECTED_TENANT_KEY = "cozystack-ui:selected-tenant"
