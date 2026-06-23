import { useState, useEffect } from "react"
import type { WidgetProps } from "@rjsf/utils"

export function SourceWidget(props: WidgetProps) {
  const { schema, value, onChange, id, label, required } = props
  const properties = schema.properties || {}
  const options = Object.keys(properties)

  const currentOption = value
    ? options.find((opt: string) => value[opt] !== undefined && value[opt] !== null)
    : undefined

  const [selected, setSelected] = useState<string | undefined>(currentOption)

  // Re-sync when value changes externally (e.g. async load in edit forms)
  useEffect(() => {
    const opt = value
      ? options.find((o: string) => value[o] !== undefined && value[o] !== null)
      : undefined
    setSelected(opt)
  }, [value])

  const handleSelect = (option: string) => {
    setSelected(option)
    const prop = properties[option]
    // If the property is an empty object marker (like upload: {}), set it to {}
    // Otherwise initialize with default value or empty object
    const defaultValue =
      prop && typeof prop === "object" && Object.keys((prop as any).properties || {}).length === 0
        ? {}
        : prop && typeof prop === "object" && (prop as any).type === "object"
          ? {}
          : ""
    onChange({ [option]: defaultValue })
  }

  const handleClear = () => {
    setSelected(undefined)
    onChange(undefined)
  }

  const renderFieldInput = (option: string) => {
    const prop = properties[option] as any
    if (!prop || typeof prop !== "object") return null

    // If it's an empty object marker (like upload: {}), show confirmation message
    if (Object.keys(prop.properties || {}).length === 0) {
      return (
        <div className="ml-6 mt-2 rounded-md bg-blue-50 border border-blue-200 p-3">
          <p className="text-sm text-blue-900 font-medium">
            ✓ {option} selected
          </p>
          <p className="text-xs text-blue-700 mt-1">
            {prop.description || "No additional configuration needed"}
          </p>
          {option === "upload" && (
            <p className="text-xs text-blue-600 mt-2">
              After creating the disk, you can upload an image using the UI or virtctl command.
            </p>
          )}
        </div>
      )
    }

    // Render input fields for the selected option
    const subProps = prop.properties || {}
    return (
      <div className="ml-6 mt-2 space-y-2">
        {Object.entries(subProps).map(([key, subProp]: [string, any]) => (
          <div key={key} className="flex flex-col gap-1">
            <label className="text-sm font-medium text-slate-700">
              {subProp.title || key}
              {prop.required?.includes(key) && <span className="text-red-500 ml-1">*</span>}
            </label>
            {subProp.description && (
              <p className="text-xs text-slate-500">{subProp.description}</p>
            )}
            <input
              type="text"
              value={(value?.[option] as Record<string, string>)?.[key] || ""}
              onChange={(e) => {
                onChange({
                  [option]: {
                    ...(value?.[option] as Record<string, unknown>),
                    [key]: e.target.value,
                  },
                })
              }}
              placeholder={subProp.title || key}
              className="rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-400"
            />
          </div>
        ))}
      </div>
    )
  }

  return (
    <div className="field">
      {label && (
        <label className="control-label mb-2 block">
          {label}
          {required && <span className="required ml-1">*</span>}
        </label>
      )}
      {schema.description && <p className="field-description mb-3">{schema.description}</p>}

      <div className="space-y-2">
        {options.map((option: string) => {
          const prop = properties[option] as any
          const optionDescription =
            typeof prop === "object" && prop ? prop.description : undefined
          const isSelected = selected === option

          return (
            <div key={option} className="rounded-lg border border-slate-200 p-3">
              <label className="flex items-start gap-2 cursor-pointer">
                <input
                  type="radio"
                  name={`${id}-source`}
                  checked={isSelected}
                  onChange={() => handleSelect(option)}
                  className="mt-0.5 size-4 rounded-full border-slate-300 text-blue-600 focus:ring-blue-500"
                />
                <div className="flex-1">
                  <div className="text-sm font-medium text-slate-900">{option}</div>
                  {optionDescription && (
                    <div className="text-xs text-slate-500 mt-0.5">{optionDescription}</div>
                  )}
                </div>
              </label>
              {isSelected && renderFieldInput(option)}
            </div>
          )
        })}
        {selected && (
          <button
            type="button"
            onClick={handleClear}
            className="mt-2 text-xs text-slate-500 hover:text-slate-700"
          >
            Clear selection
          </button>
        )}
      </div>
    </div>
  )
}
