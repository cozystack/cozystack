import { useK8sGet } from "@cozystack/k8s-client"
import { graftOptionSources } from "./crd-option-sources.ts"

interface CRDVersion {
  name: string
  storage?: boolean
  schema?: {
    openAPIV3Schema?: {
      properties?: {
        spec?: unknown
      }
    }
  }
}

interface CRD {
  apiVersion: string
  kind: string
  metadata: {
    name: string
    annotations?: Record<string, string>
  }
  spec: {
    group: string
    versions: CRDVersion[]
  }
}

/**
 * Hook to fetch OpenAPI schema from a CRD's spec field
 */
export function useCRDSchema(crdName: string) {
  const { data: crd, isLoading, error } = useK8sGet<CRD>(
    {
      apiGroup: "apiextensions.k8s.io",
      apiVersion: "v1",
      plural: "customresourcedefinitions",
      name: crdName,
    },
    { enabled: !!crdName },
  )

  // Use the storage version (authoritative) or fall back to the first listed version
  const version = crd?.spec?.versions?.find((v) => v.storage) ?? crd?.spec?.versions?.[0]
  const specSchema = version?.schema?.openAPIV3Schema?.properties?.spec

  // Reattach the x-cozystack-options dropdown hints the apiserver strips from
  // the CRD schema; they are carried in metadata annotations instead.
  const schema = graftOptionSources(specSchema, crd?.metadata?.annotations)

  return {
    schema: schema ? JSON.stringify(schema) : null,
    isLoading,
    error,
  }
}
