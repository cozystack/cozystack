import { useState, useEffect, useRef } from "react"
import type { FieldProps } from "@rjsf/utils"
import { X, Plus } from "lucide-react"

type QuotaMap = Record<string, string>

interface KnownRow {
  key: string
  label: string
  placeholder: string
  suffix?: string
  units?: readonly string[]
  defaultUnit?: string
}

const KNOWN_ROWS: KnownRow[] = [
  { key: "cpu", label: "CPU", placeholder: "e.g. 10", suffix: "cores" },
  { key: "memory", label: "Memory", placeholder: "e.g. 20", units: ["Mi", "Gi", "Ti"] as const, defaultUnit: "Gi" },
  { key: "storage", label: "Storage", placeholder: "e.g. 100", units: ["Mi", "Gi", "Ti"] as const, defaultUnit: "Gi" },
  { key: "services.loadbalancers", label: "Load Balancers", placeholder: "e.g. 5", suffix: "LBs" },
]

const KNOWN_KEYS = new Set(KNOWN_ROWS.map((r) => r.key))

// Accepts count/<resource> and extended resource patterns like <resource>.<group>
const VALID_CUSTOM_KEY = /^(?:count\/[a-z0-9]([a-z0-9._-]*[a-z0-9])?|[a-z0-9]([a-z0-9._/-]*[a-z0-9])?)$/

function parseSize(val: string, units: readonly string[]): { num: string; unit: string } {
  // Match longest suffix first to avoid partial matches
  const sorted = [...units].sort((a, b) => b.length - a.length)
  for (const u of sorted) {
    if (val.endsWith(u)) return { num: val.slice(0, -u.length), unit: u }
  }
  // Strip any trailing alphabetic chars so we don't corrupt data on save
  const numericPart = val.replace(/[A-Za-z]+$/, "")
  return { num: numericPart || val, unit: units[0] ?? "" }
}

function formatSize(num: string, unit: string): string {
  const n = num.trim()
  if (!n) return ""
  return `${n}${unit}`
}

interface KnownRowEditorProps {
  row: KnownRow
  value: string | undefined
  onChange: (val: string | undefined) => void
  readonly?: boolean
}

function KnownRowEditor({ row, value, onChange, readonly }: KnownRowEditorProps) {
  // Track checkbox state independently so clearing the number input doesn't
  // uncheck the row — the cleared state is buffered locally and not propagated.
  const [checked, setChecked] = useState(value !== undefined && value !== "")
  const [localNum, setLocalNum] = useState(() => {
    if (!value) return ""
    return row.units ? parseSize(value, row.units).num : value
  })
  const [localUnit, setLocalUnit] = useState(() => {
    if (!value || !row.units) return row.defaultUnit ?? row.units?.[0] ?? ""
    return parseSize(value, row.units).unit
  })

  // expectedValueRef tracks values we emitted ourselves so we can distinguish
  // an external reset (e.g. form data cleared from outside) from our own onChange.
  const expectedValueRef = useRef(value)

  useEffect(() => {
    if (value === expectedValueRef.current) return
    expectedValueRef.current = value
    const hasValue = value !== undefined && value !== ""
    setChecked(hasValue)
    if (hasValue && value) {
      setLocalNum(row.units ? parseSize(value, row.units).num : value)
      if (row.units) setLocalUnit(parseSize(value, row.units).unit)
    } else {
      setLocalNum("")
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value])

  const enabled = checked

  const handleToggle = () => {
    if (enabled) {
      setChecked(false)
      setLocalNum("")
      expectedValueRef.current = undefined
      onChange(undefined)
    } else {
      setChecked(true)
      // Don't propagate until the user fills in a number
    }
  }

  const handleNumChange = (num: string) => {
    setLocalNum(num)
    const trimmed = num.trim()
    if (!trimmed) {
      // Clear from parent data but keep checkbox checked locally
      expectedValueRef.current = undefined
      onChange(undefined)
      return
    }
    const next = row.units ? formatSize(trimmed, localUnit) : trimmed
    expectedValueRef.current = next
    onChange(next)
  }

  const handleUnitChange = (unit: string) => {
    setLocalUnit(unit)
    if (localNum.trim()) {
      const next = formatSize(localNum, unit)
      expectedValueRef.current = next
      onChange(next)
    }
  }

  return (
    <div className={`flex items-center gap-3 rounded-lg border px-3 py-2 transition-colors ${enabled ? "border-slate-200 bg-white" : "border-dashed border-slate-200 bg-slate-50/50"}`}>
      {!readonly && (
        <input
          type="checkbox"
          checked={enabled}
          onChange={handleToggle}
          className="size-4 rounded border-slate-300 accent-blue-600"
        />
      )}
      <span className={`w-28 shrink-0 text-sm font-medium ${enabled ? "text-slate-800" : "text-slate-400"}`}>
        {row.label}
      </span>
      {enabled ? (
        <div className="flex flex-1 items-center gap-1.5">
          <input
            type="text"
            value={localNum}
            onChange={(e) => handleNumChange(e.target.value)}
            placeholder={row.placeholder}
            disabled={readonly}
            className="w-28 rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-sm text-slate-900 outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-400 disabled:bg-slate-50 disabled:text-slate-500"
          />
          {row.units ? (
            <select
              value={localUnit}
              onChange={(e) => handleUnitChange(e.target.value)}
              disabled={readonly}
              className="rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm text-slate-700 outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-400 disabled:bg-slate-50"
            >
              {row.units.map((u) => (
                <option key={u} value={u}>{u}</option>
              ))}
            </select>
          ) : (
            <span className="text-sm text-slate-400">{row.suffix}</span>
          )}
        </div>
      ) : (
        <span className="flex-1 text-sm text-slate-400 italic">not set</span>
      )}
    </div>
  )
}

export function ResourceQuotasField(props: FieldProps) {
  const { schema, formData, onChange, readonly, disabled, name, required } = props
  const isReadonly = readonly || disabled

  const data: QuotaMap = formData ?? {}
  const customKeys = Object.keys(data).filter((k) => !KNOWN_KEYS.has(k))

  const [newKey, setNewKey] = useState("")
  const [newVal, setNewVal] = useState("")
  const [keyError, setKeyError] = useState("")

  const update = (key: string, val: string | undefined) => {
    const next = { ...data }
    if (val === undefined || val.trim() === "") {
      delete next[key]
    } else {
      next[key] = val
    }
    onChange(next)
  }

  const addCustom = () => {
    const k = newKey.trim()
    const v = newVal.trim()
    if (!k || !v) return
    if (!VALID_CUSTOM_KEY.test(k)) {
      setKeyError("Invalid key format")
      return
    }
    setKeyError("")
    update(k, v)
    setNewKey("")
    setNewVal("")
  }

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

      <div className="space-y-1.5">
        {KNOWN_ROWS.map((row) => (
          <KnownRowEditor
            key={row.key}
            row={row}
            value={data[row.key] as string | undefined}
            onChange={(val) => update(row.key, val)}
            readonly={isReadonly}
          />
        ))}

        {customKeys.length > 0 && (
          <div className="mt-3 space-y-1.5">
            <p className="text-xs font-medium text-slate-500 uppercase tracking-wide">Custom quotas</p>
            {customKeys.map((k) => (
              <div key={k} className="flex items-center gap-2">
                <span className="w-40 shrink-0 font-mono text-xs text-slate-600">{k}</span>
                <input
                  type="text"
                  value={data[k] as string}
                  onChange={(e) => update(k, e.target.value)}
                  disabled={isReadonly}
                  className="flex-1 rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-sm text-slate-900 outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-400 disabled:bg-slate-50"
                />
                {!isReadonly && (
                  <button
                    type="button"
                    onClick={() => update(k, undefined)}
                    className="rounded-md p-1.5 text-slate-400 hover:bg-red-50 hover:text-red-500"
                  >
                    <X className="size-3.5" />
                  </button>
                )}
              </div>
            ))}
          </div>
        )}

        {!isReadonly && (
          <div className="mt-3 space-y-1">
            <div className="flex items-center gap-2 pt-2 border-t border-slate-100">
              <div className="flex flex-col gap-0.5">
                <input
                  type="text"
                  value={newKey}
                  onChange={(e) => { setNewKey(e.target.value); setKeyError("") }}
                  placeholder="custom key"
                  className={`w-40 rounded-md border px-2.5 py-1.5 text-sm font-mono text-slate-900 outline-none focus:ring-1 focus:ring-blue-400 placeholder:font-sans placeholder:text-slate-400 ${keyError ? "border-red-400 focus:border-red-400" : "border-slate-300 bg-white focus:border-blue-400"}`}
                />
              </div>
              <input
                type="text"
                value={newVal}
                onChange={(e) => setNewVal(e.target.value)}
                placeholder="value"
                onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); addCustom() } }}
                className="flex-1 rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-sm text-slate-900 outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-400 placeholder:text-slate-400"
              />
              <button
                type="button"
                onClick={addCustom}
                disabled={!newKey.trim() || !newVal.trim()}
                className="flex items-center gap-1 rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-sm font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-40 disabled:cursor-not-allowed"
              >
                <Plus className="size-3.5" /> Add
              </button>
            </div>
            {keyError && <p className="text-xs text-red-500">{keyError}</p>}
          </div>
        )}
      </div>
    </div>
  )
}
