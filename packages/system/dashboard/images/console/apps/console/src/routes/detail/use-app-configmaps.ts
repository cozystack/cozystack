import { useMemo } from "react"
import { JSON_SCHEMA, load } from "js-yaml"
import { K8sApiError, useK8sList, type K8sResource } from "@cozystack/k8s-client"
import type { ApplicationDefinition, ApplicationInstance } from "@cozystack/types"
import { releasePrefix } from "../../lib/app-definitions.ts"

export type ConfigMap = K8sResource & { data?: Record<string, string> }

export function configMapNames(resources: string | undefined, namespace: string): string[] {
  if (!resources) return []
  const entries: unknown = load(resources, { schema: JSON_SCHEMA })
  if (!Array.isArray(entries)) throw new Error("The application's resource map must contain a list.")
  const names = new Set<string>()
  for (const value of entries as unknown[]) {
    if (!value || typeof value !== "object") continue
    const entry = value as Record<string, unknown>
    if (entry.apiVersion !== "v1" || entry.kind !== "ConfigMap") continue
    if (entry.namespace && entry.namespace !== namespace) continue
    if (typeof entry.name !== "string" || entry.name.length > 253 ||
      !/^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$/.test(entry.name)) {
      throw new Error("The application's resource map contains an invalid ConfigMap name.")
    }
    names.add(entry.name)
  }
  return [...names]
}

export function useApplicationConfigMaps(
  ad: ApplicationDefinition | undefined,
  instance: ApplicationInstance | undefined,
  namespace: string | undefined,
) {
  const name = ad && instance ? `${releasePrefix(ad)}${instance.metadata.name}-resourcemap` : ""
  const query = useK8sList<ConfigMap>(
    { apiGroup: "", apiVersion: "v1", plural: "configmaps", namespace },
    { enabled: !!name && !!namespace, fieldSelector: `metadata.name=${name}`, retry: false },
  )
  const resources = query.data?.items.find((item) => item.metadata.name === name)?.data?.resources
  const parsed = useMemo(() => {
    try {
      return { names: configMapNames(resources, namespace ?? ""), error: null }
    } catch (error) {
      return { names: [], error: error instanceof Error ? error : new Error("Invalid resource map.") }
    }
  }, [resources, namespace])
  const unavailable = query.error && query.error instanceof K8sApiError && [403, 404].includes(query.error.status)
  return { ...parsed, isLoading: query.isLoading, error: parsed.error ?? (unavailable ? null : query.error) ?? null }
}
