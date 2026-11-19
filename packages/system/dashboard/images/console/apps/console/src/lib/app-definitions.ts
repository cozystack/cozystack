import { useMemo } from "react"
import {
  useK8sList,
  type ResourceRef,
  type K8sList,
} from "@cozystack/k8s-client"
import {
  COZYSTACK_GROUP,
  COZYSTACK_VERSION,
  APPS_GROUP,
  APPS_VERSION,
  type ApplicationDefinition,
  type ApplicationInstance,
} from "@cozystack/types"

const APPLICATION_DEFINITIONS_REF: ResourceRef = {
  apiGroup: COZYSTACK_GROUP,
  apiVersion: COZYSTACK_VERSION,
  plural: "applicationdefinitions",
}

export function useApplicationDefinitions() {
  return useK8sList<ApplicationDefinition>(APPLICATION_DEFINITIONS_REF)
}

export function useApplicationDefinition(name: string | undefined) {
  const list = useApplicationDefinitions()
  return {
    ...list,
    data: useMemo(
      () => (name ? list.data?.items.find((i) => i.metadata.name === name) : undefined),
      [list.data, name],
    ),
  }
}

export function appInstanceRef(ad: ApplicationDefinition, namespace?: string): ResourceRef {
  return {
    apiGroup: APPS_GROUP,
    apiVersion: APPS_VERSION,
    plural: ad.spec?.application.plural ?? "",
    namespace,
  }
}

export function useApplicationInstances(
  ad: ApplicationDefinition | undefined,
  namespace: string | undefined,
) {
  return useK8sList<ApplicationInstance>(
    {
      apiGroup: APPS_GROUP,
      apiVersion: APPS_VERSION,
      plural: ad?.spec?.application.plural ?? "",
      namespace,
    },
    { enabled: !!ad && !!namespace && !!ad.spec?.application.plural },
  )
}

/**
 * Cozystack-native order: IaaS → PaaS → NaaS → Administration, with anything
 * else appended alphabetically. Kept in one place so the sidebar, the
 * marketplace and any future UI stay consistent.
 */
export const CATEGORY_ORDER = ["IaaS", "PaaS", "NaaS", "Administration"]

export function compareCategories(a: string, b: string): number {
  const ai = CATEGORY_ORDER.indexOf(a)
  const bi = CATEGORY_ORDER.indexOf(b)
  if (ai !== -1 && bi !== -1) return ai - bi
  if (ai !== -1) return -1
  if (bi !== -1) return 1
  return a.localeCompare(b)
}

/**
 * Group application definitions by `dashboard.category`. Undefined category is
 * bucketed under "Other". Items inside a category are sorted alphabetically by
 * name, categories follow the Cozystack canonical order.
 */
export function groupByCategory(
  list: K8sList<ApplicationDefinition> | undefined,
  opts: { includeModules?: boolean; includeTenant?: boolean } = {},
): { category: string; items: ApplicationDefinition[] }[] {
  if (!list) return []
  const { includeModules = false, includeTenant = false } = opts
  const map = new Map<string, ApplicationDefinition[]>()
  for (const ad of list.items) {
    if (!includeModules && isTenantModule(ad)) continue
    if (!includeTenant && ad.spec?.application.kind === "Tenant") continue
    const category = ad.spec?.dashboard?.category ?? "Other"
    const bucket = map.get(category) ?? []
    bucket.push(ad)
    map.set(category, bucket)
  }
  return [...map.entries()]
    .map(([category, items]) => ({
      category,
      items: items.sort((a, b) => a.metadata.name.localeCompare(b.metadata.name)),
    }))
    .sort((a, b) => compareCategories(a.category, b.category))
}

export function iconDataUrl(ad: ApplicationDefinition): string | undefined {
  const icon = ad.spec?.dashboard?.icon
  if (!icon) return undefined
  return `data:image/svg+xml;base64,${icon}`
}

export function appDisplayName(ad: ApplicationDefinition): string {
  return ad.spec?.application.kind ?? ad.metadata.name
}

/**
 * The release prefix (usually `<kind>-`) is prepended to every resource created
 * by the application chart (Deployments, Services, Secrets, ...). We use it to
 * narrow list queries when we render a deployed application detail page.
 */
export function releasePrefix(ad: ApplicationDefinition): string {
  return ad.spec?.release?.prefix ?? `${ad.spec?.application.singular ?? ad.metadata.name}-`
}

/**
 * Tenant modules are singleton add-ons (etcd, ingress, monitoring,
 * seaweedfs, info) enabled through Tenant.spec flags. They are recognised
 * by `spec.dashboard.module === true` *and* `category === "Administration"`.
 * The `module` flag alone isn't enough: Harbor is flagged `module: true`
 * but sits under PaaS and is a regular multi-instance application.
 */
export function isTenantModule(ad: ApplicationDefinition): boolean {
  return (
    ad.spec?.dashboard?.module === true &&
    ad.spec?.dashboard?.category === "Administration"
  )
}
