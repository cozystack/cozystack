import type { Experimental_DefaultFormStateBehavior } from "@rjsf/utils"

export function isObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

export function withoutSchemaDefaults(schema: unknown, inReplaced = false): unknown {
  if (!isObject(schema)) return schema
  const result = { ...schema }
  delete result.default
  // Inside an object the server fills a field that has a default, so the form need
  // not require it. An array row or a dictionary value is stored as typed, with no
  // defaults added, so its requirements stay.
  if (!inReplaced && isObject(result.properties) && Array.isArray(result.required)) {
    const properties = result.properties
    const required = result.required.filter((name) => !(isObject(properties[name]) && "default" in properties[name]))
    if (required.length > 0) result.required = required
    else delete result.required
  }
  for (const key of ["properties", "patternProperties", "definitions", "$defs", "dependentSchemas"]) {
    if (isObject(result[key])) {
      result[key] = Object.fromEntries(Object.entries(result[key]).map(
        ([name, child]) => [name, withoutSchemaDefaults(child, inReplaced)],
      ))
    }
  }
  for (const key of ["items", "additionalProperties", "additionalItems"]) {
    if (isObject(result[key])) result[key] = withoutSchemaDefaults(result[key], true)
    else if (Array.isArray(result[key])) result[key] = result[key].map((child) => withoutSchemaDefaults(child, true))
  }
  if (Array.isArray(result.prefixItems)) result.prefixItems = result.prefixItems.map((child) => withoutSchemaDefaults(child, true))
  for (const key of ["not", "if", "then", "else"]) {
    if (isObject(result[key])) result[key] = withoutSchemaDefaults(result[key], inReplaced)
  }
  for (const key of ["oneOf", "anyOf", "allOf"]) {
    if (Array.isArray(result[key])) result[key] = result[key].map((child) => withoutSchemaDefaults(child, inReplaced))
  }
  return result
}

export const serverFormStateBehavior: Experimental_DefaultFormStateBehavior = {
  emptyObjectFields: "skipDefaults",
  arrayMinItems: { populate: "never" },
  constAsDefaults: "never",
}
