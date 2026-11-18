import { useState } from "react"
import { errorId } from "@rjsf/utils"
import type { FieldProps } from "@rjsf/utils"

function displayValue(value: unknown) {
  return typeof value === "string" ? value : JSON.stringify(value ?? {}, null, 2)
}

export function FreeformObjectField({
  schema, formData, onChange, idSchema, name, required, readonly, disabled, uiSchema, rawErrors, hideError,
}: FieldProps) {
  const source = displayValue(formData)
  const [draft, setDraft] = useState<{ source: string, text: string } | null>(null)
  const value = draft?.source === source ? draft.text : source
  const invalid = typeof formData === "string"
  // A parsed object can still fail the schema, and RJSF lists those errors
  // under the field template rather than here.
  const hasSchemaErrors = Boolean(rawErrors?.length)
  const helpId = `${idSchema.$id}__json_help`
  const describedBy = [
    helpId,
    ...(uiSchema?.["ui:help"] ? [`${idSchema.$id}__help`] : []),
    ...(hasSchemaErrors && !hideError ? [errorId(idSchema)] : []),
  ].join(" ")

  return (
    <div className="form-group field">
      <label htmlFor={idSchema.$id} className="control-label mb-2 block text-sm font-medium text-slate-700">
        {schema.title ?? name}
        {required && <span className="required ml-1 text-red-500">*</span>}
      </label>
      {schema.description && <p className="mb-3 text-xs text-slate-500">{schema.description}</p>}
      <textarea
        id={idSchema.$id}
        value={value}
        rows={8}
        readOnly={readonly}
        disabled={disabled}
        spellCheck={false}
        aria-invalid={invalid || hasSchemaErrors}
        aria-describedby={describedBy}
        className="w-full rounded-lg border border-slate-300 bg-white p-3 font-mono text-sm text-slate-900 outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-400 disabled:opacity-50"
        onChange={(event) => {
          const text = event.target.value
          const change = (next: unknown) => {
            setDraft({ source: displayValue(next), text })
            onChange(next)
          }
          try {
            const parsed: unknown = JSON.parse(text)
            if (parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)) {
              change(parsed)
              return
            }
          } catch {
            // Keep incomplete input in formData so schema validation blocks submission.
          }
          change(text)
        }}
      />
      <p id={helpId} className={invalid ? "mt-1 text-xs text-red-600" : "mt-1 text-xs text-slate-500"}>
        {invalid ? "Enter a valid JSON object." : "Enter a JSON object. Nested objects and arrays are supported."}
      </p>
    </div>
  )
}
