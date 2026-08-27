import type {
  ObjectFieldTemplateProps,
  ObjectFieldTemplatePropertyType,
  RJSFSchema,
  StrictRJSFSchema,
  FormContextType,
} from "@rjsf/utils"

type CozystackSchema = RJSFSchema & {
  "x-cozystack-no-enable-switch"?: boolean
}

function isSimpleField(schema: any): boolean {
  if (!schema) return true
  const type = schema.type
  if (type === "object") return false
  if (type === "array") {
    const itemType = schema.items?.type
    return itemType === "integer" || itemType === "string" || itemType === "number"
  }
  if (schema.anyOf || schema.oneOf || schema.allOf) {
    if (schema["x-kubernetes-int-or-string"]) return true
    return false
  }
  return true
}

function groupByComplexity(
  properties: ObjectFieldTemplatePropertyType[],
  parentSchema: any,
) {
  type Group = { simple: boolean; items: ObjectFieldTemplatePropertyType[] }
  const groups: Group[] = []
  let current: Group | null = null

  for (const prop of properties) {
    const fieldSchema = parentSchema?.properties?.[prop.name]
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
  T = any,
  S extends StrictRJSFSchema = RJSFSchema,
  F extends FormContextType = any,
>(props: ObjectFieldTemplateProps<T, S, F>) {
  const { formData } = props

  // Addon pattern: has 'enabled' + other config fields → conditional expand
  const hasEnabledField = props.properties.some((p) => p.name === "enabled")
  const hasOtherFields = props.properties.some((p) => p.name !== "enabled")
  const isAddon = hasEnabledField && hasOtherFields

  // This must be declared by schema rather than inferred from the current
  // fields: an unrelated valuesOverride-only object must not inherit addon
  // copy, and adding another config field must not silently remove the notice.
  const hasNoToggle =
    !hasEnabledField &&
    (props.schema as CozystackSchema)["x-cozystack-no-enable-switch"] === true

  if (hasNoToggle) {
    return (
      <fieldset id={props.idSchema.$id} className="border border-slate-200 rounded-lg p-3 mb-3">
        {props.title && (
          <legend className="text-xs font-semibold text-slate-700 px-1">{props.title}</legend>
        )}
        <p className="text-xs text-slate-500 mb-2">
          No enable switch. Adding an enabled field in YAML has no effect.
        </p>
        {props.description && (
          <p className="field-description text-xs text-slate-400 mb-2">{props.description}</p>
        )}
        {props.properties.map((prop) => (
          <div key={prop.name}>{prop.content}</div>
        ))}
      </fieldset>
    )
  }

  if (isAddon) {
    const isEnabled = (formData as any)?.enabled === true
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
