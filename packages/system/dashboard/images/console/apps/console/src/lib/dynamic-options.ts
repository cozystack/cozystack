import type { RJSFSchema, UiSchema } from "@rjsf/utils"

/**
 * Walk a (sanitised) JSON schema and build a uiSchema that binds every field
 * carrying the `x-cozystack-options` vendor keyword to the generic
 * DynamicOptionsWidget. Recurses into `properties`, array `items` and
 * `additionalProperties` so nested fields (e.g. vm-instance `disks[].name`,
 * kubernetes `nodeGroups.*.instanceType`) are covered too.
 *
 * Shared by SchemaForm (top-level form) and AdditionalPropertiesField (the
 * custom map editor renders its own nested <Form>, so it must build the item
 * uiSchema with this same helper or the nested dropdowns are lost).
 */
function buildUi(node: unknown, ui: Record<string, unknown> = {}): Record<string, unknown> {
  if (!node || typeof node !== "object") return ui
  const n = node as Record<string, unknown>
  const result = { ...ui }

  if (n["x-cozystack-options"]) {
    result["ui:widget"] = "DynamicOptionsWidget"
  }

  const props = n.properties
  if (props && typeof props === "object") {
    for (const [key, child] of Object.entries(props as Record<string, unknown>)) {
      const childUi = buildUi(child, result[key] as Record<string, unknown>)
      if (Object.keys(childUi).length > 0) result[key] = childUi
    }
  }

  if (n.items && typeof n.items === "object") {
    const itemsUi = buildUi(n.items, result.items as Record<string, unknown>)
    if (Object.keys(itemsUi).length > 0) result.items = itemsUi
  }

  if (n.additionalProperties && typeof n.additionalProperties === "object") {
    const apUi = buildUi(
      n.additionalProperties,
      result.additionalProperties as Record<string, unknown>,
    )
    if (Object.keys(apUi).length > 0) result.additionalProperties = apUi
  }

  return result
}

export function addDynamicOptionWidgets(schema: RJSFSchema, uiSchema: UiSchema = {}): UiSchema {
  return buildUi(schema, uiSchema as Record<string, unknown>) as UiSchema
}

/**
 * Resolves `{path.to.field}` placeholders in an option source against the whole
 * form, so a dropdown can be scoped by a choice made elsewhere in it.
 *
 * Returns null when a placeholder has no value yet — the field it depends on is
 * still empty, and asking the server for `vmimportvm.` would only 404.
 */
export function resolveSource(
  source: string | undefined,
  root: unknown,
): { name: string | null; parameterised: boolean } {
  if (!source) return { name: null, parameterised: false }
  if (!source.includes("{")) return { name: source, parameterised: false }

  let missing = false
  const name = source.replace(/\{([^}]+)\}/g, (_, path: string) => {
    const resolved = path
      .split(".")
      .reduce<unknown>(
        (acc, key) =>
          acc && typeof acc === "object"
            ? (acc as Record<string, unknown>)[key]
            : undefined,
        root,
      )
    if (typeof resolved !== "string" || resolved === "") {
      missing = true
      return ""
    }
    return resolved
  })
  return { name: missing ? null : name, parameterised: true }
}
