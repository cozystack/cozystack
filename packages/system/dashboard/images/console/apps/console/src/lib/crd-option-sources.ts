/**
 * Graft the `x-cozystack-options` vendor keyword back onto a CRD's spec schema
 * from its metadata annotations.
 *
 * The backups.cozystack.io CRDs cannot carry `x-cozystack-options` in their
 * OpenAPI schema: apiextensions `JSONSchemaProps` is a closed struct that only
 * preserves `x-kubernetes-*` extensions, and server-side apply rejects any
 * other `x-` key outright. The field→source mapping the dropdowns need is
 * therefore stored in CRD metadata annotations (which the apiserver does
 * preserve) and reattached client-side here, so DynamicOptionsWidget — which
 * reads `x-cozystack-options.source` off the schema node — keeps working.
 *
 * Annotation contract (emitted by kubebuilder markers on the Go types):
 *   options.cozystack.io/source.<dotted spec-relative path> = <option source>
 * e.g. `options.cozystack.io/source.applicationRef.kind: appkind`.
 */

const SOURCE_ANNOTATION_PREFIX = "options.cozystack.io/source."

/**
 * Return a copy of `specSchema` with `x-cozystack-options: { source }` set on
 * every field named by a matching annotation. The input is not mutated. Paths
 * that do not resolve in the schema are skipped silently.
 */
export function graftOptionSources(
  specSchema: unknown,
  annotations: Record<string, string> | undefined | null,
): unknown {
  if (!specSchema || typeof specSchema !== "object" || !annotations) {
    return specSchema
  }

  const cloned = structuredClone(specSchema)

  for (const [key, source] of Object.entries(annotations)) {
    if (!source || !key.startsWith(SOURCE_ANNOTATION_PREFIX)) continue
    const path = key.slice(SOURCE_ANNOTATION_PREFIX.length).split(".")
    applySource(cloned, path, source)
  }

  return cloned
}

function applySource(specSchema: unknown, path: string[], source: string): void {
  let node = specSchema
  for (const segment of path) {
    const properties = (node as { properties?: Record<string, unknown> })?.properties
    if (!properties || typeof properties !== "object" || !(segment in properties)) {
      return
    }
    node = properties[segment]
  }
  if (node && typeof node === "object") {
    ;(node as Record<string, unknown>)["x-cozystack-options"] = { source }
  }
}
