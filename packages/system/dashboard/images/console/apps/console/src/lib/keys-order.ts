import type { UiSchema } from "@rjsf/utils"

/**
 * ApplicationDefinition.spec.dashboard.keysOrder is a list of key paths that
 * tell the dashboard in what order to render form fields. Entries that refer
 * to `apiVersion`, `kind` or `metadata` are meta and handled outside of the
 * spec form — we only care about paths starting with `spec`.
 *
 * Example:
 *   keysOrder = [
 *     ["apiVersion"],
 *     ["kind"],
 *     ["metadata"],
 *     ["metadata", "name"],
 *     ["spec", "storageClass"],
 *     ["spec", "nodeGroups"],
 *     ["spec", "nodeGroups", "md0"],
 *     ["spec", "nodeGroups", "md0", "minReplicas"],
 *     ["spec", "nodeGroups", "md0", "maxReplicas"],
 *   ]
 *
 * This function returns a UiSchema where `ui:order` arrays are assembled per
 * nesting level so that RJSF renders top-level spec keys, then nested keys,
 * in the requested order.
 */
export function keysOrderToUiSchema(
  keysOrder: ReadonlyArray<ReadonlyArray<string>> | undefined,
): UiSchema {
  if (!keysOrder || keysOrder.length === 0) return {}

  // Group paths by their parent path.
  const groups = new Map<string, string[]>()
  for (const path of keysOrder) {
    if (path.length < 2 || path[0] !== "spec") continue
    const parent = path.slice(0, -1).join(".")
    const key = path[path.length - 1]
    const arr = groups.get(parent) ?? []
    if (!arr.includes(key)) arr.push(key)
    groups.set(parent, arr)
  }

  const root: UiSchema = {}
  for (const [parent, order] of groups) {
    // Strip the leading "spec" since RJSF is applied to the spec subtree.
    const parts = parent.split(".").slice(1)
    let target: Record<string, unknown> = root as Record<string, unknown>
    for (const p of parts) {
      target[p] = target[p] ?? {}
      target = target[p] as Record<string, unknown>
    }
    target["ui:order"] = [...order, "*"]
  }
  return root
}

/**
 * Cozystack OpenAPI schemas use Kubernetes-specific extensions that JSON
 * Schema validators don't understand. The ones we care about:
 *
 *   - `x-kubernetes-int-or-string` together with `anyOf: [{integer},{string}]`
 *     — the field accepts either; we let the form render a text input and
 *     cast on submit.
 *   - `x-kubernetes-preserve-unknown-fields: true` on an object without
 *     properties — arbitrary nested values allowed. Left as a "raw" object
 *     (handled via additionalProperties: true).
 *   - `x-kubernetes-validations` — CEL rules. We drop them here entirely.
 *     AJV has no CEL evaluator so the rules are pure noise to the form
 *     validator; immutability is harvested up-front by `findImmutablePaths`
 *     on the raw schema (see lib/immutable-paths.ts) and surfaced through
 *     uiSchema, not through AJV.
 */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function sanitizeSchema(schema: any): any {
  if (!schema || typeof schema !== "object") return schema
  if (Array.isArray(schema)) return schema.map(sanitizeSchema)

  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(schema)) {
    if (k === "anyOf" && schema["x-kubernetes-int-or-string"]) continue
    // CEL validations have no JSON-Schema validator-side semantics; the UI
    // extracts immutability paths up-front in SchemaForm and then strips
    // the rules so AJV doesn't waste cycles traversing them.
    if (k === "x-kubernetes-validations") continue
    out[k] = sanitizeSchema(v)
  }

  if (schema["x-kubernetes-int-or-string"]) {
    out.type = "string"
  }
  if (schema["x-kubernetes-preserve-unknown-fields"] && !schema.properties) {
    out.type = "object"
    out.additionalProperties = true
  }

  // Replace "Chart Values" title with "Parameters"
  if (out.title === "Chart Values") {
    out.title = "Parameters"
  }

  return out
}
