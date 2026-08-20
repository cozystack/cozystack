import type {
  ObjectFieldTemplateProps,
  ObjectFieldTemplatePropertyType,
  RJSFSchema,
  StrictRJSFSchema,
  FormContextType,
} from "@rjsf/utils"

/** Structural view of the JSON-schema nodes this template inspects. */
interface FieldSchemaNode {
  type?: string
  items?: { type?: string }
  anyOf?: unknown
  oneOf?: unknown
  allOf?: unknown
  "x-kubernetes-int-or-string"?: unknown
  properties?: Record<string, unknown>
}

function isSimpleField(schema: unknown): boolean {
  if (!schema || typeof schema !== "object") return true
  const node = schema as FieldSchemaNode
  const type = node.type
  if (type === "object") return false
  if (type === "array") {
    const itemType = node.items?.type
    return itemType === "integer" || itemType === "string" || itemType === "number"
  }
  if (node.anyOf || node.oneOf || node.allOf) {
    if (node["x-kubernetes-int-or-string"]) return true
    return false
  }
  return true
}

/** Read the addon `enabled` flag off form data of an unknown shape. */
function isAddonEnabled(formData: unknown): boolean {
  return (
    typeof formData === "object" &&
    formData !== null &&
    (formData as { enabled?: unknown }).enabled === true
  )
}

function groupByComplexity(
  properties: ObjectFieldTemplatePropertyType[],
  parentSchema: unknown,
) {
  type Group = { simple: boolean; items: ObjectFieldTemplatePropertyType[] }
  const groups: Group[] = []
  let current: Group | null = null
  const parentProperties =
    parentSchema && typeof parentSchema === "object"
      ? (parentSchema as FieldSchemaNode).properties
      : undefined

  for (const prop of properties) {
    const fieldSchema = parentProperties?.[prop.name]
    const simple = isSimpleField(fieldSchema)
    if (!current || current.simple !== simple) {
      current = { simple, items: [] }
      groups.push(current)
    }
    current.items.push(prop)
  }
  return groups
}

export function CustomObjectFieldTemplate<
  T = unknown,
  S extends StrictRJSFSchema = RJSFSchema,
  F extends FormContextType = FormContextType,
>(props: ObjectFieldTemplateProps<T, S, F>) {
  const { formData } = props

  // Addon pattern: has 'enabled' + other config fields → conditional expand
  const hasEnabledField = props.properties.some((p) => p.name === "enabled")
  const hasOtherFields = props.properties.some((p) => p.name !== "enabled")
  const isAddon = hasEnabledField && hasOtherFields

  if (isAddon) {
    const isEnabled = isAddonEnabled(formData)
    const enabledProp = props.properties.find((p) => p.name === "enabled")
    const otherProps = props.properties.filter((p) => p.name !== "enabled")
    const groups = groupByComplexity(otherProps, props.schema)

    return (
      <fieldset id={props.idSchema.$id} className="border border-slate-200 rounded-lg p-3 mb-3">
        {props.title && (
          <legend className="text-xs font-semibold text-slate-700 px-1">{props.title}</legend>
        )}
        {props.description && (
          <p className="field-description text-xs text-slate-400 mb-2">{props.description}</p>
        )}

        {enabledProp && <div className="mb-2">{enabledProp.content}</div>}

        {isEnabled && otherProps.length > 0 && (
          <div className="pl-4 border-l-2 border-blue-200 space-y-0.5">
            {groups.map((group, i) =>
              group.simple ? (
                <div key={i} className="grid-fields grid grid-cols-2 gap-x-3 xl:grid-cols-3">
                  {group.items.map((prop) => (
                    <div key={prop.name} className={group.items.length === 1 ? "col-span-full" : ""}>{prop.content}</div>
                  ))}
                </div>
              ) : (
                group.items.map((prop) => (
                  <div key={prop.name}>{prop.content}</div>
                ))
              )
            )}
          </div>
        )}

        {!isEnabled && otherProps.length > 0 && (
          <p className="text-xs text-slate-400 italic mt-1.5">
            Enable this addon to configure additional settings
          </p>
        )}
      </fieldset>
    )
  }

  // Default: smart 2-column grid for simple fields, full width for complex
  const groups = groupByComplexity(props.properties, props.schema)

  return (
    <fieldset id={props.idSchema.$id}>
      {props.title && <legend>{props.title}</legend>}
      {props.description && <p className="field-description">{props.description}</p>}
      {groups.map((group, i) =>
        group.simple ? (
          <div key={i} className="grid-fields grid grid-cols-2 gap-x-3 xl:grid-cols-3">
            {group.items.map((prop) => (
              <div key={prop.name} className={group.items.length === 1 ? "col-span-full" : ""}>{prop.content}</div>
            ))}
          </div>
        ) : (
          group.items.map((prop) => (
            <div key={prop.name}>{prop.content}</div>
          ))
        )
      )}
    </fieldset>
  )
}
