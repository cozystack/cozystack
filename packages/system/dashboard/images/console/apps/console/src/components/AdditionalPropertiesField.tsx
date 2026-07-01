import { useState, useMemo } from "react"
import type { FieldProps, RJSFSchema, TemplatesType } from "@rjsf/utils"
import Form from "@rjsf/core"
import validator from "@rjsf/validator-ajv8"
import { customTemplates, customWidgets } from "./rjsf-templates.tsx"
import { addDynamicOptionWidgets } from "../lib/dynamic-options.ts"

export function AdditionalPropertiesField(props: FieldProps) {
  const { schema, formData, onChange, readonly, disabled, name, required } = props
  const [newKey, setNewKey] = useState("")

  // Get the schema for items from additionalProperties
  const itemSchema = useMemo(
    () => (schema.additionalProperties as RJSFSchema) || {},
    [schema.additionalProperties],
  )
  const keys = Object.keys(formData || {})

  // The nested <Form> renders its own subtree, so it needs its own uiSchema to
  // bind x-cozystack-options fields (e.g. nodeGroups.*.instanceType) to the
  // DynamicOptionsWidget — the parent SchemaForm's uiSchema does not reach here.
  const itemUiSchema = useMemo(() => addDynamicOptionWidgets(itemSchema), [itemSchema])

  // Create templates without submit button for nested forms
  const templatesWithoutSubmit = useMemo<Partial<TemplatesType>>(() => {
    return {
      ...customTemplates,
      ButtonTemplates: {
        ...customTemplates.ButtonTemplates,
        SubmitButton: () => null,
      },
    }
  }, [])

  const handleAddKey = () => {
    if (!newKey.trim()) return
    if (formData && newKey in formData) {
      alert(`Key "${newKey}" already exists`)
      return
    }

    // Initialize with default value from schema
    const defaultValue = itemSchema.type === "object" ? {} : itemSchema.default ?? ""
    onChange({ ...formData, [newKey]: defaultValue })
    setNewKey("")
  }

  const handleRemoveKey = (key: string) => {
    const newValue = { ...formData }
    delete newValue[key]
    onChange(newValue)
  }

  const handleValueChange = (key: string, newVal: unknown) => {
    onChange({ ...formData, [key]: newVal })
  }

  const isReadonly = readonly || disabled

  return (
    <div className="form-group field">
      {name && (
        <label className="control-label mb-2 block text-sm font-medium text-slate-700">
          {name}
          {required && <span className="required ml-1 text-red-500">*</span>}
        </label>
      )}
      {schema.description && (
        <p className="field-description mb-3 text-xs text-slate-500">{schema.description}</p>
      )}

      <div className="space-y-3">
        {keys.map((key) => (
          <div key={key} className="rounded-lg border border-slate-200 bg-slate-50 p-4">
            <div className="mb-2 flex items-center justify-between">
              <div className="font-mono text-sm font-semibold text-slate-900">{key}</div>
              {!isReadonly && (
                <button
                  type="button"
                  onClick={() => handleRemoveKey(key)}
                  className="rounded-md border border-red-300 bg-white px-2 py-1 text-xs font-medium text-red-600 hover:bg-red-50"
                >
                  × Remove
                </button>
              )}
            </div>
            <div className="rounded-md bg-white p-3">
              <Form
                tagName="div"
                schema={itemSchema}
                uiSchema={itemUiSchema}
                formData={formData[key]}
                validator={validator}
                templates={templatesWithoutSubmit}
                widgets={customWidgets}
                onChange={(e) => handleValueChange(key, e.formData)}
                disabled={isReadonly}
                liveValidate={false}
                showErrorList={false}
              />
            </div>
          </div>
        ))}

        {keys.length === 0 && isReadonly && (
          <div className="text-sm text-slate-500 italic">No entries</div>
        )}

        {!isReadonly && (
          <div className="flex gap-2">
            <input
              type="text"
              value={newKey}
              onChange={(e) => setNewKey(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault()
                  handleAddKey()
                }
              }}
              placeholder="Enter key name..."
              className="flex-1 rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 placeholder:text-slate-400 outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-400"
            />
            <button
              type="button"
              onClick={handleAddKey}
              disabled={!newKey.trim()}
              className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-50 disabled:cursor-not-allowed"
            >
              + Add
            </button>
          </div>
        )}
      </div>
    </div>
  )
}
