import type { RJSFSchema, UiSchema } from "@rjsf/utils"

// Word tokens that look like credentials wherever they appear in the
// field name. `password` covers `password`, `rootPassword`,
// `passwordConfirmation`; the `*key` entries cover S3-style credentials
// in any prefix (`s3AccessKey`, `myApiKey`, etc.).
const ANYWHERE_TOKENS: ReadonlySet<string> = new Set([
  "accesskey",
  "secretkey",
  "secretaccesskey",
  "password",
  "apikey",
  "privatekey",
])

// Word tokens that look like credentials ONLY when they are the last
// word of the field name, to avoid masking unrelated identifiers that
// happen to contain the word. `bearerToken`/`api_token` match;
// `tokenAudience`/`csrfTokenName`/`passwdFile` do not.
const SUFFIX_ONLY_TOKENS: ReadonlySet<string> = new Set([
  "token",
  "passwd",
])

/**
 * Split a field name into lower-case word parts, treating both
 * camelCase boundaries and `_` / `-` / `.` as word separators. Digits
 * stay attached to the preceding letter (`s3` ⇒ `s3`).
 */
function splitWords(name: string): string[] {
  return name
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .split(/[\s_\-.]+/)
    .filter(Boolean)
    .map((part) => part.toLowerCase())
}

/**
 * Return every joined run of consecutive word parts, so that
 * `secretAccessKey` and `secret_access_key` both produce the chunk
 * `secretaccesskey` for matching, without the detector having to
 * enumerate every casing/concatenation variant.
 */
function chunks(words: string[]): string[] {
  const out: string[] = []
  for (let start = 0; start < words.length; start++) {
    let chunk = ""
    for (let end = start; end < words.length; end++) {
      chunk += words[end]
      out.push(chunk)
    }
  }
  return out
}

/**
 * Returns true when a field name looks like a credential by convention.
 * Cozystack chart schemas do not annotate sensitive fields with a
 * `format: password` hint, so detection is purely name-based. The match
 * is on word boundaries (camelCase / `_` / `-` / `.`) rather than a raw
 * substring, so neighbours like `tokenAudience` or `passwdFile` stay
 * out of the set.
 */
export function isSensitiveFieldName(name: string): boolean {
  const words = splitWords(name)
  if (words.length === 0) return false

  for (const chunk of chunks(words)) {
    if (ANYWHERE_TOKENS.has(chunk)) return true
  }

  const last = words[words.length - 1]
  return SUFFIX_ONLY_TOKENS.has(last)
}

interface JsonSchemaLike {
  type?: string
  enum?: unknown[]
  properties?: Record<string, JsonSchemaLike>
  // JSON Schema allows `items` to be a single schema or a tuple of schemas.
  items?: JsonSchemaLike | JsonSchemaLike[]
}

/**
 * Recursively walks `schema` and, for every string field whose name matches
 * a sensitive pattern, sets `ui:widget` to `SensitiveStringWidget` on the
 * corresponding entry in `uiSchema`. Existing widget bindings are preserved.
 *
 * The input `uiSchema` is not mutated; the returned object is independent.
 *
 * Limitation: only `properties` and `items` subtrees are walked. Fields
 * inside `oneOf` / `anyOf` / `allOf` branches are NOT inspected — see the
 * "pin broken behaviour" test in sensitive-fields.test.ts. Extend when a
 * chart in the catalogue starts using that shape.
 */
export function addSensitiveStringWidgets(
  schema: RJSFSchema,
  uiSchema: UiSchema = {},
): UiSchema {
  if (!schema || typeof schema !== "object") return uiSchema
  const properties = (schema as JsonSchemaLike).properties
  if (!properties) return uiSchema

  const result: UiSchema = { ...uiSchema }

  for (const [key, fieldSchema] of Object.entries(properties)) {
    if (!fieldSchema || typeof fieldSchema !== "object") continue

    const existing = (result[key] ?? {}) as UiSchema
    const alreadyHasWidget = typeof existing["ui:widget"] === "string"

    const isLeafString =
      fieldSchema.type === "string" && !Array.isArray(fieldSchema.enum)

    if (isLeafString && isSensitiveFieldName(key) && !alreadyHasWidget) {
      result[key] = { ...existing, "ui:widget": "SensitiveStringWidget" }
      continue
    }

    if (fieldSchema.properties) {
      result[key] = addSensitiveStringWidgets(
        fieldSchema as RJSFSchema,
        existing,
      )
      continue
    }

    if (fieldSchema.type === "array" && fieldSchema.items) {
      const itemSchemas = Array.isArray(fieldSchema.items)
        ? fieldSchema.items
        : [fieldSchema.items]
      // Shallow-copy so we never write through into the caller's items
      // sub-object — see "does not mutate the input uiSchema" tests.
      const merged: UiSchema = { ...((existing.items ?? {}) as UiSchema) }
      let mutated = false
      const keyIsSensitive = isSensitiveFieldName(key)
      for (const itemSchema of itemSchemas) {
        if (!itemSchema) continue
        if (itemSchema.properties) {
          const next = addSensitiveStringWidgets(
            itemSchema as RJSFSchema,
            merged,
          )
          for (const [k, v] of Object.entries(next)) merged[k] = v
          mutated = true
          continue
        }
        // Array of scalar credentials, e.g. `apiKey: { items: {type:"string"} }`.
        // The array key itself is the only signal we have for individual items,
        // so propagate the widget down only when the array's name matches.
        const itemIsLeafString =
          itemSchema.type === "string" && !Array.isArray(itemSchema.enum)
        if (
          itemIsLeafString &&
          keyIsSensitive &&
          typeof merged["ui:widget"] !== "string"
        ) {
          merged["ui:widget"] = "SensitiveStringWidget"
          mutated = true
        }
      }
      if (mutated) {
        result[key] = { ...existing, items: merged }
      }
    }
  }

  return result
}
