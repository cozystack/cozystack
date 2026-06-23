import { useEffect, useRef, useState } from "react"

interface KeyValuePair {
  key: string
  value: string
}

interface KeyValueEditorProps {
  value: Record<string, unknown>
  onChange: (value: Record<string, unknown>) => void
  readonly?: boolean
}

function toPairs(value: Record<string, unknown>): KeyValuePair[] {
  return Object.entries(value || {}).map(([key, val]) => ({
    key,
    value: typeof val === "string" ? val : JSON.stringify(val),
  }))
}

export function KeyValueEditor({ value, onChange, readonly }: KeyValueEditorProps) {
  const [pairs, setPairs] = useState<KeyValuePair[]>(() => toPairs(value))
  // Tracks whether the latest value change originated inside this component.
  // If so, the incoming prop update is our own echo and we skip re-sync.
  const internalChange = useRef(false)

  useEffect(() => {
    if (internalChange.current) {
      internalChange.current = false
      return
    }
    setPairs(toPairs(value))
  }, [value])

  const updatePairs = (newPairs: KeyValuePair[]) => {
    internalChange.current = true
    setPairs(newPairs)
    const obj: Record<string, unknown> = {}
    for (const pair of newPairs) {
      if (pair.key.trim()) {
        try {
          obj[pair.key] = JSON.parse(pair.value)
        } catch {
          obj[pair.key] = pair.value
        }
      }
    }
    onChange(obj)
  }

  const addPair = () => {
    updatePairs([...pairs, { key: "", value: "" }])
  }

  const removePair = (index: number) => {
    updatePairs(pairs.filter((_, i) => i !== index))
  }

  const updateKey = (index: number, key: string) => {
    const newPairs = [...pairs]
    newPairs[index] = { ...newPairs[index], key }
    updatePairs(newPairs)
  }

  const updateValue = (index: number, value: string) => {
    const newPairs = [...pairs]
    newPairs[index] = { ...newPairs[index], value }
    updatePairs(newPairs)
  }

  if (readonly && pairs.length === 0) {
    return <div className="text-sm text-slate-500 italic">No overrides</div>
  }

  return (
    <div className="space-y-2">
      {pairs.map((pair, index) => (
        <div key={index} className="flex gap-2 items-start">
          <input
            type="text"
            placeholder="key"
            value={pair.key}
            onChange={(e) => updateKey(index, e.target.value)}
            disabled={readonly}
            className="flex-1 rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 placeholder:text-slate-400 outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-400 disabled:bg-slate-50 disabled:text-slate-500"
          />
          <input
            type="text"
            placeholder="value"
            value={pair.value}
            onChange={(e) => updateValue(index, e.target.value)}
            disabled={readonly}
            className="flex-1 rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 placeholder:text-slate-400 outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-400 disabled:bg-slate-50 disabled:text-slate-500"
          />
          {!readonly && (
            <button
              type="button"
              onClick={() => removePair(index)}
              className="rounded-md border border-red-300 bg-white px-3 py-2 text-sm font-medium text-red-600 hover:bg-red-50"
            >
              × Remove
            </button>
          )}
        </div>
      ))}
      {!readonly && (
        <button
          type="button"
          onClick={addPair}
          className="rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium text-slate-700 hover:bg-slate-50"
        >
          + Add Override
        </button>
      )}
    </div>
  )
}
